# Zed Agent 参考实现调研

> 参考目录：`../zed/crates`（只读）。
> 本文所有 `path:line` 均相对 `crates/`（唯一例外：`assets/settings/default.json` 相对仓库根 `zed/`），行号以本次实际读取的版本为准。
> 结论只写读到的代码；凡是参考实现里没有对应机制的，显式写「不存在」。

---

## 0. 范围与结论摘要

实际精读的文件：

| 文件 | 作用 |
|---|---|
| `agent/src/thread.rs`（9609 行） | 会话状态机、单轮循环、工具调度、权限授权、压缩、事件流 |
| `agent/src/agent.rs`（8660 行） | ACP 适配层、会话注册、保存 worker、项目上下文与 Skills 装载 |
| `agent/src/tool_permissions.rs` | 规则求值：硬编码安全规则 + allow/deny/confirm 正则 + 默认值 |
| `agent/src/tools/tool_permissions.rs` | 路径级授权（symlink 逃逸、敏感设置文件、脏 buffer） |
| `agent/src/tools/*.rs` | 各内置工具；权限调用发生在工具内部 |
| `agent/src/db.rs`、`thread_store.rs` | SQLite + zstd 快照持久化 |
| `acp_thread/src/acp_thread.rs`（23868 行） | UI 侧会话模型：工具卡片、权限请求、流式文本合帧 |
| `language_model_core/src/language_model_core.rs` | 流式事件枚举（最小粒度） |
| `language_model/src/language_model.rs` | 模型抽象与 Provider 契约 |
| `agent_ui/src/conversation_view/thread_view.rs` | 消息队列/steering 的 UI 侧实现 |
| `agent_settings/src/agent_settings.rs`、`assets/settings/default.json` | 权限规则结构与压缩阈值默认值 |

六个必答问题的答案（详见后文）：

1. **主循环靠什么推进**：靠「本次模型调用是否产生了 tool_result」。产生工具结果就带着结果再请求一次；没有工具结果就是 `end_turn`，直接返回。`StopReason` 不参与控制流（`agent/src/thread.rs:3133`、`agent/src/thread.rs:3542`）。
2. **tool_use 与 tool_result 配对**：tool_use 写进 assistant 消息的 `content`，tool_result 按 `tool_use.id` 写进同一消息的 `tool_results`（IndexMap）；发请求时只有拿到结果的 tool_use 才会被发出（`agent/src/thread.rs:703-709`、`agent/src/thread.rs:4131-4144`）。
3. **权限检查与审批时刻**：不在循环里统一做，而是**每个工具在自己 `run()` 内部、执行副作用之前**调用 `event_stream.authorize(...)`；设置能定论就直接返回，需要确认才发事件到前端并阻塞该工具（`agent/src/thread.rs:6596-6604`、`agent/src/tools/terminal_tool.rs:431-448`）。
4. **流式事件最小粒度**：Provider 层是 `Text(String)` / `Thinking{text}` 增量、`ToolUse{is_input_complete}`、`UsageUpdate` 等（`language_model_core/src/language_model_core.rs:42-69`）；UI 层再做 16ms 节流合帧（`acp_thread/src/acp_thread.rs:3520-3525`）。
5. **上下文何时压缩、保留什么**：每轮模型调用**之前**检查（`agent/src/thread.rs:2843`），阈值默认 90% 上下文（`assets/settings/default.json:1279-1290`），上下文窗口小于 80k 不压缩（`agent/src/thread.rs:4416-4423`）；压缩后保留「摘要 + 压缩点之前最多 80KB 的用户消息 + 压缩点之后的全部消息」（`agent/src/thread.rs:4967-5034`）。
6. **持久化与恢复**：整线程快照写 SQLite（zstd 压缩的 JSON blob，`agent/src/db.rs:501-574`），不是追加日志；恢复时 `from_db` + `replay` 逐条重放事件重建 UI（`agent/src/thread.rs:1564-1617`）。

---

## 1. 分层架构

```text
UI / 会话视图
  AcpThread（acp_thread）              ← UI 侧可变模型：entries、工具卡片、权限请求、流式文本
      ▲  ThreadEvent（mpsc，unbounded）
NativeAgentConnection.handle_thread_events   ← ACP 适配：ThreadEvent → AcpThread 方法调用
      ▲
Thread（agent）                        ← 引擎：消息历史、run_turn 循环、工具调度、压缩
      ├─ LanguageModelProvider::stream_completion  → LanguageModelCompletionStream
      ├─ AnyAgentTool::run(ToolCallEventStream)    → Task<Result<Output, Output>>
      ├─ ToolPermissionDecision / authorize
      └─ DbThread → ThreadsDatabase（SQLite + zstd）
```

- 引擎与 UI 之间只有一个 `mpsc::UnboundedSender<Result<ThreadEvent>>`（`agent/src/thread.rs:5457-5461`）。
- `ThreadEvent` 共 12 个变体，就是前后端的全部契约：`UserMessage / AgentText / AgentThinking / ToolCallUpdate / ToolCallAuthorization / ToolCallAuthorizationResolved / Elicitation / SubagentSpawned / Retry / ContextCompaction / ContextCompactionUpdate / Stop`（`agent/src/thread.rs:900-917`）。
- 适配层的映射是一个大 match，逐条转调 `AcpThread`（`agent/src/agent.rs:2297-2460`）。

