# OpenAI Codex 参考实现分析（codex-rs）

> 阅读对象：`/Users/baike3/CodeRepo/codex/codex-rs`（只读）。本文所有结论均标注 `file:line`；本次未阅读或无法确认的内容明确写「不存在」或「未确认」。
> 目的：为 anc-dsk（Go + Wails）实现 agent 运行时提供可落地依据。

## 0. 阅读范围与事实基线

本次实际打开并逐段读过的文件：

| 主题 | 文件 |
|---|---|
| 回合入口 / 任务生命周期 | `core/src/tasks/regular.rs`、`core/src/tasks/mod.rs` |
| 主循环 | `core/src/session/turn.rs`（3409 行） |
| 工具注册 / 调度 | `core/src/tools/registry.rs`、`core/src/tools/router.rs`、`core/src/tools/parallel.rs`、`core/src/tools/orchestrator.rs`、`core/src/tools/spec_plan.rs`、`tools/src/tool_executor.rs` |
| 审批 / 沙箱 | `core/src/tools/approvals.rs`、`core/src/tools/sandboxing.rs`、`core/src/safety.rs`、`core/src/unified_exec/mod.rs` |
| 上下文 / 压缩 | `core/src/context_manager/history.rs`、`core/src/context_manager/normalize.rs`、`core/src/session/context_window.rs`、`core/src/compact.rs`、`core/src/compact_token_budget.rs`、`core/src/session/async_compaction.rs`、`core/src/session/token_budget.rs` |
| 会话持久化 / 恢复 | `rollout/src/recorder.rs`、`rollout/src/policy.rs`、`rollout/src/rollout_file_name.rs`、`history/src/lib.rs`、`core/src/session/rollout_reconstruction.rs`、`core/src/session/mod.rs`、`thread-store/src/live_thread.rs` |
| 事件协议 / 流式 | `protocol/src/protocol.rs`、`codex-api/src/common.rs`、`app-server-protocol/src/protocol/event_mapping.rs`、`core/src/stream_events_utils.rs` |

一个重要的版本事实：这个 Codex 不是「Read/Edit/Glob/Grep 文件工具集」的形态，而是**以 `exec_command` + `write_stdin` + `apply_patch` 为核心**（`core/src/tools/handlers/unified_exec/exec_command.rs:108-110`、`core/src/tools/handlers/unified_exec/write_stdin.rs:38-40`、`core/src/tools/handlers/apply_patch.rs:285-287`）。差异清单见第 7 节。

---

## 1. 主回合循环：靠什么推进，什么时候退出

### 1.1 三层结构

```text
RegularTask::run                  core/src/tasks/regular.rs:37-50
  └─ loop                         core/src/tasks/regular.rs:104
      └─ run_turn                 core/src/session/turn.rs:173
          └─ loop                 core/src/session/turn.rs:461        ← 真正的回合循环
              └─ run_sampling_request   core/src/session/turn.rs:1714
                  └─ loop（重试）        core/src/session/turn.rs:1750
                      └─ try_run_sampling_request  core/src/session/turn.rs:2628
                          └─ loop（读流）    core/src/session/turn.rs:2775
```

- `RegularTask` 在启动时先发 `TurnStarted`，再解析启动预热（`core/src/tasks/regular.rs:52-90`），这样首轮生命周期不必等待 MCP/预热完成。
- `RegularTask` 的外层 `loop`（`core/src/tasks/regular.rs:104`）只在 **input queue 仍有待处理输入** 时重启 `run_turn`；否则返回最后一条 assistant 文本（`core/src/tasks/regular.rs:118-122`）。重启时 `next_input` 置空（`core/src/tasks/regular.rs:123`），说明排队的用户输入已经落进历史，不需要重复投喂。
- 每次 `run_turn` 返回的都是 `Option<String>`（最后一条 agent 消息），不是整段会话（`core/src/session/turn.rs:180`）。
- `SamplingRequestResult` 只有两个字段（`core/src/session/turn.rs:1981-1984`）：

  ```rust
  struct SamplingRequestResult { needs_follow_up: bool, last_agent_message: Option<String> }
  ```

### 1.2 推进信号：`needs_follow_up`

循环唯一的正常推进信号是 `SamplingRequestResult.needs_follow_up`（结构体在 `core/src/session/turn.rs:1981-1984`）。

它来自三处：

1. **模型返回了工具调用**：`core/src/stream_events_utils.rs:324-370`，`ToolRouter::build_tool_call` 返回 `Some(call)` 时 `output.needs_follow_up = true`（`core/src/stream_events_utils.rs:368`）。
2. **服务端 `end_turn == false`**：`ResponseEvent::Completed` 里 `if let Some(false) = end_turn { needs_follow_up = true; }`（`core/src/session/turn.rs:3132-3183`，判断在 3177-3179）。注意 `end_turn` 是 `Option<bool>`，部分 provider 不返回，此时不据此推进（`codex-api/src/common.rs:100-107`）。
3. **用户中途插话**：pending input 被计入 `let needs_follow_up = model_needs_follow_up || has_pending_input;`（`core/src/session/turn.rs:610`）。

