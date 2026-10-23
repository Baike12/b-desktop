# ZCode Agent 运行时调研笔记

> 参考目录：`../ZCode`（只读）。下文所有路径均相对该目录。
> 结论只来自实际读到的代码与文档；没有发现实现的地方明确标注「不存在 / 未找到执行入口」。

## 0. 事实基线与边界

- 权威实现不是单进程：`UI → Host → workspace CLI`。CLI 持有 conversation 真相，UI 只消费 snapshot/delta（`docs/architecture/zcode-code-architecture-overview.md:84-98`、`docs/architecture/message-flow.md:6-30`）。
- Agent 核心在 `apps/zcode-cli/packages/core/src`；V4 产品投影在 `apps/zcode-cli/packages/bootstrap/src/zcode-protocol-v4`；行模型在 `packages/shared/src/zcode-protocol-v4`；UI 在 `packages/ui/src/v4`。
- 模型请求走 AI SDK 适配层（`apps/zcode-cli/packages/adapters/src/model/transform.ts`），内部统一用 `ModelInputMessage { role, content, toolCalls, toolCallId }`（`apps/zcode-cli/packages/core/src/agent/message-history.ts:18-35`）。

---

## 1. 主循环：靠什么推进，什么时候退出

### 1.1 结构

```text
executeTurnCommand (turn.ts)
  └─ runRegularTurnLoop            while(true) 每轮 = 一次模型请求 + 一批工具
       ├─ microcompact → autoCompact   每次模型请求前
       ├─ 组装 tools / reminders / provider messages
       ├─ runModelBackedTurnStep
       │    ├─ 流式请求 → text / reasoning / tool_use
       │    ├─ 无可执行 tool_use → finishModelStepWithoutToolCalls → break
       │    └─ 有 tool_use → 调度 + 执行 + tool_result 回灌 → continue
       └─ 结果 "break" 才退出
```

证据：`apps/zcode-cli/packages/core/src/runtime/methods/turn-loop.ts:48`（`while (true)`）、`:77-114`（压缩）、`:220-232`（模型步 + `result === "break"` 退出）；`runtime/methods/turn.ts:664`（调用 `runRegularTurnLoop`）。

### 1.2 唯一推进信号：可执行的 `tool_use`

- 模型步结束后取 `toolCalls`，并过滤 `providerExecuted`，得到 `executableToolCalls`（`runtime/methods/turn-model-step.ts:498`、`:691`）。
- `executableToolCalls.length === 0` → `finishModelStepWithoutToolCalls`（`turn-model-step.ts:754-762`）。
- 否则累积 `toolCallCount`、把带 tool_use 的 assistant 写入历史（`:764-769`），执行工具并返回 `executeToolCallsForModelStep` 的结果（`:770-778`）。
- 工具批次执行完固定返回 `"continue"`（`runtime/methods/turn-tools.ts:279-289`），于是 loop 进入下一轮模型请求。

### 1.3 退出（break）的全部路径

| 触发 | 行为 | 证据 |
|---|---|---|
| 模型步没有可执行 tool_use | 持久化 assistant、`turnMachine.complete(..., "success")`、返回 `break` | `runtime/methods/turn-stop.ts:231-236` |
| 工具结果声明 `turnControl.stopTurnAfterResult` | 标记后续工具取消、`break` | `runtime/methods/turn-tools.ts:233-272`；调度层 `tool/executor/batch-runner.ts:92-128` |
| automation 创建上限命中 | 转纯文本收口后 `break` | `runtime/methods/turn-stop.ts:176-194`、`turn-tools.ts:236-250` |

### 1.4 不退出、继续循环的路径

- output-token 截断续写：`turn-model-step.ts:693-711`（`output_continuation`）。
- 声明式模型降级 / 流恢复 / Start-Plan 繁忙重试 / 上下文超窗后的 reactive compact：`turn-model-step.ts:303-310`、`:311-326`、`:333-397`、`:475-486`。
- 纯文本步后仍有内联 guide：`turn-stop.ts:195-200`；Stop hook 要求继续：`turn-stop.ts:201-217`（上限 3 次，`runtime/methods/hooks.ts:10`）。

### 1.5 没有主循环 `maxTurns`