---

## 2. 主循环

### 2.1 入口与结构

`Thread::send` 追加一条 `Message::User` 后调用 `run_turn`（`agent/src/thread.rs:2597-2629`）。`run_turn` 做四件事（`agent/src/thread.rs:2768-2830`）：

1. `flush_pending_message` + `cancel` 旧轮（避免旧任务把新轮 pending 消息刷掉，注释写在 `2772-2776`）。
2. 建事件通道与取消通道 `watch::channel(false)`。
3. `cx.spawn` 执行 `run_turn_internal`。
4. 结束时：正常 → `send_stop(EndTurn)`；`Refusal` → `send_stop(Refusal)` 且把本轮消息 `truncate` 掉；`MaxTokens` → `send_stop(MaxTokens)`；其他 → `send_error`（`2803-2823`）。

`run_turn_internal` 是一个**双层循环**（`agent/src/thread.rs:2832-3190`）：

```text
loop {                                  // 外层：一轮 = 一次模型请求 + 它触发的工具
  1. perform_compaction_if_needed()     // 每次模型调用之前
  2. 重读 model / 刷新工具 / build_completion_request
  3. provider.stream_completion(...)    // 内层：消费流式事件
     loop {
        select! { 下一个事件 | 某个工具完成 | cancel }
        批量取出所有就绪事件 → handle_completion_event
     }
  4. 等所有工具结果（FuturesUnordered）
  5. flush_pending_message + 生成标题
  6. 退出判定
}
```

关键行：压缩 `agent/src/thread.rs:2843`；重读模型与工具 `2924-2934`；发起请求 `2950-2953`；内层 `select!` `2963-2998`；批量处理 `3003-3041`；等待工具结果 `3138-3140`。

### 2.2 推进信号

唯一的推进信号是**工具结果**：

```rust
let end_turn = tool_results.is_empty() && early_tool_results.is_empty();
```

`agent/src/thread.rs:3133`。`end_turn == false` 时置 `intent = CompletionIntent::ToolResults`、`attempt = 0` 继续下一圈（`3186-3187`）。

模型流里的 `Stop` 原因被显式忽略：`Stop(StopReason::ToolUse | StopReason::EndTurn) => {}`（`agent/src/thread.rs:3542`）。也就是说，即使 provider 报了 `EndTurn`，只要本轮产生了 tool_use 并跑出了结果，循环照样继续；反过来，provider 报 `ToolUse` 但没有任何 tool_use 事件，也会结束。**控制流完全由「有没有工具结果」决定**。

### 2.3 退出条件（全部）

| 条件 | 位置 | 行为 |
|---|---|---|
| 没有工具结果 | `agent/src/thread.rs:3177-3178` | `return Ok(())` → `send_stop(EndTurn)` |
| 用户取消 | `3149-3152`；取消信号在 `2991-2997` 置 `cancelled` | `return Ok(())`，`cancel()` 已发 `Stop(Cancelled)` |
| 用户在消息边界要求停止（steering） | `3180-3185` | `return Ok(())`，队列里的下一条消息接管 |
| 模型拒答 | `3048-3052`、`3078-3131`、`3540` | 若该模型配置了 `refusal_fallback_model_id`，切到同 provider 的替代模型并 `continue`；没有可用 fallback 则 `Err(CompletionError::Refusal)` |
| 输出被截断 | `3541` | `Err(CompletionError::MaxTokens)` |
| 请求错误且重试耗尽 | `3154-3168` → `retry_completion_error` → `Err` | 外层 `send_error` |
| 压缩失败且不可重试 | `2901-2914` | 整轮失败 |

**不存在 maxTurns / 最大请求次数限制**：`run_turn_internal` 的外层没有计数变量（`2838-2842` 只有 `attempt` 用于重试，且工具续轮时被重置为 0，`3187`）。`StopReason::MaxTurnRequests` 只在适配外部 ACP agent 时透传（`agent/src/agent.rs:2465-2483`），原生循环从不产生它。

### 2.4 错误不打断循环

- 工具错误被结构化：`AgentTool::run` 的返回类型是 `Task<Result<Self::Output, Self::Output>>`，成功和失败都用同一个结构化 Output，失败也是给模型看的内容（trait 注释 `agent/src/thread.rs:5274-5286`）。
- 执行侧只在拿到 `Err` 时置 `is_error=true`，其余照常回灌（`agent/src/thread.rs:3751-3803`）。
- 工具名不存在、参数 JSON 解析失败，都立即产出一条 `is_error: true` 的结果（`3613-3645`、`3807-3874`）。
- 模型请求本身的错误走 `retry_completion_error`（`3196-3219`），按错误类别给固定或指数退避，`RetryStatus` 通过事件推给前端（`3209`）。

### 2.5 流中断时的兜底

流在 tool_use 之前结束（例如网络错误），会主动 drop 掉未收到最终参数的流式工具输入，避免工具永久等待（`agent/src/thread.rs:3063-3076`）。

---

## 3. 工具调用

### 3.1 并发模型：所有工具并发，没有读写分组