`needs_follow_up` 还会在流被 preempt（用户新输入触发）时被置 true：`core/src/session/turn.rs:1832-1839`。

### 1.3 退出条件

`core/src/session/turn.rs:461` 的 `loop` 只在这些情况下退出：

| 条件 | 位置 | 结果 |
|---|---|---|
| `!needs_follow_up`，Stop hook 未 block/stop | `core/src/session/turn.rs:724-831` | `break`，返回最后 agent 消息（`core/src/session/turn.rs:912`） |
| Stop hook `should_stop` | `core/src/session/turn.rs:771-773` | `break` |
| Stop hook `should_block` 且带 continuation prompt | `core/src/session/turn.rs:743-760` | 记录 hook 消息后 `continue`，再采样一轮 |
| Stop hook 对 MemoryConsolidation 会话拒绝 | `core/src/session/turn.rs:733-742` | 返回 `InvalidRequest` |
| 取消 | `core/src/session/turn.rs:866-868` | 返回 `Err(TurnAborted)` |
| InvalidImageRequest | `core/src/session/turn.rs:869-891` | 发 `Error` 事件后 `break` |
| 其它错误 | `core/src/session/turn.rs:892-908` | 发 `Error` 事件后 `break`（注释明确「let the user continue the conversation」） |
| 压缩后需要重开一步 | `core/src/session/turn.rs:682-705`（`should_roll_over` 分支内） | `continue` |

**没有最大轮次上限。** 在 `core/src` 与 `protocol/src` 中检索 `max_turns` 无命中；`core/src/session/turn.rs`、`core/src/tasks/regular.rs` 也无迭代计数。循环只由「模型不再要工具 + 无新输入」或错误/取消终止。这一点与 anc-dsk AGENTS.md 里「受 maxTurns 约束」的假设不一致。

### 1.4 用户中途输入（steering）

- 每次采样前，如果允许排空 pending input，就把并发的用户输入取出（`core/src/session/turn.rs:465-472`），经 `run_hooks_and_record_inputs(..., PersistContext::SteeredUserInput)` 落盘（`core/src/session/turn.rs:474-481`）。
- 采样进行中，`watch_user_input` 监视待处理输入，一有用户输入就 cancel 掉当前的 preempt token（`core/src/session/input_queue.rs:293-313`），`try_run_sampling_request` 收到后以 `needs_follow_up: true` 返回（`core/src/session/turn.rs:1832-1839`），下一轮请求带上新输入。
- 注释说明了两处刻意延迟：回合开始时先采样新输入；自动压缩后若仍有工具续跑，则暂不排空（`core/src/session/turn.rs:453-459`）。

### 1.5 SubAgent 是独立 thread

- 子 Agent 通过 `StartThreadOptions` 创建新 thread，`thread_source = Subagent`（`core/src/agent/control/spawn.rs:818-831`）。
- 需要继承父上下文时，`prepare_forked_history` 产出的初始历史写入 `start_options.initial_history`（`core/src/agent/control/spawn.rs:832-847`）；不 fork 时保持默认并直接 `spawn_child_thread` 创建新 thread（`core/src/agent/control/spawn.rs:868-877`）。
- 因此子 Agent 拥有自己的会话、历史与 rollout 文件；主循环不共享消息历史。

---

## 2. 工具注册与调度

### 2.1 工具契约

最小契约在 `tools/src/tool_executor.rs:106-130`：

```rust
pub trait ToolExecutor<Invocation>: Send + Sync {
    fn tool_name(&self) -> ToolName;
    fn spec(&self) -> ToolSpec;
    fn exposure(&self) -> ToolExposure { ToolExposure::Direct }
    fn search_info(&self) -> Option<ToolSearchInfo>;
    fn supports_parallel_tool_calls(&self) -> bool { false }   // 默认串行
    fn handle<'a>(&'a self, invocation: Invocation) -> ToolExecutorFuture<'a>;
}
```

- 默认 `supports_parallel_tool_calls = false`（`tools/src/tool_executor.rs:122-124`），即**默认串行，显式声明才并行**。
- `core` 侧再包一层 `CoreToolRuntime`，补充生命周期通知、spec 缓存、取消语义（`core/src/tools/registry.rs:59-105`）。
- 工具契约里**不存在** `isReadOnly` / `isConcurrencySafe` 这组字段；并发能力只有一个布尔位。

### 2.2 注册与对模型暴露

- 每个 step（一次采样请求视图）在 `build_tool_router` 里新建 registry（`core/src/tools/spec_plan.rs:121-180`），依次加入：核心工具（shell/MCP 资源/工具/协作，`core/src/tools/spec_plan.rs:1177-1197`）、MCP 工具（`core/src/tools/spec_plan.rs:151-158`）、扩展工具、动态工具（`core/src/tools/spec_plan.rs:166-172`）。
- 暴露面是 6 档枚举：`Direct` / `Deferred` / `DeferredModelOnly` / `DirectModelOnly` / `CodeModeOnly` / `Hidden`（`tools/src/tool_executor.rs:51-85`）。
- `Deferred` 工具不进初始工具列表，只能通过 `tool_search` 发现（`tools/src/tool_executor.rs:56-68`）。
- registry 用 `IndexMap` 保序并在注册时检测命名冲突（`core/src/tools/registry.rs:330-334`、`417-470`）。