- `maxTurns` 只出现在子代理与后台摘要 Agent：`runtime/methods/subagent.ts:284`、`subagent/runner.ts:1149`、`runtime/helpers/project-memory-extraction.ts:19`。`turn.ts` / `turn-loop.ts` 中 grep 不到 `maxTurns`。
- 主循环的失控保护是软性的：工具调用预算告警只在配置了 `toolCallWarningThreshold` 时注入提醒（`runtime/helpers/model-anomaly.ts:64-90`），重复工具告警同样可配置（`runtime/methods/turn-tool-warnings.ts:52-75`）。
- 结论：**主循环没有硬性轮数上限，退出完全由模型是否继续产出 tool_use 决定。**

---

## 2. `tool_use` ↔ `tool_result` 如何配对

### 2.1 配对键是模型返回的 `id`，全程原样传递

- 抽取 tool_use 时保留 `id/name/input`（`runtime/methods/context.ts:322-346`）。
- 执行入口把 `ModelToolCall` 转成 `ToolCall`，`id` 不变（`runtime/methods/turn-tools.ts:46-52`）。
- 结果以 `toolCallId` 为键回填：`resultById` 用 `toolCallId` 建索引，再按原始 `coreToolCalls` 顺序对齐（`turn-tools.ts:213-230`）。
- 结算时用同一 `toolCallId` 做三件事：写模型历史、`turnMachine.completeTool`、写持久化 tool part（`runtime/methods/tool-result-settlement.ts:54-65`、`:79-90`、`:121-170`）。

### 2.2 「每个 tool_use 恰好一个 tool_result」靠四条防线

1. **执行结果全量兜底**：未注册工具、schema 失败、权限拒绝、取消都会生成 `ToolExecutionResult`，而不是抛给循环（`tool/executor/call-runner.ts:123-155`、`:201-226`；`tool/executor/errors.ts:6-61`、`:93-117`）。
2. **结果顺序与数量对齐**：`results` 由原始 tool_use 列表 map 出来，缺失才被过滤（`turn-tools.ts:220-222`），随后全部交给结算。
3. **turn-stop 后的兄弟工具补结果**：后续调度组统一生成 `ToolCancelled` 结果（`tool/executor/batch-runner.ts:92-128`）。
4. **冷恢复补结果**：持久化 tool part 若停在 pending/running，重建历史时写入 `"[Tool execution was interrupted before resume]"`（`agent/session-history-hydrator.ts:46`、`:209-210`）。

### 2.3 provider 侧的硬约束

- 适配层要求 tool 消息必须带 `toolCallId` 与 `toolName`，否则抛 `InvalidModelRequest`（`packages/adapters/src/model/transform.ts:100-107`）。
- tool 结果序列化成 AI SDK 的 `{ type: "tool-result", toolCallId, toolName, output }`（同文件 `:128-148`），由 SDK 生成对应 provider 的 `tool_result`。
- assistant 的 tool_use 与该批 tool_result 在同一条持久化 assistant message 下：tool part 以 `messageID = assistantMessageId`、`callID = toolCallId` 写入（`tool-result-settlement.ts:121-130`）。因此恢复时能按 `callID` 精确重放（`session-history-hydrator.ts:191-210`）。

---

## 3. 工具调度：并行 / 串行 / 流式执行

### 3.1 调度规则

- 默认并发上限 10（`tool/scheduler.ts:48`）。
- 并行判定（`tool/scheduler.ts:85-103`）：`destructive` 一律不并行；`concurrentSafe === true` 可并行；`false` 不并行；否则 `readOnly` 为真或 `sideEffectScope === "none"` 才并行。
- 传入调度器的 `readOnly` 已按 `metadata.readOnly && sideEffectScope === "none"` 收紧（`runtime/methods/tools.ts:42-60`）。
- 分组按拓扑层级 → 同层可并行项聚成一组（上限 10），不可并行项独立成组（`tool/scheduler.ts:154-202`）。
- 执行时按组串行推进，组内 `Promise.all`；组大小超过并发上限再切片（`tool/executor/batch-runner.ts:30-54`、`:70-90`）。

### 3.2 流式期间提前执行只读工具