- 工具在拿到**完整参数**（`is_input_complete: true`）后立刻由 `handle_tool_use_event` 调用 `run_tool`（`agent/src/thread.rs:3688-3699`）；支持流式输入的工具在第一个增量就启动（见 §3.3）。
- `run_tool` 把工具 future 丢进 `cx.foreground_executor().spawn(...)` 并立刻返回一个 `Task`（`agent/src/thread.rs:3749-3750`）。
- 所有 `Task` 被收进 `FuturesUnordered<Task<(usize, LanguageModelToolResult)>>`（`agent/src/thread.rs:2958-2960`、`3043`），谁先完成谁先回灌。

> **与 `AGENTS.md` 中「只读工具并行、写工具串行」的假设不一致。** `AgentTool` / `AnyAgentTool` trait 里没有 `is_read_only` / `is_concurrency_safe` 之类的钩子（trait 定义见 `agent/src/thread.rs:5222-5354`），全仓 grep `read_only|is_concurrency` 在 `agent/src` 无命中。工具全部并发执行；安全性由各工具自己保证，例如编辑工具的 `old_text` 精确匹配（匹配不上就报错让模型重读，`agent/src/tools/edit_session.rs:976-982`）、`file_changed_since_last_read` 提示（`970-974`）、以及 buffer 有未保存修改时的二次确认（`agent/src/tools/edit_session.rs:742-746`、`1061-1121`）。
>
> 副作用：同一个模型响应里的两个 `edit_file` 会同时执行，各自打开同一个 buffer，由上述检查兜底，没有全局写锁。

### 3.2 tool_use / tool_result 的配对

数据结构（`agent/src/thread.rs:743-759`）：

```rust
pub struct AgentMessage {
    content: Vec<AgentMessageContent>,                                 // Text / Thinking / RedactedThinking / ToolUse
    tool_results: IndexMap<LanguageModelToolUseId, LanguageModelToolResult>,
    reasoning_details: Option<Arc<serde_json::Value>>,
}
```

- 记录 tool_use：`send_or_update_tool_use` 把 `AgentMessageContent::ToolUse` push 进当前 pending 消息，同时发 `ThreadEvent::ToolCallUpdate`（`agent/src/thread.rs:3888-3918`）。
- 记录 tool_result：`process_tool_result` 以 `tool_use_id` 为 key 插入同一消息的 `tool_results`，并把工具卡片状态改成 Completed/Failed（`agent/src/thread.rs:3392-3411`）。
- 组装请求：只发出「已经有结果」的 tool_use（`agent/src/thread.rs:703-709`）；结果统一放进一条 `Role::User` 的消息（`713-730`）。空内容会被替换成 `<Tool returned an empty string>`，注释说明 API 会把空串误判成没有结果（`720-726`）。
- 兜底不变量：`flush_pending_message` 遍历所有 tool_use，凡是缺结果的，全部补一条 `is_error=true`、内容为 `"Tool canceled by user"` 的结果（`agent/src/thread.rs:4126-4145`）。因此**每个 tool_use 必然有且只有一条 tool_result**，取消和异常也不例外。

### 3.3 流式参数：工具可以在参数还没收完时就开始执行

- Provider 在 `ContentBlockDelta` 阶段持续发 `ToolUse { is_input_complete: false, .. }`，并把不完整 JSON 用 `fix_streamed_json` 修复后尽量解析（`anthropic/src/completion.rs:644-668`）。
- 完整输入在 `ContentBlockStop` 时以 `is_input_complete: true` 发出；解析失败则发 `ToolUseJsonParseError`（`anthropic/src/completion.rs:688-701`）。
- 引擎侧：工具若声明 `supports_input_streaming()`，第一次收到部分输入就启动执行，并通过 `ToolInputSender` 持续灌入增量；收到 final 时 `send_full` 而不是重复启动（`agent/src/thread.rs:3647-3686`）。典型使用者是 `edit_file` / `write_file`（`agent/src/tools/edit_file_tool.rs:107-160`：路径一完整就建 `EditSession` 并开始追加编辑）。
- 如果工具在流式参数期间报错且输入还没收完，内层循环会提前 break，不再等剩余事件（`agent/src/thread.rs:2969-2989`）。

### 3.4 工具契约

`AgentTool` 关联类型 + 方法（`agent/src/thread.rs:5222-5302`）：

| 成员 | 说明 |
|---|---|
| `type Input: Deserialize + Serialize + JsonSchema` | 输入 schema 由 schemars 生成 |
| `type Output: Deserialize + Serialize + Into<LanguageModelToolResultContent>` | 输出同时用于模型与持久化 |
| `const NAME` | 工具名 |
| `fn description()` | 默认取 schema 的 `description` |
| `fn kind() -> acp_v2::ToolKind` | UI 图标/渲染类别 |
| `fn initial_title(input)` | 工具卡片初始标题 |
| `fn input_schema()` | 默认 `root_schema_for::<Input>()` |
| `fn supports_input_streaming()` | 默认 false |
| `fn supports_provider()` | 按 provider 过滤（如 web_search） |
| `fn allow_in_restricted_mode()` | 受限工作区是否可见/可跑，默认 true |
| `fn run(...) -> Task<Result<Output, Output>>` | 错误也用 Output 表达 |
| `fn replay(...)` | 从持久化结果重建 UI（不重新执行） |

工具结果统一包装为 `AgentToolOutput { llm_output, raw_output }`：`raw_output` 给 UI/持久化，`llm_output` 给模型（`agent/src/thread.rs:5306-5323`、`5395-5425`）。若模型不支持图片而工具返回了图片，会把图片替换成占位文本（`3748-3792`）。