实际的内置工具（从 handler 的 `tool_name()` 读到的）：`exec_command`（`core/src/tools/handlers/unified_exec/exec_command.rs:108`）、`write_stdin`（`core/src/tools/handlers/unified_exec/write_stdin.rs:38`）、`apply_patch`（`core/src/tools/handlers/apply_patch.rs:285`）、`update_plan`（`core/src/tools/handlers/plan.rs:49`）、`view_image`（`core/src/tools/handlers/view_image.rs:73`）、`spawn_agent`（`core/src/tools/handlers/multi_agents_v2/spawn.rs:37`）。其余见 `core/src/tools/handlers/` 目录清单。

### 2.3 并发调度：一把读写锁

不是「按只读/写分组」，而是：

```rust
if supports_parallel { lock.read().await } else { lock.write().await }   // core/src/tools/parallel.rs:213-217
```

- 锁是每个采样请求一份的 `Arc<RwLock<()>>`（`core/src/tools/parallel.rs:44-52`、`65-70`）。
- 并行的工具共享读锁，可同时跑；声明为串行的工具拿写锁，独占执行（`core/src/tools/parallel.rs:213-217`）。
- 例：`exec_command` 声明 `true`（`core/src/tools/handlers/unified_exec/exec_command.rs:134-136`），`write_stdin` 也声明 `true`（`core/src/tools/handlers/unified_exec/write_stdin.rs:46`）。
- 这不是「写工具一律串行」：`apply_patch` 未重写该方法，走默认 false，所以串行；但 shell 命令即使会写文件也被声明为可并行——写冲突不由调度器统一串行化，安全性取决于工具自身的声明与沙箱。

### 2.4 分发时序：先立即 spawn，流结束后按序回收

这是最值得照搬的细节之一：

1. 收到 `OutputItemDone` 且解析出工具调用时，**先把调用项本身写入历史并落盘**（`core/src/stream_events_utils.rs:324-348`，`record_completed_response_item` 定义在 `core/src/stream_events_utils.rs:79-105`）。
2. 随后立即构造 tool future。`handle_tool_call` 内部在创建 future 时就 `tokio::spawn` 执行（`core/src/tools/parallel.rs:202`），注释明确：「handle_tool_call dispatches immediately, not when its future is polled」（`core/src/stream_events_utils.rs:352-353`）。
3. future 被塞进 `FuturesOrdered`（`core/src/session/turn.rs:2968`），**保持调用顺序**。
4. 响应流结束后统一 `drain_in_flight`（`core/src/session/turn.rs:3359-3368`），逐个 await 并 `record_annotated_conversation_items` 写入历史（`core/src/session/turn.rs:2575-2599`）。

因此：工具在模型还在吐剩余 item 时就已经开跑；但**工具结果只在整条响应流结束后才按顺序写入历史**。这保证了历史里 call 与 output 的相对顺序稳定。

### 2.5 `tool_use` 与 `tool_result` 如何配对

四层保障：

| 层 | 机制 | 证据 |
|---|---|---|
| 生成 | 结果封装为 `FunctionCallOutput/CustomToolCallOutput/ToolSearchOutput`，携带原 `call_id` | `core/src/tools/registry.rs:215-230`（`AnyToolResult::into_response`） |
| 失败 | 非致命错误也转成一条 output，`success: Some(false)`，不向上抛 | `core/src/tools/parallel.rs:110-112`（调用）、`338-360`（`failure_response`） |
| 取消 | 生成 `aborted_response`（"aborted by user..."） | `core/src/tools/parallel.rs:365-386` |
| 历史归一 | 发送前扫描：缺失的 call 补一条 `output = "aborted"` 的合成 output；孤儿 output 直接删除 | `core/src/context_manager/normalize.rs:21-157`（补齐）、`171-245`（删孤儿）、调用点 `core/src/context_manager/history.rs:968-982` |

例外：`FunctionCallError::Fatal` 不生成 output，而是让整个 tool future 返回 `Err`（`core/src/tools/parallel.rs:109`）；drain 时调用 `error_or_panic`，该函数在 debug 构建下直接 panic、release 下只记 error 日志（`core/src/util.rs:81-87`、`core/src/session/turn.rs:2593-2597`）。这条调用会暂时「有 call 无 output」，靠下一次 prompt 归一化补一条 `"aborted"`（`core/src/context_manager/normalize.rs:53-69`）。

注意：合成 output 的 ID 是确定性的（UUID v5，命名空间固定），注释说明这是为了不破坏 prompt cache（`core/src/context_manager/normalize.rs:159-170`）。

另有本地错误处理：`in_flight` 里某个 future join 失败会进入同一条 `error_or_panic` 路径——release 下记日志并继续处理其它结果，debug 下 panic（`core/src/util.rs:81-87`、`core/src/session/turn.rs:2593-2597`）。