- 仅当 `modelStreaming === "on"` 且工具满足 `readOnly && concurrentSafe && !destructive && !needsApproval && !requiresUserInteraction && sideEffectScope === "none"` 时，在流中就开始执行（`runtime/methods/streaming-tool-coordinator.ts:360-383`）。
- 流中执行的工具同样走完整执行管线（含权限），只是结果在流结束时 `drain` 复用（`streaming-tool-coordinator.ts:292-314`、`turn-model-step.ts:691-692`）。

---

## 4. 权限检查与用户审批：发生在哪个时刻

### 4.1 时序（单次工具调用）

```text
解析/归一化输入 (resolveInput)
  → PreToolUse hooks（可 deny / 改输入）
  → PermissionService.checkPermission  ── allow → 执行
  │                                     ├─ deny  → PermissionDenied 结果
  │                                     └─ ask   → 登记 broker → 发 PermissionRequested
  │                                                  → 等待用户/客户端
  │                                                  → allow / deny / modify / timeout / abort
  → emitToolCallStarted → handler 执行
```

证据：`tool/executor/call-runner.ts:228-248`（归一化先于 hook）、`:250-286`（PreToolUse deny）、`:312-352`（权限先于 started）、`:360-368`（`ToolCallStarted` 在权限通过后才发）。

### 4.2 判定顺序（`PermissionService`）

- `requiresUserInteraction` 的工具直接 ask（`permission/service.ts:116-132`）。
- `guarded` + 工具声明 `userApprovalRule` → `ask` 且 `approvalMode: "user-once"`（`:136-146`）。
- `yolo` / `guarded`（未开 plan）→ allow（`:153-155`）。
- `auto` → deny，理由 `mode.auto.unimplemented`（`:157-164`）。
- 项目规则 deny → ask → 预批（WebFetch、workflow 草稿）→ allowedTools → `edit` / `build` 分支（`:166-265`）。
- `planEnabled` 独立于 mode：`planEnabled ?? mode === "plan"`（`:135`），plan 分支会拦下一切写入（`:211-213`、`:445-490`）。
- 枚举实际有 6 个值：`"plan" | "build" | "edit" | "yolo" | "guarded" | "auto"`（`packages/contracts/src/interfaces/session.port.ts:32`）。**不存在第五档「auto 已实现」**——auto 当前固定 deny。

### 4.3 「ask」不是默认放行

- 工具自报的 `prepareApproval` 只能把 ask 收窄成 proceed 或补预览；预览生成失败仍然 ask（`tool/executor/approval-gate.ts:36-74`）。
- `PermissionRequested` 事件在 broker **登记完成后**才发布，避免用户秒回与 deferred 登记竞态（`tool/executor/permission-request.ts:48-91`）。
- 无客户端时 `DenyPermissionBroker` 直接 deny（`permission/broker.ts:24-38`）；`ManualPermissionBroker` 以 `requestId` 为键保存 pending，支持 abort 与可选 timeout（`permission/broker.ts:41-167`）。
- 终态只发一次 `PermissionResolved`（`tool/executor/permission-flow.ts:274-290`）。

### 4.4 审批交互面

- CLI 把 `PermissionRequested` 投影成 `ToolCallRow.status = "pendingApproval"` + `pendingInteractions`（`bootstrap/src/zcode-protocol-v4/product-projection.ts:3306-3334`）。
- UI 从 `pendingInteractions` 渲染 `PermissionDialog`，用户选择后发 `resolveInteraction` 命令（`packages/ui/src/v4/V4InteractionDialogs.tsx:327-369`、`:199-230`）。
- `resolveInteraction` 命中则投递给等待中的 deferred；未命中按幂等成功（多端先到先得）（`bootstrap/src/zcode-protocol-v4/commands/handlers/interaction-background.ts:45-63`）。
- `AskUserQuestion` / `ExitPlanMode` / MCP elicitation 复用同一条 interaction 通道，但投影成 `userInput` 而不是权限弹窗（`product-projection.ts:3343-3413`）。

---

## 5. 流式事件的最小粒度与到界面的映射

### 5.1 Core 事件粒度