### 3.5 注册与暴露

- 编译期清单：`tools!` 宏生成 `ALL_TOOL_NAMES`，并在编译期检查重名（`agent/src/tools.rs:102-182`、`199-224`）。
- 运行期注册：`Thread::add_default_tools` 逐个 `add_tool`；`spawn_agent` 只在 `depth < MAX_SUBAGENT_DEPTH(=1)` 时注册（`agent/src/thread.rs:2206-2280`、`77`）。
- 暴露给模型前有三道过滤（`Thread::enabled_tools`，`agent/src/thread.rs:4254-4346`）：
  1. 受限工作区过滤（`allow_in_restricted_mode`，`4269-4275`）；
  2. profile 白名单 + provider 兼容（`4287-4289`）；
  3. feature flag（`4304`，定义于 `agent/src/tools.rs:233-243`）。
- 工具名会被 sanitize 到 64 字符（`agent/src/thread.rs:79-99`、`76`）；MCP 工具重名时按 server id 加前缀（`4323-4343`）。
- 每轮请求前都会 `refresh_turn_tools`，因此用户中途改 profile / 切模型会在下一次工具续轮时生效（`agent/src/thread.rs:2920-2930`、`4348-4353`）。

---

## 4. 权限与审批

### 4.1 规则模型

设置结构（`agent_settings/src/agent_settings.rs:480-522`）：

```rust
struct ToolPermissions { default: ToolPermissionMode, tools: HashMap<tool, ToolRules> }
struct ToolRules { default: Option<ToolPermissionMode>,
                   always_allow: Vec<CompiledRegex>,
                   always_deny: Vec<CompiledRegex>,
                   always_confirm: Vec<CompiledRegex>,
                   invalid_patterns: Vec<InvalidRegexPattern> }
```

判定优先级（文档 + 实现，`agent/src/tool_permissions.rs:214-365`）：

1. **硬编码安全规则**（不可被任何设置覆盖）：拦截 `rm -rf /`、`rm -rf ~`、`rm -rf .`、`rm -rf ..` 等（`agent/src/tool_permissions.rs:19-106`）。
2. **always_deny**：任一输入命中即拒绝，短路（注意终端会先展开成子命令，`393-402`）。
3. **always_confirm**：任一命中 → Confirm（`404-418`）。
4. **always_allow**：所有输入（终端为所有子命令）都命中至少一条 allow 才算 Allow（`409-422`）。
5. **工具级 default**。
6. **全局 default**（`424-430`）。

补充约束：

- 任一正则编译失败 → 整个工具调用被拒，并给出修复提示（`265-271`、`445-460`）。
- 终端命令若含 `$VAR`、`$(...)`、反引号等替换，且不是「无条件全允许」，直接拒绝（`273-286`）。
- 终端命令先解析出所有子命令再逐条比对，防止 `ls && rm -rf /` 绕过 `^ls` 白名单（`302-355`）；shell 不支持 POSIX 连接语法时禁用 always_allow（`308-334`）。
- 路径类工具（edit/write/delete/move/copy/create_dir）会同时用原始路径和归一化路径求值，取更严格者（`agent/src/tool_permissions.rs:512-556`）。

### 4.2 审批发生在哪个时刻

**在工具内部、副作用之前**，而不是在循环里统一 gate。三个例证：

- 终端：先 `event_stream.authorize(command, context, cx)`，`authorize.await` 通过后才创建终端进程（`agent/src/tools/terminal_tool.rs:431-448`）。
- 编辑：`EditSession::new` 在打开/写入 buffer 之前 `context.authorize(...).await`（`agent/src/tools/edit_session.rs:723-725`）。
- 抓取：先过普通工具权限，再逐跳校验 host 授权，两次都通过才发请求（`agent/src/tools/fetch_tool.rs:249-266`、`279-283`）。

`run_authorization_loop` 的行为（`agent/src/thread.rs:6587-6705`）：

1. 先用当前设置求值：`Allow` 立即返回 `Ok`，`Deny(reason)` 立即返回 `Err`，只有 `Confirm` 才继续（`6595-6604`）。
2. 发送 `ThreadEvent::ToolCallAuthorization`（含 tool_call 更新、`PermissionOptions`、`oneshot` 回应通道）（`6620-6638`）。
3. 同时监听设置变更：一旦别的待审批工具点击了「Always for …」使规则变为 Allow/Deny，就自动解析当前 prompt，无需用户再点（`6652-6703`）。这条注释明确说明是为了解决「同一轮或子代理中其他 pending 工具拿不到新规则」的问题（`5900-5903`）。
4. 用户选择结果通过 `persist_permission_outcome` 落盘（always allow/deny → 写 settings；once → 不写）（`6709-6758`）。

**审批不阻塞模型流**：工具在独立 future 里等审批，内层事件循环继续消费模型事件、启动其它工具；只有到「等待所有工具结果」这一步才会整体阻塞（`agent/src/thread.rs:2958-2960`、`3138-3140`）。

### 4.3 前端拿到的选项

`ToolPermissionContext::build_permission_options` 是选项的唯一来源（`agent/src/thread.rs:987-1176`）：