---

## 3. 权限检查与用户审批

### 3.1 策略枚举

- 审批策略：`UnlessTrusted` / `OnRequest`（默认）/ `Granular(GranularApprovalConfig)` / `Never`（`protocol/src/protocol.rs:1097-1120`）。
- 沙箱档位是另一维：`ReadOnly` / `WorkspaceWrite` / `DangerFullAccess`（`protocol/src/config_types.rs:100-115`）。
- 权限剖面：`Managed { file_system, network }` / `Disabled` / `External { network }`（`protocol/src/models.rs:426-440`）。
- `protocol` 与 `core` 中**不存在**桌面端那种五档 `default/acceptEdits/plan/bypassPermissions/dontAsk` 枚举；对应能力由「审批策略 + 沙箱档 + profile」组合表达。`ModeKind::Plan` 只控制提示/工具面（`core/src/session/turn.rs:2768`），不参与权限判定。

### 3.2 决策时刻：工具执行之前，在 orchestrator 里

所有会触碰文件系统/进程的工具都走 `ToolOrchestrator::run`，顺序固定为：

```text
1) Approval（approvals）
2) 第一次沙箱尝试
3) 若判定为沙箱拒绝且策略允许 → 再次审批（retry_reason）→ 无沙箱重试
```

证据：`core/src/tools/orchestrator.rs:1-2`（模块注释）与 `core/src/tools/orchestrator.rs:136-236`（第 1 步）、`core/src/tools/orchestrator.rs:380-470`（第 3 步）。调用点：`apply_patch` 在 `core/src/tools/handlers/apply_patch.rs:554`，`exec_command` 在 `core/src/unified_exec/process_manager.rs:1493`；总览注释在 `core/src/unified_exec/mod.rs:6-16`。

审批需求的来源：

- 工具可自定义 `exec_approval_requirement`，否则走默认策略（`core/src/tools/orchestrator.rs:160-163`）。
- 默认规则：`Never` → 不需要；`OnRequest`/`Granular` → 仅当文件系统沙箱是 `Restricted` 时才需要；`UnlessTrusted` → 总是需要（`core/src/tools/sandboxing.rs:192-230`）。
- `Granular` 且关闭 `sandbox_approval` → 直接 `Forbidden`，不弹窗（`core/src/tools/sandboxing.rs:210-217`）。

### 3.3 审批优先级：Hook → Guardian（自动审查）→ 用户

`request_approval` 的注释直接写明优先级（`core/src/tools/approvals.rs:492-496`）：

1. `PermissionRequest` hook：Allow/Deny（`core/src/tools/approvals.rs:497-515`）。
2. 若启用 Guardian/StrictAutoReview，则 Guardian 模型自动裁决（`core/src/tools/approvals.rs:552-566`）。
3. 否则请求用户（`core/src/tools/approvals.rs:676-836`）。

### 3.4 用户审批的落点

- 生成 `EventMsg::ExecApprovalRequest` 或 `EventMsg::ApplyPatchApprovalRequest` 事件（`core/src/session/mod.rs:3010-3027`、`:3064-3071`）。
- 先用 `oneshot` 把回调登记到当前 turn 的 pending 表，**再**发事件，避免竞态（`core/src/session/mod.rs:2964-2975`）。
- 然后阻塞等待 `rx_approve.await`，通道断开时默认 `Abort`（`core/src/session/mod.rs:3031`、patch 同类逻辑在 `core/src/session/mod.rs:3073`）。也就是说：**没有任何默认放行**。
- 用户侧决策通过 `notify_approval(approval_id, decision)` 回填（`core/src/session/mod.rs:3476-3495`）。
- 决策类型包含：`Approved` / `ApprovedExecpolicyAmendment` / `ApprovedForSession` / `Denied` / `TimedOut` / `Abort` 等（`protocol/src/protocol.rs:4269-4304`）。
- 会话级缓存：同一 cache key 全部 `ApprovedForSession` 时跳过追问；本次批准则把每个 key 单独写入缓存（`core/src/tools/sandboxing.rs:72-118`）。

### 3.5 沙箱失败后的二次审批

- 只有当错误是 `SandboxErr::Denied` 且工具声明 `escalate_on_failure()` 时才考虑重试（`core/src/tools/orchestrator.rs:396-425`）。
- `Never`/`OnRequest` 默认不再无沙箱重试（`core/src/tools/orchestrator.rs:427-470`）。
- 重试前，如果上一次审批来自 User/Hook，则免二次审批；Guardian 的裁决只覆盖被审查的那一次权限（`core/src/tools/orchestrator.rs:481-491`）。
- 需要有 denied-read 限制时，不允许简单去掉沙箱，改走 `EscalatedSandboxWithRestrictions`（`core/src/tools/sandboxing.rs:234-245`、`249-295`）。

### 3.6 文件写入的例外：沙箱内路径自动放行