- 事件类型全集见 `packages/contracts/src/events/session.events.ts:84-185`。
- 模型流事件粒度是 **provider 增量**：`start / text_start / text_delta / text_end / reasoning_start / reasoning_delta / reasoning_end / tool_input_start / tool_input_delta / tool_input_end / tool_call / finish / error`（同文件 `:748-773`）。
- `text_delta`、`reasoning_delta` 每个 provider chunk 一条（`runtime/methods/model.ts:254-264`、`:296-312`）。
- `tool_input_delta` 不逐字符发出：累积到出现换行（真实或 JSON 转义）或累计 4096 字符才 flush（`model.ts:27-33`、`:210-224`）。
- 一条 delta 事件就是一次 `appendEvent`（`runtime/methods/model-streaming-event.ts:5-14`）。
- 写入串成有序队列，高水位 128 条；超过才等待 drain，finish/error 边界强制 drain（`runtime/methods/model-streaming-event-queue.ts:4-73`、`model.ts:450-454`）。

### 5.2 另一条粒度更粗的事件线：工具生命周期

`ToolCallScheduled / ToolCallStarted / ToolCallProgress / ToolCallResult / ToolCallError / ToolBatchComplete`（`session.events.ts:135-140`），以及流中执行的 ledger：`StreamingToolLedgerUpdated`（`:123`）。

### 5.3 Core → UI 的映射

- 增量在 CLI 侧先折成 **行级 delta**：`text_delta → row.delta(path:"text")`、`tool_input_delta → appendStreamingToolInput`、`tool_call → finalizeStreamingToolInput`（`product-projection.ts:2605-2671`）。
- 工具事件映射：Scheduled 建行（含完整 `inputText`）、Started 置 running、Progress 更新进度（仅 running 时）、Result 置 success/error 并清进度（`product-projection.ts:3072-3231`）。
- 行模型状态机固定为 `inputStreaming | pendingApproval | running | success | error | cancelled`（`packages/shared/src/zcode-protocol-v4/rows.ts:203-232`）。
- delta 合并是纯函数：相邻同 `(rowId, path)` 的 `row.delta` 拼接，`row.upserted` 吞掉更早的 delta，`row.removed` 是屏障（`packages/shared/src/zcode-protocol-v4/coalesce.ts:1-11`、`:82-140`）。
- 传输层是「有界 delta 日志 + subscribe(base)」：客户端落后在保留窗内给续传 delta，超出保留窗才给 snapshot（`bootstrap/src/zcode-protocol-v4/conversation-topic-publisher.ts:1-11`、`:253-270`）。
- 流式事件本身是瞬态：turn 结束被下一 turn 取代后可淘汰；durable 文本由持久化消息重建（`packages/contracts/src/events/session-event-retention.ts:4-26`）。

---

## 6. 上下文压缩：何时压、压后保留什么

### 6.1 三个自动入口（每次模型请求前）

每轮模型请求前固定执行 `microcompactIfNeeded` → `autoCompactIfNeeded`（`runtime/methods/turn-loop.ts:77-113`）。

| 路径 | 触发 | 动作 | 证据 |
|---|---|---|---|
| Microcompact | 距上次 assistant 完成 > 60 分钟，或估算 token ≥ 自动阈值 × 0.9（且 ≤ 阈值 − 2k） | 把旧的可压缩工具结果替换为 `[Old tool result content cleared]`，保留最近 5 组；节省不足 256 token 则放弃 | `compact/microcompact.ts:12-20`、`:125-156`；`runtime/methods/microcompact.ts:36-101` |
| Auto compact | 估算/或 provider usage token ≥ `effectiveContextWindow − 13k`；`effective = contextWindow − min(maxOutput, 21k)` | 调用模型生成摘要，写入压缩边界 | `compact/policy.ts:6-14`、`:67-88`、`:90-155`；`runtime/methods/compact.ts:184-295` |
| Reactive compact | provider 返回上下文超窗（异常或 finishReason） | 同一摘要流程，`trigger: reactive`，每个模型步只尝试一次 | `runtime/methods/turn-model-step.ts:475-486`、`:546-570`、`:788-850` |