- 普通工具：下拉，含「Always for {tool}」「Always for `<pattern>`」「Only this time」三种粒度；allow/deny 成对生成（`1120-1175`）。
- 终端多子命令：`DropdownWithPatterns`，可逐个勾选要永久放行的子命令（`1032-1072`）。
- symlink 目标：只有「Yes / No」（`993-1006`）。
- Skills：永远只有一次性 Allow/Deny（`1008-1022`）。
- 终端在 shell 不支持链式语法时，隐藏所有 Always 选项（`1024-1030`）。

工具还提供两个变体：

- `authorize_always_prompt`：无视设置、强制弹窗，用于 symlink 逃逸、敏感设置文件、skills 目录（`agent/src/thread.rs:5931-5945`，调用点 `agent/src/tools/tool_permissions.rs:477-555`）。
- `authorize_third_party_tool`：MCP 工具只按 tool id 授权，没有参数模式（`agent/src/thread.rs:5842-5889`）。

### 4.4 审批结果写入哪里

`persist_always_permission`（`agent/src/thread.rs:6762-6815`）：

- 终端子命令模式 → `add_tool_allow_pattern` / `add_tool_deny_pattern`；
- 否则 → `set_tool_default_permission`；
- 只对「always」写盘，「only this time」什么都不写（`6746-6753`）。

### 4.5 沙箱授权是独立的一条链路

`authorize_sandbox`（`agent/src/thread.rs:5955-6091`）处理网络/文件写入/完全去沙箱的**升级请求**，选项为 `Allow once / Allow for this thread / Allow always / Deny`（`5993-6014`）。线程级授权存在内存里的 `ThreadSandboxGrants`，会随线程一起持久化（字段 `agent/src/db.rs:84-89`，结构 `94-122`）；「always」写入 settings（`6213-6277`）。

另外 `terminal` 会拒绝「请求比当前已授予权限更窄」的沙箱参数，并回一段解释让模型去掉这些字段（`agent/src/tools/terminal_tool.rs:460-521`）。

### 4.6 不存在的权限模式

参考实现**没有** Claude Code 式的五档 permission mode（`default / acceptEdits / plan / bypassPermissions / dontAsk / auto`）。在 `agent/src`、`agent_settings/src`、`acp_thread/src` 中搜索 `bypass_permissions|permission_mode|accept_edits|dont_ask` 均无命中。等价的档位只能通过「全局 default + 工具级 default + 模式匹配」组合出来，且没有 plan 模式工具（`EnterPlanMode/ExitPlanMode` 不在 `ALL_TOOL_NAMES` 中，`agent/src/tools.rs:199-224`）。

---

## 5. 流式事件与 UI 映射

### 5.1 最小粒度

`LanguageModelCompletionEvent`（`language_model_core/src/language_model_core.rs:42-69`）：

| 事件 | 粒度 |
|---|---|
| `Queued { position }` / `Started` | 云端排队与开始 |
| `Text(String)` | 一个 SSE delta 片段的文本 |
| `Thinking { text, signature }` | thinking delta；签名也可能单独以空 text 到达（`anthropic/src/completion.rs:611-616`） |
| `RedactedThinking { data }` | 一段被加密的 thinking |
| `ToolUse(LanguageModelToolUse)` | 工具调用；`is_input_complete=false` 表示参数增量 |
| `ToolUseJsonParseError` | 参数最终解析失败（含 raw input 与错误信息） |
| `StartMessage { message_id }` | 新消息边界；收到后会 flush 上一条 pending 消息（`agent/src/thread.rs:3479-3482`） |
| `ReasoningDetails(json)` | 加密推理状态（OpenAI 系） |
| `UsageUpdate(TokenUsage)` | token 用量 |
| `Compaction(CompactionUpdate)` | provider 原生压缩（`Started/SummaryDelta/Finished/Failed`），引擎侧当前忽略（`agent/src/thread.rs:3543`） |

Provider 契约只有一个方法：`stream_completion(&self, model, request, cx) -> BoxFuture<Result<LanguageModelCompletionStream, _>>`（`language_model/src/language_model.rs:380-389`）。`stream_completion_text` / `stream_completion_tool` 是在它之上做的适配（`391-490`），不是新的传输层。

### 5.2 引擎到 UI

`ThreadEventStream` 是唯一的出口（`agent/src/thread.rs:5463-5630`）：

| 引擎动作 | 事件 |
|---|---|
| 文本增量 | `AgentText(String)`（`5477-5481`） |
| thinking 增量 | `AgentThinking(String)`（`5483-5487`） |
| 工具出现 | `ToolCallUpdate`，状态 `Pending`，带 name/kind/raw_input（`5489-5517`） |
| 工具字段变化 | `ToolCallUpdate` 局部 patch（`5519-5529`） |
| 工具开始执行 | `ToolCallUpdate{status: InProgress}`（`agent/src/thread.rs:3747`） |
| 工具结束 | `ToolCallUpdate{status: Completed/Failed, raw_output}`（`3392-3406`） |
| 需要授权 | `ToolCallAuthorization`（`6620-6638`） |
| 授权被设置变更自动解析 | `ToolCallAuthorizationResolved`（`5531-5542`） |
| 压缩 | `ContextCompaction` + 多个 `ContextCompactionUpdate`（`5550-5606`） |
| 重试 | `Retry(RetryStatus)`（`5544-5548`） |
| 结束 | `Stop(reason)`（`5608-5612`） |