`assess_patch_safety`：`apply_patch` 若所有目标路径都在可写根内且沙箱可用，则 `AutoApprove`；否则 `AskUser` 或 `Reject`（`core/src/safety.rs:67-123`）。`AutoApprove` 被翻译成 `ExecApprovalRequirement::Skip { bypass_sandbox: false }`——**免的是审批，不是沙箱**（`core/src/apply_patch.rs:21-42`）。代码注释指出补丁路径存在硬链接逃逸风险，因此即便自动放行也仍在沙箱内执行（`core/src/safety.rs:96-100`）。

---

## 4. 上下文与压缩

### 4.1 历史写入：入库即截断，不是事后微压缩

- `ContextManager::record_items` 逐条写入（`core/src/context_manager/history.rs:507-518`）。
- 工具输出在写入历史时就按模型的 `truncation_policy` 截断：`truncate_function_output_payload(output, policy, ...)`（`core/src/context_manager/history.rs:576`）。注释明确「Tool output truncation applies only to live history, preserving full rollout payloads」（`core/src/context_manager/history.rs:511-513`）。
- `TruncationPolicy` 只有 `Bytes(usize)` 和 `Tokens(usize)` 两种（`protocol/src/protocol.rs:3509-3512`）。
- **不存在 microCompact / 微压缩路径**：在 `core/src` 与 `protocol/src` 检索 `microcompact|micro_compact|micro compact` 无命中。轻量减负靠上面的逐条截断。

### 4.2 何时检查：每个回合内的 4 个检查点

| 检查点 | 位置 | 条件 |
|---|---|---|
| 回合开始前（PreTurn） | `core/src/session/turn.rs:196-232` → `run_pre_sampling_compact`（`core/src/session/turn.rs:1379-1405`） | `token_limit_reached` 或已有后台压缩结果 |
| 每次采样前 | 上面这一步在 `run_turn` 头部只做一次；循环内每步前只做 world-state/时间注入 | `core/src/session/turn.rs:534-560` |
| 每次采样后（MidTurn / PostTurn） | `core/src/session/turn.rs:580-690` | `needs_follow_up` 且（新窗口请求 / 触限 / 后台候选就绪） |
| 回合正常结束前 | `core/src/session/turn.rs:787-830` | `model_post_turn_compact_threshold_percent > 0` 且达到阈值 |

另外还有：
- 模型切换/压缩兼容哈希变化时的 PreTurn 压缩（`core/src/session/turn.rs:1423-1505`，`comp_hash_changed` 在 1413-1421）。
- 上下文超限错误（`ContextWindowExceeded`）的一次性补救压缩（`core/src/session/turn.rs:835-865`，带 `guardian_budget_compacted` 防循环）。
- 后台投机压缩：`core/src/session/async_compaction.rs`，需要 `Feature::AsyncCompaction` 且 provider 支持 V2 远程压缩（`core/src/session/async_compaction.rs:40-50`），阈值由 `async_compaction_threshold_percent` 控制（`core/src/session/async_compaction.rs:77`）。

### 4.3 阈值怎么算

`context_window_token_status`（`core/src/session/context_window.rs:26-130`）：

- 用量来自 `sess.get_total_token_usage()`（`core/src/session/context_window.rs:60`；计算方式见 `core/src/context_manager/history.rs:939-956`：上次服务端 usage + 最后一条模型消息之后的本地项估算）。
- 自动压缩作用域二选一：`Total`（全部活跃上下文）或 `BodyAfterPrefix`（减去窗口 prefill 基线），`core/src/session/context_window.rs:62-82`。
- 硬上限 = 模型 context window × `effective_context_window_percent / 100`（`core/src/session/context_window.rs:84-88`）。
- `token_limit_reached` = 作用域用量 ≥ (limit + fallback buffer) 或达到硬上限（`core/src/session/context_window.rs:102-110`）。

### 4.4 压缩实现的三条路径

`run_auto_compact` 分派（`core/src/session/turn.rs:1514-1618`）：

1. **TokenBudget 特性**：不做摘要，直接开新窗口（`core/src/compact_token_budget.rs:42-69`：`sess.start_new_context_window(...)`）。
2. **Provider 支持 remote compaction V2**：`compact_remote_v2::run_inline_remote_auto_compact_task`（`core/src/session/turn.rs:1560-1598`）。
3. **否则本地摘要压缩**：`run_inline_auto_compact_task`（`core/src/compact.rs:117-143`）。

本地摘要压缩的做法：

- 套 `PreCompact` hook，被停止则 `TurnAborted`；成功后再跑 `PostCompact` hook（`core/src/compact.rs:197-212`、`224-236`）。
- 用一次性 prompt（默认 `SUMMARIZATION_PROMPT`，可被 `config.compact_prompt` 覆盖）对**当前完整历史**再请求一次模型（`core/src/compact.rs:125-143`、`263-300`）。
- 压缩请求本身遇到 `ContextWindowExceeded` 时，从最旧项开始逐条删除后重试（`core/src/compact.rs:358-370`），保留前缀缓存与最近消息。
- 摘要以「另一个模型写的总结」前缀呈现（`prompts/templates/compact/summary_prefix.md:1`），并通过 `SUMMARY_PREFIX` 识别（`core/src/compact.rs:617-619`）。