其它保护：
- 连续 3 次自动压缩失败 → 熔断（`compact/policy.ts:14`、`:136-144`）。
- 快速回填熔断：3 次压缩后工具轮次仍 < 3 → 抛 `compact_rapid_refill`（`runtime/methods/turn-loop-state.ts:21-22`、`:160-174`、`turn-loop.ts:102-109`）。
- 消息轮次不足 2 个 assistant 轮 → 不压缩（`compact/manual.ts:69-75`）。

手动 `/compact` 走 `executeManualCompact`（`runtime/methods/compact.ts:45`）。`CompactTrigger.SessionMemory` 在契约里存在（`packages/contracts/src/compact/index.ts:9-15`），但 core/bootstrap 中没有执行入口（grep 仅命中 `runtime/helpers/compact.ts:46`、`:60` 的默认映射）——**这一路径在本仓库未见实现**。

### 6.2 压缩后保留什么

运行时重建顺序（`runtime/helpers/compact.ts:72-88`）：

```text
[context prefix / system / skills listing]
[compact summary 消息]
[preservedEntries（最近若干完整轮次）]
[post-compact reminders：agent listing、plan 文件引用、read-state]
```

- 自动 / 主动压缩默认保留 **最近 1 个 assistant 起始轮**（`runtime/helpers/compact-selection.ts:30-57`、`:209-227`）。
- 摘要提示词要求保留用户明确请求、关键文件/代码片段、错误与修复、所有用户消息、待办与当前工作（`compact/prompt.ts:14-109`），并用 `<analysis>/<summary>` 包裹后只留 summary（`:119-131`）。
- 摘要消息是 synthetic 的 `user` 消息：UI/transcript 隐藏、provider 可见（`runtime/methods/compact-persistence.ts:309-350`）。
- 同时写一个 `compaction` part，携带 `compactBoundary` 与 `tail_start_id`（同文件 `:351-366`）。
- 冷恢复按「最后一个 active compaction 边界」切片，并把 `preservedSegment` 插回 summary 之后（`agent/session-history-hydrator.ts:222-277`、`agent/compact-session.ts:4-46`）。

---

## 7. 会话持久化与恢复

### 7.1 存储介质与 schema

- 默认 SQLite：`~/.zcode/cli/db/db.sqlite`（`apps/zcode-cli/packages/adapters/src/storage/session-store/paths.ts:1-12`）。
- 核心表：`session`、`message`、`part`、`todo`、`session_entry`、`permission`、`input_history`、`session_input`，以及 usage 表（`adapters/src/storage/session-store/migrations.ts:14-110`、`:396-520`、`:691-789`）。
- `message` / `part` 用 `data` JSON 列存可变字段，`session_id` 外键级联删除，`message.sequence` 保证同会话顺序（`migrations.ts:41-62`；`repositories/messages.ts:40-111`）。
- 写入入口是 `sessionStore.saveMessage` / `savePart`，assistant 消息在完成时二次 save（`runtime/methods/message-persistence.ts:342-367`、`repositories/messages.ts:40-111`、`:113`）。

### 7.2 事件日志不是持久事实

- 默认 event store 是内存实现：`eventStore: options.eventStore ?? createInMemorySessionEventStore()`（`bootstrap/src/app/create-app.ts:830`）。
- 保留策略 `turn-window`，`ModelStreaming`、`ToolCallProgress` 等瞬态事件在 turn 被取代或 sealed turn 超过 120s 后淘汰（`packages/contracts/src/events/session-event-retention.ts:9-26`）。
- 因此 durable 事实是 `message` + `part`；事件用于 live 投影与进程内恢复（`conversation-topic-publisher.ts:1-11`）。

### 7.3 恢复流程

`resumeFromStore`（`runtime/methods/resume.ts`）：

1. `getSession`（`:74`），读全部 messages（`:93-97`）。
2. 恢复 read-file state（`:140-148`）、初始化 context（`:150`）。
3. 修复中断的 compaction timeline（`:151`）。
4. 从 messages/parts 重建模型历史（`:155-163`）；运行中/待执行的 tool part 得到 synthetic interrupted 结果（`session-history-hydrator.ts:209-210`）。
5. 计算 active messages：处理 rewind / branch cut / 最后一个 compact 边界（`:168-176`、`session-history-hydrator.ts:222-277`）。
6. 恢复 checkpoint、rewind 索引（`:194-195`）、权限模式与执行状态（`:196-220`）、动态 workflow 激活（`:222`）、权限授予标记（`:224`）。
7. 恢复 todo / target、注入目标状态，发出 `SessionResumed` 并跑 `SessionStart` hook（`:244-258`）。