工具还能主动发：`update_diff(Entity<Diff>)`（编辑实时 diff，`5814-5825`）、`subagent_spawned`（`5827-5832`）、`request_elicitation`（结构化表单，`ask_user` 用，`agent/src/tools/ask_user_tool.rs:116-130`）。

### 5.3 合批与合帧

- **引擎侧合批**：拿到第一个事件后，用 `events.next().now_or_never()` 把已经就绪的事件一次性收进 batch，在同一个 `this.update(...)` 里处理，减少调度开销（`agent/src/thread.rs:3003-3041`）。
- **UI 侧合帧**：文本不直接逐 delta 渲染，先写入 Markdown 源，再由 `StreamingTextBuffer` 以 16ms 一帧、目标 200ms 内显示完积压文本的速率揭示（`acp_thread/src/acp_thread.rs:3512-3541`、`5018-5051`、`4867-4879`）。
- **工具卡片状态**：`authorization.is_some()` 时对外状态恒为 `WaitingForConfirmation`（`acp_thread/src/acp_thread.rs:1364-1370`）；收到授权结果后按选项类型转为 `InProgress` 或 `Rejected`（`6059-6070`）。
- 工具 id 做了 message-index 作用域化，原因是某些 provider 每次请求都从 `call_1` 重新编号，会跨轮撞车（`agent/src/thread.rs:5440-5455`）。

### 5.4 事件与 UI 条目的对应

- `AgentText`/`AgentThinking` → `push_assistant_content_block`，追加到同一个 assistant entry（`agent/src/agent.rs:2317-2326`）。
- `ToolCallUpdate` → `AcpThread::update_tool_call`，v2 走 `upsert_local_tool_call`：存在则 patch，不存在则创建（`acp_thread/src/acp_thread.rs:5319-5370`、`5545-5560`）。
- `ToolCallAuthorization` → `request_tool_call_update_authorization` 安装一个 `PermissionRequest`，返回一个等待用户选择的 `Task`（`acp_thread/src/acp_thread.rs:5899-5973`）；适配层把结果写回 `oneshot`（`agent/src/agent.rs:2327-2357`）。

---

## 6. 上下文与压缩

### 6.1 何时压缩

- 位置：**外层循环每圈开头、发起模型请求之前**（`agent/src/thread.rs:2843`）。
- 开关与门槛：设置 `auto_compact.enabled`（默认 true，`assets/settings/default.json:1281`）+ 输入容量 ≥ 80,000 token（`agent/src/thread.rs:4416-4423`、`124`）。
- 阈值：默认 `90%`（`assets/settings/default.json:1290`），支持 `Percentage / TokensUsed / TokensRemaining` 三种写法（`agent_settings/src/agent_settings.rs:148-208`），换算见 `agent/src/thread.rs:4758-4771`。
- 触发量：取**最后一条有 usage 记录的用户消息**的 `input + output`（含 cache 读写）作为当前上下文占用（`agent/src/thread.rs:4542-4569`、`4740-4745`）。
- 幂等：如果该 usage 之后已经存在一条压缩消息，就不再重复压缩（`4558-4562`）。

### 6.2 怎么压缩

`stream_compaction`（`agent/src/thread.rs:3257-3381`）：

1. 发 `ContextCompaction{InProgress}`（`3271-3275`）。
2. 用压缩模型发一次普通 completion，请求体是「截至插入点的完整历史 + 一条固定的压缩提示词」（`4603-4626`）。压缩模型优先取 registry 的 `compaction_model()`，否则用当前模型（`4597-4601`）。
3. 摘要 delta 实时推给 UI（`3308-3312`）。
4. 成功且非空 → 插入一条 `Message::Compaction(CompactionInfo::Summary)`（`3336-3368`）。

压缩提示词是固定的五段式交接文档：Goal / State / Context / Next / Pitfalls（`agent_settings/src/prompts/compaction_prompt.txt`）。

**不存在 microCompact / sessionMemoryCompact**：`agent/src`、`acp_thread/src`、`agent_settings/src` 中搜索 `micro.compact` 无命中；只有「阈值触发的 auto」和「`/compact` 手动」两条路径（`agent/src/thread.rs:4775-4777` 的 telemetry trigger 取值 `"auto" | "manual"`）。

### 6.3 压缩后保留什么

请求历史的拼装规则（`agent/src/thread.rs:4967-5034`）：

```text
[System prompt]
+ 压缩点之前、最近的用户消息（从后往前，总字节预算 80_000；放不下就截断最后一条并停止）
+ 压缩消息本身（渲染成 "The previous conversation was compacted. Use this summary as context: ..."）
+ 压缩点之后的全部消息
```

- 预算常量 `COMPACTION_RETAINED_USER_MESSAGES_BYTE_BUDGET = 80_000`（`agent/src/thread.rs:127`），按字节而非 token。
- 只保留**用户消息**，旧的 assistant / tool_result 一律丢弃（`5008-5014`）。
- 空内容的用户消息跳过（`5012-5014`），保证手动 `/compact` 插入的占位消息不进入请求。
- 自动压缩的插入点可能落在「最后一条还没被回答的用户消息」之前（`4571-4582`、`4898-4906`），这样这条新消息不会被压掉。

### 6.4 手动压缩