### 4.5 压缩后保留什么

`build_compacted_history`（`core/src/compact.rs:697-710`）的替换历史只含三部分：

1. **最近的真实用户消息**，从新到旧回填，总预算 `COMPACT_USER_MESSAGE_MAX_TOKENS = 20_000`（`core/src/compact.rs:63`、`716-800`）；单条超预算会被截断文本、丢弃图片/音频，并标记 `mark_retained_sources_incomplete`（`core/src/compact.rs:785-788`）。
2. **一条压缩摘要**，作为 `CompactionSummary` 用户片段追加在末尾（`core/src/compact.rs:803-812`）。
3. **规范初始上下文**（系统/开发者指令等）在最后被插回，插入点优先在「最后一条真实用户/agent 消息或保留工具输出之前」，否则放在摘要之前（`core/src/compact.rs:646-695`、`build_compaction_replacement_history` 在 `84-99`）。

变量是 `feature token_budget` 时替换历史为空、直接换窗口（第 4.4 节第 1 条）。

### 4.6 压缩边界的持久化

`replace_compacted_history`（`core/src/session/mod.rs:4242-4426`）会：

- 把替换历史整体写入内存历史（`history.replace_compacted`，`core/src/context_manager/history.rs:721-758`）。
- 生成 `CompactedItem`，其中 `replacement_history: Some(...)`、`window_number/window_id`、`compaction_response_id`、`latest_token_usage_record`、`resume_metadata`（`core/src/session/mod.rs:4377-4405`；结构定义在 `history/src/lib.rs:290-310`）。
- 连续写入 4 条 rollout：`Compacted`、`WorldState(full)`、`TurnContext`、`ThreadSettingsApplied`（`core/src/session/mod.rs:4406-4424`）。

---

## 5. 会话持久化与恢复

### 5.1 存储形态：每会话一个追加式 JSONL rollout

- 路径：`$CODEX_HOME/sessions/YYYY/MM/DD/rollout-<timestamp>-<thread_id>.jsonl`；revert 后文件名追加 `_<rollout_id>`（`rollout/src/recorder.rs:1755-1777`、`rollout/src/rollout_file_name.rs:52-71`）。
- 每行一个 `RolloutLine`：`timestamp` + 可选 `ordinal` + flatten 的 `RolloutItem`（`history/src/lib.rs:369-376`）。
- `RolloutItem` 共 12 种：`SessionMeta` / `ResponseItem` / `InterAgentCommunication` / `InterAgentCommunicationMetadata` / `Compacted` / `TurnContext` / `TokenUsageRecord` / `WorldState` / `SecurityRiskScore` / `RetainedContext` / `EventMsg` / `RealtimeItem`（`history/src/lib.rs:215-231`）。
- 不是「每会话一个 transcript 只记消息」：**协议事件本身也被持久化**，见 5.2。

### 5.2 事件即记录

`send_event_raw_with_persistence` 在把事件投递给前端之前，先把它包成 `RolloutItem::EventMsg` 持久化（`core/src/session/mod.rs:2703-2707`）。这带来两个直接结果：

- `TurnStarted` / `TurnComplete` / `TurnAborted` / `ThreadRolledBack` / `ThreadSettingsApplied` 都能在重放时读到（写入策略见 5.3）。
- 恢复时可用事件流重建「上一轮是否中断」「标题/设置何时变更」等语义（`core/src/session/mod.rs:1676-1710`、`core/src/session/rollout_reconstruction.rs:270-350`）。

### 5.3 写入策略：先过滤、再截断、异步落盘

- 过滤：`persisted_rollout_item` 决定哪些项落盘（`rollout/src/policy.rs:19-55`；事件过滤入口 `persisted_event_msg` 在 `rollout/src/policy.rs:127`）。`ResponseItem::CompactionTrigger` 与 `Other` 不落（`rollout/src/policy.rs:93-113`）。
- 截断：MCP 结果单条最大 64KB；命令输出最大 64KB 并插入截断标记（`rollout/src/policy.rs:16-18`、`254-271`）。
- 写入：`RolloutRecorder` 用有界 channel（256）+ 后台 tokio 任务写文件（`rollout/src/recorder.rs:1013-1035`、`1978-2020`）；每行写完即 `flush`（`rollout/src/recorder.rs:2141-2146`）。
- 新会话文件**延迟创建**（`deferred_creation`），由 `PersistContext` 决定何时物化（`rollout/src/recorder.rs:985-990`、`thread-store/src/store.rs:102-126`）。
- 会话初始化时 `flush_rollout`，回合结束时再 flush（`core/src/tasks/mod.rs:402`、`core/src/session/mod.rs:1729-1731`）。

### 5.4 恢复：从最新压缩边界 + 后缀重放

入口：`InitialHistory::Resumed` → `LiveThread::resume`（`core/src/session/session.rs:1116-1153`）→ `apply_rollout_reconstruction`（`core/src/session/mod.rs:1817-1900`）。