冷恢复入口：订阅一个不在内存中的会话时由 `ColdSessionResumeCoordinator` 拉起 runtime（`bootstrap/src/zcode-protocol-v4/cold-session-resume.ts:1-19`、`:44-62`）。

---

## 8. 子代理与后台任务

### 8.1 子代理是独立 AgentRuntime

- 每次 spawn 生成独立 `childSessionId`（`subagent/runner.ts:809`）。
- 子 runtime 有自己的工具白名单/黑名单、模型选择、context 与 turn 状态（`runtime/methods/subagent.ts:284-309`）。
- 子代理禁止再派生子代理：`subagents.enabled: false`（`subagent.ts:305-308`），且文档明确 child 不暴露 `Agent`/`Task`（`docs/subagent-compatibility.md:40-50`）。
- 子代理有自己的 `maxTurns`，默认 4（`subagent.ts:284`）。
- 权限：内置 `Explore` 用独立只读 `PermissionService`；`general-purpose` 与自定义 profile 继承父权限服务（`subagent.ts:340-343`）。
- 阻塞交互（权限 / AskUserQuestion / provider headers）路由回父 session（`subagent.ts:214-234`）。

### 8.2 事件镜像

- child 工具生命周期与权限事件被镜像到父会话，`toolCallId` 加 `tool_subagent_<agentId>_` 前缀、附 `childSessionId/parentToolCallId/source:"subagent"`（`subagent/tool-event-mirror.ts:19-31`、`:64-82`）。
- 父 V4 投影禁止把镜像工具物化成父自己的 `ToolCallRow`，父只保留 subagent 摘要行（`docs/agent-tool-call-renderer.md:15-17`；投影过滤见 `product-projection.ts:3073`、`:3132`）。

### 8.3 前台 / 后台

- `runInBackground` 或 profile 声明 background → 直接后台（`runner.ts:150-160`）。
- 前台调用与 `waitForBackgroundRequest` 做 race：收到后台请求即返回 `async_launched`，同一 child completion 继续后台完成（`runner.ts:305-321`）。
- 后台完成后：写产物、登记 terminal 状态、`enqueueBackgroundNotification`（`runner.ts:1497-1543`）；`task.notified` 去重（`:1850-1860`）。
- 通知进入父 runtime 的 **runtime command queue**（`mode:"task-notification"`, `priority:"next"`），并同时写入 session_input 账本（`runtime/methods/background-notifications.ts:44-82`）。
- 入队后立刻 `drainRuntimeCommandQueue()`（`runtime/methods/runtime-command-queue.ts:23-35`）；drain 可以合并同批通知，然后用 `inputVisibility: "model-only"`、`inputSource: "background_task"` 发起一次普通 turn（`:317-366`）。
- 结论：**父 turn 已 idle 时，后台子代理完成会自动唤醒一次父模型请求**，用户不需要再输入（与 `docs/cli-background-subagent-wake-plan.md:5-12` 的目标一致，且实现已落在 command queue drain 上）。
- 后台通知的 synthetic 消息对 UI 不可见、对 provider 可见（`runtime/methods/background-notifications.ts:184-191`）。

---

## 9. 明显不存在 / 未实现的能力（避免误抄）

| 项 | 事实 |
|---|---|
| 主循环 `maxTurns` 硬上限 | 不存在；只有子代理有（`subagent.ts:284`） |
| `auto` 权限模式 | 枚举存在，但固定 deny（`permission/service.ts:157-164`） |
| `session_memory` 压缩触发 | 契约有（`contracts/src/compact/index.ts:9-15`），core/bootstrap 无执行入口 |
| 持久化事件日志 | 默认内存 + turn-window 淘汰（`create-app.ts:830`、`session-event-retention.ts:9-26`） |
| 权限请求默认超时 | 只有传入 `permissionTimeoutMs` 才起定时器（`permission/broker.ts:108-126`） |