`/compact` 走 `Thread::compact`（`agent/src/thread.rs:2633-2723`）：先 flush + 取消在跑的轮次，再强制压缩，插入方式是「零内容用户消息 + 摘要消息」（`3359-3365`，`CompactionInsertion::Manual`）。没有内容可压（空线程或已以压缩结尾）时直接返回（`4587-4595`）。

### 6.5 其他上下文注入

- 系统提示包含：项目根目录、沙箱策略、用户级 `AGENTS.md`、项目规则文件、可用工具清单、日期（`agent/src/templates/system_prompt.hbs:150-182`、`248-279`；渲染在 `agent/src/thread.rs:4444-4493`）。
- 每条用户消息还可以带 `<context>` 块：打开的文件、目录、符号、选区、线程引用、抓取 URL、规则、诊断、diff、合并冲突、技能（`agent/src/thread.rs:329-360`）。

---

## 7. 模型抽象

- `LanguageModel` 是纯数据：provider/model 名、`max_token_count / max_input_tokens / max_total_tokens / max_output_tokens`、`supports_images / supports_tools / supports_thinking / supports_streaming_tools`、`refusal_fallback_model_id`、计费信息等（`language_model/src/language_model.rs:230-353`）。
- 能力通过 `LanguageModelProvider`（继承 `LanguageModelClient`）暴露：流式请求只有 `stream_completion` 一个方法（`language_model/src/language_model.rs:380-389`）；Provider 另外必须提供 `id / name / default_model / provided_models / is_authenticated / authenticate / settings_view`（`529-544`）。
- 输入容量不是直接用 `max_input_tokens`，而是 `min(max_input_tokens, max_total - max_output)`，为输出预留空间（`agent/src/thread.rs:4748-4756`）。
- 拒答降级：`Stop(Refusal)` 时查 `refusal_fallback_model_id`，同一 provider 内找替代模型，改线程模型并 `send_retry`，然后 `continue` 重来一次（`agent/src/thread.rs:3078-3131`）。
- 每轮重读模型与 provider，因此用户中途切换模型会在下一次工具续轮生效（`agent/src/thread.rs:2920-2934`）。
- 请求里带 `intent`：`UserPrompt / ToolResults / Subagent / ThreadContextSummarization`（`agent/src/thread.rs:4153-4163`、`4234`、`4612`），子代理的首轮会被改成 `Subagent`。

---

## 8. 会话持久化与恢复

### 8.1 存储形态

- SQLite，表 `threads(id, parent_id, folder_paths, folder_paths_order, summary, updated_at, data_type, data, created_at)`（`agent/src/db.rs:452-487`）。
- `data` 是**整线程 JSON 的 zstd 压缩**（压缩级别 3）（`agent/src/db.rs:507-541`）。
- 每次保存是 `INSERT ... ON CONFLICT(id) DO UPDATE SET ... data = excluded.data`，也就是**全量覆盖快照，不是追加日志**（`agent/src/db.rs:548-571`）。
- 快照带版本号 `DbThread::VERSION = "0.3.0"`，读到别的版本会走 `upgrade_from_agent_1` 迁移（`agent/src/db.rs:188-208`、`210-360`）。

### 8.2 快照内容

`DbThread`（`agent/src/db.rs:53-89`）：标题、消息数组、更新时间、详细摘要、初始项目快照、累计与按消息的 token 用量、模型、profile、subagent 上下文、speed、thinking 开关与档位、草稿输入、滚动位置、沙箱临时目录、线程级沙箱授权。

注意：

- `to_db` 只序列化 `self.messages`，**不包含正在流式生成的 `pending_message`**（`agent/src/thread.rs:1941-1946`）；正在流式时若内容没变会直接跳过保存（`agent/src/agent.rs:260-270`）。
- 保存由 `cx.observe(&thread_handle, ...)` 触发（`agent/src/agent.rs:877-879`），每次 notify 都可能触发。
- 写入被合并：同一 session 只有一个 `pending_save` 槽位，新快照覆盖旧快照，由单个 worker 消费（`agent/src/agent.rs:1849-1894`）；退出时对所有非空线程并发 flush（`1925-1957`）。

### 8.3 恢复

1. `ThreadStore::load_thread` 从数据库读 `DbThread`（`agent/src/thread_store.rs:53-63`）。
2. `Thread::from_db` 重建：恢复消息、token 用量、模型（解析不到就保留 `Unresolved` 选择）、profile、沙箱授权等（`agent/src/thread.rs:1776-1869`）。
3. `Thread::replay` 把历史逐条转成 `ThreadEvent` 重放给 UI（`agent/src/thread.rs:1564-1617`）：
   - User → `UserMessage`；Text/Thinking → `AgentText`/`AgentThinking`；
   - ToolUse + ToolResult → `send_tool_call` 后用工具的 `replay()` 恢复 diff/终端等富内容，再置 Completed/Failed（`1619-1725`）；
   - Compaction → `ContextCompaction{Completed}` + 摘要文本（`1594-1613`）；
   - `Message::Resume` 不重放（`1593`）。
4. 只有「取消占位结果」（`Tool canceled by user`）的工具调用不会重放（`1627-1632`）。

### 8.4 会话级操作