重建逻辑在 `ContextManager::reconstruct_rollout`（`core/src/session/rollout_reconstruction.rs:208-620`）：

1. **只选最新一个可用压缩边界**（`select_input_compaction`，`core/src/session/rollout_reconstruction.rs:64-91`）。若最新压缩缺 `replacement_history` 或 `window_number`（旧格式），则不使用它。
2. 反序扫描后缀，重建每个 turn 的段信息：`TurnStarted` / `TurnComplete` / `TurnAborted` / `TurnContext` / `UserMessage`，并处理 `ThreadRolledBack` 的回滚轮数（`core/src/session/rollout_reconstruction.rs:270-450`）。
3. 先安装压缩的 `replacement_history`（`core/src/session/rollout_reconstruction.rs:494-505`），再正序重放后缀的 `ResponseItem`、`InterAgentCommunication`、`RetainedContext`、`ThreadRolledBack`（`core/src/session/rollout_reconstruction.rs:506-560`）。
4. 重放 world-state 记录，支持 full 快照 + merge patch（`core/src/session/rollout_reconstruction.rs:567-605`）。
5. 恢复 `previous_turn_settings`、`turn_attribution`、`auto_compact_window`、token usage，并重新注入规范初始上下文（`core/src/session/mod.rs:1871-1899`、`core/src/session/mod.rs:1712-1719`）。

关键点：**恢复不是重跑整个会话，而是「最新压缩快照 + 其后增量」**，因此压缩边界必须自带完整替换历史，否则只能走 legacy 全量重放（`core/src/session/rollout_reconstruction.rs:520-545`）。

### 5.5 索引与检索

- SQLite state db 维护 thread 元数据（`rollout/src/state_db.rs:1-40`，`thread-store/src/live_thread.rs:229-280` 在追加时同步元数据）。
- `session_index.jsonl` 以追加方式记录线程名，最后一条生效（`rollout/src/session_index.rs:9-35`）。

---

## 6. 事件协议与流式输出

### 6.1 两条协议层

| 层 | 类型 | 位置 |
|---|---|---|
| 模型客户端 → core | `ResponseEvent` | `codex-api/src/common.rs:81-129` |
| core → 前端/宿主 | `EventMsg`（包在 `Event { id, msg }` 里） | `protocol/src/protocol.rs:1469-1689` |
| app-server（JSON-RPC/stdio） | `ServerNotification`，如 `item/agentMessage/delta` | `app-server-protocol/src/protocol/common.rs:1993-2005`、`app-server-protocol/src/protocol/event_mapping.rs:367-372` |

`Event.id` 是**提交（turn）id**（`protocol/src/protocol.rs:1451-1456`、`core/src/session/mod.rs:2481-2484`）。

### 6.2 流式事件的最小粒度

- 文本：**每个模型文本 delta 一个事件**。`ResponseEvent::OutputTextDelta(delta)` → `AgentMessageContentDeltaEvent { delta }`（`core/src/session/turn.rs:3195-3212`），字段定义 `protocol/src/protocol.rs:2083-2088`。plan 模式下改发 `PlanDelta`（`protocol/src/protocol.rs:2090-2096`）。
- 推理摘要：`ReasoningSummaryDelta` → `ReasoningContentDelta`，带 `summary_index`（`core/src/session/turn.rs:3233-3260`、`protocol/src/protocol.rs:2098-2107`）。
- 工具参数：`ToolCallInputDelta` 交给 `ToolArgumentDiffConsumer`，产出参数 diff 事件（`core/src/session/turn.rs:3215-3231`）。
- 命令输出：`ExecCommandOutputDelta` 携带原始字节块（base64），并标明 stdout/stderr（`protocol/src/protocol.rs:3783-3793`）。
- 生命周期：`ItemStarted` / `ItemCompleted` 包住一个 turn item（`protocol/src/protocol.rs:1656-1657`）。

因此最小可观测粒度是 **provider 返回的一个 delta**，不是 token、也不是 chunk 聚合后的结果；默认路径逐 delta 直发，不做聚合。

### 6.3 投递

- `Session` 持有 `mpsc::Sender<Event>`（`core/src/session/session.rs:60-64`），`deliver_event_raw` 直接 send，通道关闭只记 debug 日志（`core/src/session/mod.rs:2724-2731`）。
- 旧事件兼容：`as_legacy_events` 会把新事件再展开成一组 legacy 事件逐个发送（`core/src/session/mod.rs:2511-2521`）。

### 6.4 usage / 完成

- `ResponseEvent::Completed` 携带 `token_usage`、`usage_metadata`、`end_turn`（`codex-api/src/common.rs:100-108`）。
- core 收到后写 token 用量并决定是否 `needs_follow_up`（`core/src/session/turn.rs:3132-3183`）。
- 回合终态由 `on_task_finished` 统一发 `TurnComplete` 或 `TurnAborted`，包含 `started_at/completed_at/duration_ms`（`core/src/tasks/mod.rs:645-880`；事件结构 `protocol/src/protocol.rs:2273-2293`）。

---

## 7. 与 anc-dsk AGENTS.md 假设的差异

以下不是评价，是事实对照，直接影响 Go 端设计：

| AGENTS.md 假设 | codex-rs 实际 | 证据 |
|---|---|---|
| 循环受 `maxTurns` 约束 | 无轮次上限 | 全文检索 `max_turns` 无命中；`core/src/session/turn.rs:461` |
| 只读工具并行、写工具串行（`isReadOnly`/`isConcurrencySafe`） | 单一 `supports_parallel_tool_calls` + 读写锁；且 shell 命令默认并行 | `tools/src/tool_executor.rs:122-124`、`core/src/tools/parallel.rs:213-217` |
| 工具集含 `Read/Write/Edit/Glob/Grep/NotebookEdit/PowerShell` | 只有 `exec_command`/`write_stdin`/`apply_patch` 等 shell 中心工具 | `core/src/tools/handlers/` 目录；`core/src/tools/handlers/unified_exec/exec_command.rs:108`、`core/src/tools/handlers/apply_patch.rs:285` |
| 五档权限模式 | 审批策略 4 值 × 沙箱 3 档 × profile | `protocol/src/protocol.rs:1097-1120`、`protocol/src/config_types.rs:100-115` |
| 三种压缩路径含 `microCompact` | 无 microCompact；逐条截断 + PreTurn/MidTurn/PostTurn 摘要压缩 + 可选的 TokenBudget 换窗 | 检索无命中；`core/src/context_manager/history.rs:576`、`core/src/session/turn.rs:196-232`、`core/src/session/turn.rs:580-690`、`core/src/session/turn.rs:787-830` |
| 权限检查在工具执行之前 | 一致，但精确顺序是：PreToolUse hook → 工具 handler → orchestrator 审批 → 沙箱执行 | `core/src/tools/registry.rs:701-757`（hook）、`core/src/tools/orchestrator.rs:136-236` |
| 会话 transcript 记录消息 + 压缩边界 | 记录消息**和协议事件**、world state、token usage、turn context | `history/src/lib.rs:215-231` |

---

## 8. 对 Go 实现的可落地建议

1. **循环用「结果结构体」而不是异常退出**：`SamplingResult{NeedsFollowUp, LastAgentMessage}`；只有 `NeedsFollowUp` 为 false 才结束，工具调用/`end_turn=false`/有新输入三者任一为 true 就继续。不要引入隐式轮次上限；要限流就用 `context` + 取消。
2. **工具执行与结果入库分离**：工具拿到调用即 `go` 启动，结果放进按调用顺序索引的 slice/channel；等 HTTP 流结束后按序写入历史并持久化。用 `errgroup` 或 `sync.WaitGroup` 管理，慎用无序并发写历史。
3. **并发控制用「读写锁 + 能力位」**：`Tool.SupportsParallel() bool`，默认 false；并行工具 `RLock`，串行工具 `Lock`。Go 里直接用 `sync.RWMutex`，不要自造分组器。
4. **审批做成阻塞调用 + 事件推送**：`RequestApproval(ctx, action) (Decision, error)` 内部注册 `map[approvalID]chan Decision`，先注册再 `EventsEmit`，`select` 等待 ctx 或决策；channel 关闭/ctx 取消一律按 `Abort` 处理，禁止默认放行。
5. **压缩要产出「可独立重放的快照」**：压缩结果 = 初始上下文 + 最近用户消息（带 token 预算）+ 摘要，整体写入一条 checkpoint 记录（含完整 replacement history），恢复时只重放 checkpoint 之后的后缀。只存摘要文本会在恢复时丢失工具结果与上下文结构。
6. **事件粒度就是 provider delta**，每个 text/thinking delta 直接 `EventsEmit`；工具输出按字节块转发；不要聚合成整段再发。前端做节流是前端的事。

---

## 9. 未确认 / 不存在

"不存在" 的检索依据（本次实际执行，输出均为 0 行）：

```bash
cd /Users/baike3/CodeRepo/codex/codex-rs
grep -rn "max_turns" --include="*.rs" core/src protocol/src                                           # 0
grep -rni "microcompact\|micro_compact\|micro compact" --include="*.rs" core/src protocol/src         # 0
grep -rn "is_read_only\|isReadOnly\|is_concurrency_safe\|isConcurrencySafe" --include="*.rs" core/src tools/src protocol/src  # 0
```

- 「桌面端五档权限模式的映射」：`protocol`/`core` 中没有该枚举；本次未阅读 `tui/`，无法确认 UI 层是否存在映射，**未确认**。
- `microCompact`、`sessionMemoryCompact`：在 `core/src` 与 `protocol/src` 中**不存在**。
- `maxTurns`：在 `core/src` 与 `protocol/src` 中**不存在**。
- `isReadOnly` / `isConcurrencySafe`：在 `core/src` 与 `core/src/tools/` 中**不存在**。
- Go 端是否需要跨平台沙箱（Landlock/Seatbelt/Windows）：本仓有 `core/src/sandboxing/`、`linux-sandbox/`、`windows-sandbox-rs/` 等实现，但本次未逐文件阅读其系统调用细节，**未确认**。