- 取消：`cancel` 向 `watch` 发 true，并向所有运行中的子代理递归取消（`agent/src/thread.rs:2356-2377`）。
- 截断（编辑历史后重发）：`truncate(client_user_message_id)` 从该用户消息起删掉全部内容，并清掉对应 token 用量（`agent/src/thread.rs:2459-2485`）。
- 重试：`retry` 在 `run_turn_internal` 里对可重试错误发 `Retry` 事件并退避（`3196-3219`、`4643-4693`）。
- 会话列表：列表查询只取元数据，且过滤掉 `parent_session_id.is_some()` 的子代理线程（`agent/src/thread_store.rs:106-128`）。

---

## 9. SubAgent

- 深度上限硬编码为 1：`MAX_SUBAGENT_DEPTH: u8 = 1`（`agent/src/thread.rs:77`）；子代理不再获得 `spawn_agent` 工具（`2269-2271`），直接创建时也会拒绝（`agent/src/agent.rs:3296-3301`）。
- 子代理是独立的 `Thread` 实体，有自己的消息历史、模型与工具表，但从父线程继承设置（`agent/src/agent.rs:3303-3307`、`agent/src/thread.rs:1358-1394`、`1498-1508`）。
- 父看到的只有子代理的**最终一条消息**：`spawn_agent` 调用 `SubagentHandle::send` 拿到 `String`，再序列化成 `{session_id, output}` 回给父模型（`agent/src/tools/spawn_agent_tool.rs:90-115`、`agent/src/agent.rs:3580`、`agent/src/thread.rs:769-777`）。
- 支持用 `session_id` 续跑已有子代理（`agent/src/tools/spawn_agent_tool.rs:182-192`）。
- 父取消时递归取消所有运行中的子代理（`agent/src/thread.rs:2356-2361`）。
- 子代理线程以 `parent_id` 落库（取自 `thread.subagent_context`，`agent/src/db.rs:518-521`），但不出现在会话列表（`agent/src/thread_store.rs:118-121`）。

---

## 10. 明确不存在的能力（不要从参考实现「推演」出来）

| 能力 | 结论 |
|---|---|
| 五档权限模式（default/acceptEdits/plan/bypassPermissions/dontAsk/auto） | 不存在，见 §4.6 |
| `isReadOnly` / `isConcurrencySafe` 分组、写工具串行 | 不存在，全部工具并发（§3.1） |
| maxTurns / 最大请求轮数 | 原生循环不存在（§2.3） |
| microCompact / sessionMemoryCompact | 不存在（§6.2） |
| 追加式 transcript（append-only log） | 不存在，是全量快照覆盖（§8.1） |
| plan 模式工具（EnterPlanMode / ExitPlanMode） | 不在工具清单内（`agent/src/tools.rs:199-224`） |
| Hook 生命周期（PreToolUse / PostToolUse …） | 本文核查的 `agent` crate 内没有对应实现；会话侧只有 `SettingsStore` 变更订阅用于重算授权（`agent/src/thread.rs:6652-6657`） |
| LSP 专用工具名 `LSP` | 拆成 `find_references / get_code_actions / apply_code_action / go_to_definition / rename_symbol`，且受 `LspToolFeatureFlag` 控制（`agent/src/tools.rs:233-243`） |

---

## 11. 对 b-desktop 的映射建议（基于以上事实）

1. **循环判据照搬**：Go 里用「本轮是否产生了 tool_result」作为唯一续轮信号，忽略 provider 的 stop reason；不要引入 maxTurns（参考实现没有，且用户可在 UI 里取消）。
2. **tool_result 兜底必须有**：每次把 pending 消息落盘时遍历所有 tool_use，缺结果的补 `is_error` + cancel 文案；Tool 接口用 `Result[Output, Output]` 的等价物（错误也走同一结构体），避免 `error` 逃出循环。
3. **权限下沉到工具**：Tool 契约里提供 `Authorize(ctx)` 回调（对应 `ToolCallEventStream::authorize`），由工具在执行副作用前调用；集中式 `checkPermissions` 可以做成一层的默认实现，但必须允许工具覆盖（symlink、敏感路径、脏 buffer、沙箱升级这些都需要工具自己的上下文）。
4. **决策顺序固定**：hardcoded deny → always_deny → always_confirm → always_allow → tool default → global default；allow 的「全部匹配」与 deny 的「任一匹配」语义要一致，否则可以绕过。
5. **压缩只在请求前做一次**，阈值用「最后一次 usage」而不是累计；保留策略至少包含「摘要 + 压缩前的最近用户消息（字节预算）+ 压缩后的全部」，并让自动压缩的插入点避开刚发来的用户消息。
6. **事件粒度对齐**：文本/thinking 用增量字符串；工具用「创建 + 字段 patch」而不是每次全量重发；UI 侧再做 16ms 合帧。Wails 的 `runtime.EventsEmit` 天然是 JSON，建议把工具 patch 定义成可空字段结构。

---

## 12. 交叉验证说明

- 本文没有修改参考目录或 `b-desktop` 的任何源码，只新增本文件。
- 未运行 Zed 的测试（`cargo test` 未执行）；所有结论来自静态阅读上述文件。
- 「不存在」类结论通过在 `agent/src`、`agent_settings/src`、`acp_thread/src` 内 grep 关键词得到（`bypass_permissions|permission_mode|accept_edits|dont_ask`、`micro.compact`、`is_read_only|concurren`），属于「在本次搜索范围内未找到」，不等于全仓绝对不存在。
