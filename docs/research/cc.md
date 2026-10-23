# Claude Code Agent 运行时调研（参考实现 `../cc`）

调研对象：`/Users/baike3/CodeRepo/cc/src`（Bun + TypeScript，泄漏源码，只读）。
目标：为 `anc-dsk` 的 Go 实现提供可落地依据。本文所有结论都带 `file:line`；未在参考代码中找到的内容标注为「不存在」或「无法确认」。

## 0. 阅读范围与源码完整性

实际精读的文件：

| 主题 | 文件 |
|---|---|
| 单轮循环 | `src/query.ts`（1729 行） |
| 循环辅助 | `src/query/stopHooks.ts`、`src/query/tokenBudget.ts`、`src/query/config.ts`、`src/query/deps.ts` |
| 会话编排 | `src/QueryEngine.ts`（1295 行） |
| 工具契约 | `src/Tool.ts`、`src/tools.ts`、`src/services/tools/toolOrchestration.ts`（188 行）、`src/services/tools/toolExecution.ts`（1745 行） |
| 内置工具 | `src/tools/FileReadTool/*`、`src/tools/BashTool/BashTool.tsx` |
| 权限 | `src/types/permissions.ts`、`src/utils/permissions/permissions.ts`、`src/hooks/useCanUseTool.tsx`、`src/hooks/toolPermission/handlers/interactiveHandler.ts` |
| 模型请求 | `src/services/api/claude.ts`（125KB）、`src/services/api/withRetry.ts` |
| 压缩 | `src/services/compact/{autoCompact,microCompact,compact,sessionMemoryCompact,timeBasedMCConfig}.ts` |
| 上下文 | `src/context.ts`、`src/utils/api.ts`、`src/utils/messages.ts` |
| 持久化 | `src/utils/sessionStorage.ts`、`src/utils/conversationRecovery.ts` |

源码不完整处（被 import 但目录里没有文件）：

- `src/types/message.ts`：`query.ts:39` 从这里导入 `Message` / `StreamEvent` / `TombstoneMessage`，但 `src/types/` 下不存在该文件。**流式事件的 TS 类型定义无法确认**，只能从产出点推断。
- `src/query/transitions.ts`：`query.ts:104` 导入 `Terminal`、`Continue`，文件不存在。**退出原因的完整类型无法确认**，只能按 `return { reason: ... }` 的字面量汇总。
- `src/services/compact/reactiveCompact.ts`：`query.ts:16` 条件 require，文件不存在。
- `src/services/contextCollapse/`：目录存在但为空；`query.ts:19` require 的 `index.js` 不存在。

---

## 1. 总体结构：会话层 → 单轮循环

```text
QueryEngine.submitMessage(prompt)                 // 会话级：一个会话一个实例
  ├─ 组装 systemPrompt / userContext / systemContext   QueryEngine.ts:286-325
  ├─ processUserInput（slash command、附件、模式）      QueryEngine.ts:410-431
  ├─ 先把 user message 写入 transcript                  QueryEngine.ts:436-463
  └─ for await (query({...}))                          QueryEngine.ts:675-686
        └─ query() → queryLoop()                        query.ts:219-239
             while (true) 每轮：
               1. 压缩流水线（tool result 预算→snip→microcompact→collapse→autocompact）
               2. callModel 流式请求
               3. 收集 tool_use；有则执行工具、回灌 tool_result、继续
               4. 没有 tool_use → stop hooks / token 预算 → 返回
```

`QueryEngine` 明确是「一个会话一个实例，`submitMessage()` 每调用一次 = 会话内一个 turn，状态跨 turn 保留」（`QueryEngine.ts:180-207`）。`query()` 只是薄包装，`queryLoop()` 才是循环本体（`query.ts:219-251`）。

---

## 2. 主循环：靠什么信号推进、什么时候退出

### 2.1 唯一推进信号是 `tool_use`

循环内每轮的状态变量：

```ts
const assistantMessages: AssistantMessage[] = []
const toolResults: (UserMessage | AttachmentMessage)[] = []
const toolUseBlocks: ToolUseBlock[] = []
let needsFollowUp = false
```

`query.ts:551-558`。代码注释直接写明：

> `stop_reason === 'tool_use'` is unreliable -- it's not always set correctly. Set during streaming whenever a tool_use block arrives — the sole loop-exit signal.
> —— `query.ts:553-556`

落地规则：

- 每个 assistant 消息里筛出 `type === 'tool_use'` 的 block；只要有，就 `toolUseBlocks.push(...)` 且 `needsFollowUp = true`（`query.ts:826-835`）。
- 流结束后先看 `abortController.signal.aborted`，中断优先处理（`query.ts:1015-1052`）。
- `if (!needsFollowUp)` → 走收尾分支（stop hooks / token 预算 / 返回），`query.ts:1062`。
- 否则执行工具，把 `messagesForQuery + assistantMessages + toolResults` 组成下一轮 `state.messages`，继续 `while (true)`（`query.ts:1714-1727`）。

注意：`query.ts:1714` 的日志点叫 `query_recursive_call`，但实现不是递归，而是给 `state` 重新赋值后 `continue`（`query.ts:1715-1727`）。Go 实现照「状态机 + 单循环」即可，不要真的递归。

### 2.2 turn 计数与 `maxTurns`

- `turnCount` 从 1 开始（`query.ts:276`）。
- 每次「有工具结果、准备下一轮」时 `nextTurnCount = turnCount + 1`（`query.ts:1679`）。
- 超过 `maxTurns` 时产出一条 `max_turns_reached` 附件并 `return { reason: 'max_turns' }`（`query.ts:1705-1712`）。
- 中断路径下也会检查一次（`query.ts:1507-1514`）。
- `QueryEngine` 收到该附件后转成 `result.subtype = 'error_max_turns'` 并结束（`QueryEngine.ts:842-873`）。

因此 `maxTurns` 约束的是「模型调用轮数」，不是工具调用次数。

### 2.3 退出原因汇总（按源码字面量）

| 退出原因 | 触发条件 | 位置 |
|---|---|---|
| `blocking_limit` | 未发生压缩且 token 已达硬阻塞线（自动压缩关闭时），直接返回错误 | `query.ts:628-648` |
| `image_error` | 图片尺寸/缩放异常 | `query.ts:970-978` |
| `model_error` | `callModel` 抛出非 fallback 错误 | `query.ts:955-997` |
| `aborted_streaming` | 流式阶段用户中断 | `query.ts:1015-1052` |
| `prompt_too_long` | 413 且 reactive compact / collapse 均无法恢复 | `query.ts:1168-1183` |
| `completed` | 无 tool_use 且 stop hooks 通过、token 预算不再续跑 | `query.ts:1262-1264`、`1357` |
| `stop_hook_prevented` | Stop hook 明确阻止继续 | `query.ts:1278-1280` |
| `aborted_tools` | 工具执行阶段用户中断 | `query.ts:1485-1516` |
| `hook_stopped` | hook 发出 `hook_stopped_continuation` 附件 | `query.ts:1519-1521` |
| `max_turns` | 超过 `maxTurns` | `query.ts:1705-1712` |

另外：`QueryEngine` 层还有 USD 预算退出（`error_max_budget_usd`，`QueryEngine.ts:971-1002`）和结构化输出重试上限（`error_max_structured_output_retries`，`QueryEngine.ts:1004-1048`）。

### 2.4 收尾分支里的「重试型 continue」

没有新 `tool_use` 并不等于结束，以下路径会改状态并 `continue`：

1. **413 / 媒体过大**：先尝试 `contextCollapse.recoverFromOverflow`，失败再 `reactiveCompact.tryReactiveCompact`（`query.ts:1070-1175`）。每次只尝试一次：`hasAttemptedReactiveCompact` 防止无限循环（`query.ts:1119-1166`，注释解释了 stop-hook 死循环教训 `1292-1297`）。
2. **`max_output_tokens`**：可先升到 `ESCALATED_MAX_TOKENS` 重试一次（`query.ts:1195-1221`），之后最多注入 3 次「继续写」的 meta user 消息（`MAX_OUTPUT_TOKENS_RECOVERY_LIMIT = 3`，`query.ts:164`；恢复逻辑 `1223-1252`）。
3. **Stop hook 返回 blockingErrors**：把错误作为消息追加，设 `stopHookActive = true`，继续下一轮（`query.ts:1282-1306`）。
4. **Token 预算续跑**：见 2.5。

### 2.5 Token 预算（与上下文窗口无关）

`src/query/tokenBudget.ts` 是一套「工作到花完 N token」的续跑机制，独立于压缩：

- 预算来自用户输入（`+500k` / `use 2M tokens` 等），`src/utils/tokenBudget.ts:1-29`；
- 当前 turn 的预算与起始 output token 快照存全局状态，`src/bootstrap/state.ts:729-737`；
- 判定（`tokenBudget.ts:45-92`）：
  - 子 agent（有 `agentId`）、预算为空/≤0 → `stop`；
  - 未到 90% 且非「收益递减」→ `continue`，注入 nudge 消息；
  - 「收益递减」= 已续跑 ≥3 次且两次增量都 <500 token（`tokenBudget.ts:3-4, 59-62`）。
- 循环侧收到 `continue` 就追加 meta user 消息并继续（`query.ts:1308-1341`）。

Go 实现注意：这是产品功能而非循环必需项；但阈值语义（90%、500、3 次）要原样保留才能复刻行为。

---

## 3. `tool_use` 与 `tool_result` 如何配对

### 3.1 配对单位与结构

- 一个 tool_use 的 ID 是模型给的 `block.id`；对应的 tool_result 用 `tool_use_id` 回指（`toolExecution.ts:396-408`）。
- tool_result 不是独立的消息类型，而是放在一条 `user` 消息的 content block 数组里，并带 `sourceToolAssistantUUID` 指回产生它的 assistant 消息（`toolExecution.ts:397-408`、`query.ts:123-149`）。
- 持久化时 `sourceToolAssistantUUID` 会被写成 `parentUuid`，让链正确分叉（`sessionStorage.ts:1028-1037`）。

### 3.2 五个「必须生成结果」的兜底点

参考实现把「每个 tool_use 有且只有一个 tool_result」当成硬不变量，所有异常路径都生成结构化结果：

| 场景 | 结果 | 位置 |
|---|---|---|
| 模型调了不存在的工具 | `is_error: true` 的 `<tool_use_error>No such tool available` | `toolExecution.ts:369-411` |
| 工具不存在/执行抛异常 | `catch` 后生成 `Error calling tool ...` 结果 | `toolExecution.ts:469-488` |
| 参数 schema 校验失败 | `InputValidationError` 结果 | `toolExecution.ts:614-679` |
| 工具自定义 `validateInput` 失败 | `<tool_use_error>...` 结果 | `toolExecution.ts:683-732` |
| 权限被拒 / 用户拒绝 | `is_error: true`，内容为拒绝原因 | `toolExecution.ts:995-1103` |
| 执行前已被取消 | 取消消息结果 | `toolExecution.ts:415-452` |
| 流式中断或模型错误导致 assistant 已产出但未执行 | `yieldMissingToolResultBlocks()` 为每个 tool_use 补 `is_error` 结果 | `query.ts:123-149`、`984-986`、`1015-1029` |

`StreamingToolExecutor` 在工具被丢弃/中断时也会生成 synthetic error block（`StreamingToolExecutor.ts:276-292`、`332-345`）。

### 3.3 恢复时的配对修补

恢复 transcript 时，`filterUnresolvedToolUses` 统计所有 `tool_use.id` 与 `tool_result.tool_use_id`，把「所有 tool_use 都未配对」的 assistant 消息整条删除（`utils/messages.ts:2795-2841`），调用点在 `conversationRecovery.ts:186-189`。这是为了不让历史里残留的孤儿 tool_use 触发 API 400。

### 3.4 并行/串行编排

- 并发上限默认 10，可用 `CLAUDE_CODE_MAX_TOOL_USE_CONCURRENCY` 覆盖（`toolOrchestration.ts:8-12`）。
- 分批依据是 `tool.isConcurrencySafe(input)`，不是 `isReadOnly`：连续的安全工具合并成一批并发跑，非安全工具单独一批串行跑（`toolOrchestration.ts:91-116`）。
- 并发批用 `getMaxToolUseConcurrency()` 限流（`toolOrchestration.ts:152-177`）；串行批逐个执行并即时传递 context 修改（`toolOrchestration.ts:118-150`）。
- Bash 的 `isConcurrencySafe` 直接等于 `isReadOnly`（`BashTool.tsx:434-441`），所以只读命令天然并行。
- 工具执行中产生的 context 修改在并发批里先收集、批结束再按顺序应用（`toolOrchestration.ts:30-63`）。

### 3.5 消息顺序

工具结果必须在工具全部执行完之后、且不能与普通 user 消息交错才组装进历史：

> Be careful to do this after tool calls are done, because the API will error if we interleave tool_result messages with regular user messages.
> —— `query.ts:1535-1536`

随后才追加附件、记忆、技能发现等 user 侧消息（`query.ts:1580-1628`），最后一次性组成下一轮 messages（`query.ts:1715-1727`）。

---

## 4. 权限检查与用户审批发生在哪个时刻

### 4.1 模式与决策类型

模式共五个外部可见 + 内部两个：

```ts
EXTERNAL_PERMISSION_MODES = ['acceptEdits','bypassPermissions','default','dontAsk','plan']
InternalPermissionMode = ExternalPermissionMode | 'auto' | 'bubble'
```

`src/types/permissions.ts:16-38`。运行时用户可选集合还会按 feature gate 追加 `auto`（`permissions.ts:33-38`）。

决策只有三种，外加工具内部使用的 `passthrough`：

- `allow` / `deny` / `ask`：`src/types/permissions.ts:44`；
- `passthrough` 表示「工具没有意见，交给通用权限系统」，最终被转成 `ask`（`utils/permissions/permissions.ts:1299-1310`）。

`ask` 决策携带 `message`、`suggestions`（可一键加规则）、`blockedPath`、`pendingClassifierCheck` 等（`src/types/permissions.ts:199-236`）。

### 4.2 时刻：在参数校验和 PreToolUse hook 之后、`tool.call` 之前

单个工具的执行流水线（`checkPermissionsAndCallTool`）：

```text
1. inputSchema.safeParse            toolExecution.ts:614-680
2. tool.validateInput(...)          toolExecution.ts:683-733
3. PreToolUse hooks                 toolExecution.ts:800-862
4. resolveHookPermissionDecision → canUseTool   toolExecution.ts:921-931
5. permissionDecision != allow → 生成 is_error tool_result 并 return  toolExecution.ts:995-1103
6. permissionDecision.updatedInput 覆盖输入      toolExecution.ts:1128-1132
7. tool.call(...)                   toolExecution.ts:1206-1222
```

即：**权限检查在工具执行之前完成，且一定在 hook 之后**。hook 可以给出 allow（跳过弹窗），但 deny/ask 规则仍然优先（`toolHooks.ts:332-402`，特别是 `373-396`）。

### 4.3 通用权限判定的内部顺序

`hasPermissionsToUseToolInner`（`utils/permissions/permissions.ts:1158-1319`）：

1. 已 abort → 抛 `AbortError`（`1163-1165`）。
2. 整工具 deny 规则 → `deny`（`1171-1181`）。
3. 整工具 ask 规则 → `ask`（例外：沙箱内 Bash 自动允许，`1184-1206`）。
4. 调 `tool.checkPermissions` 拿工具级结果（`1210-1223`）。
5. 工具级 `deny` 直接返回（`1226-1228`）。
6. `requiresUserInteraction` + `ask` 直接返回（`1231-1236`）。
7. 内容级 ask 规则优先于 bypass 模式（`1244-1250`）。
8. safetyCheck（`.git/`、`.claude/`、shell 配置等）免疫 bypass（`1255-1260`）。
9. `bypassPermissions`（或 plan 模式下原本就允许 bypass）→ `allow`（`1268-1281`）。
10. 整工具 allow 规则 → `allow`（`1284-1297`）。
11. 其余 `passthrough` → `ask`（`1299-1318`）。

工具级规则复用同一套判定：例如 Write/Edit 的 `checkWritePermissionForTool` 里，`acceptEdits` 且在工作目录内直接 `allow`（`utils/permissions/filesystem.ts:1360-1375`），否则默认 `ask`（`1395-1409`）。

### 4.4 `ask` 之后：审批是一个被 await 的 Promise

- `CanUseToolFn` 签名返回 `Promise<PermissionDecision>`（`hooks/useCanUseTool.tsx:27`）。
- 实现里先跑 `hasPermissionsToUseTool`（`useCanUseTool.tsx:37`）；
  - `allow` → 立即 resolve，并记录日志（`39-53`）；
  - `deny` → resolve deny（`64-92`）；
  - `ask` → 依次尝试 coordinator / swarm / Bash 分类器，最后 `handleInteractivePermission(..., resolve)`（`93-168`）。
- 交互式路径把请求推进确认队列，挂上 `onAllow` / `onReject` / `onAbort` 回调；**只有回调触发时才 resolve 外层的 Promise**（`interactiveHandler.ts:43-56`、`92-107`、`154-180`）。
- 权限检查本身抛异常（abort 等）→ `cancelAndAbort(...)`，不会默认放行（`useCanUseTool.tsx:171-179`）。

所以 `ask` 的语义是「循环阻塞等待」；没有用户响应就不会继续执行工具（除非超时/中断路径）。

`dontAsk` 模式在最后统一把 `ask` 转 `deny`：

```ts
if (appState.toolPermissionContext.mode === 'dontAsk') { ... }
```

`utils/permissions/permissions.ts:503-517`。注意它放在 `allow` 分支之后，避免被提前 return 绕过。

### 4.5 拒绝/中断如何回到模型

拒绝不是抛异常，而是生成一条 `is_error: true` 的 tool_result，让模型看到原因并自行调整（`toolExecution.ts:1029-1071`）。这也是「错误不能打断循环」的实现方式。

---

## 5. 流式事件的最小粒度

参考实现有**三个层级**：

### 5.1 最细：原始 SSE 事件逐条转发

`queryModelWithStreaming` 解析 SDK 的 raw stream，每个 `part` 原样包一层：

```ts
yield { type: 'stream_event', event: part, ...(part.type === 'message_start' ? { ttftMs } : undefined) }
```

`services/api/claude.ts:2299-2303`。`part.type` 覆盖 `message_start` / `content_block_start` / `content_block_delta` / `content_block_stop` / `message_delta` / `message_stop`（同一 switch：`claude.ts:1979-2297`）。

增量粒度再往下是 `delta.type`：`text_delta`、`thinking_delta`、`signature_delta`、`input_json_delta`、`citations_delta`（`claude.ts:2083-2161`）。文本增量是 `contentBlock.text += delta.text`（`claude.ts:2125`），tool 入参是 `contentBlock.input += delta.partial_json`（`claude.ts:2111`）。

### 5.2 中间层：每个 content block 结束产出一条 assistant 消息

`content_block_stop` 时用累积块构造一条 `AssistantMessage` 并 `yield`：

```ts
newMessages.push(m); yield m
```

`claude.ts:2171-2211`。所以一条模型回复里有 N 个 content block，就会产出 N 条 assistant 消息（N 个 uuid，同一个 `message.id`），后续持久化时靠 message.id 做兄弟分组（`sessionStorage.ts:2096-2135` 的注释明确说明这一点）。

`message_delta` 到达后，**原地修改**最后一条消息的 `usage` 和 `stop_reason`（`claude.ts:2229-2248`）；注释强调必须直接改属性，不能替换对象，否则写队列持有的是旧引用（`2236-2241`）。

### 5.3 循环层：收到就转发，不等整轮

`query.ts` 对 `callModel` 的每个产出立即 `yield`：

- 先判断是否要暂扣可恢复错误（413/媒体/max_output_tokens），否则立即 `yield yieldMessage`（`query.ts:788-825`）；
- 每轮循环开始先 `yield { type: 'stream_request_start' }`（`query.ts:337`）；
- 工具进度消息由 `StreamingToolExecutor.getCompletedResults()` 立即产出（`StreamingToolExecutor.ts:412-440`），`query.ts` 收到就 `yield`（`query.ts:847-862`、`1384-1408`）；
- 压缩边界、tool_use summary、tombstone 也各自 `yield`（`query.ts:406-408`、`1054-1060`、`716-718`）。

### 5.4 会话层的转发开关

`QueryEngine` 只在 `includePartialMessages` 为真时把 `stream_event` 透传给 SDK 调用方（`QueryEngine.ts:818-826`），其余消息类型（assistant/user/progress/attachment/system）都会转换后转发（`QueryEngine.ts:757-969`）。

对 Go/Wails 的直接影响：如果前端需要逐字打字效果，必须转发 5.1 的原始增量；如果只需按块渲染，转发 5.2 的 assistant 消息即可。参考实现两者都保留，且用 `includePartialMessages` 控制。

---

## 6. 上下文何时压缩、压缩后保留什么

### 6.1 时机：每次模型调用之前，固定五段流水线

在 `queryLoop` 的每轮 `while` 里、`callModel` 之前，按以下顺序执行（顺序本身有语义，注释写明了原因）：

| 顺序 | 步骤 | 作用 | 位置 |
|---|---|---|---|
| 1 | `applyToolResultBudget` | 单条消息里 tool_result 总量超限时，把大结果落盘换成预览 | `query.ts:369-394` |
| 2 | `snipCompactIfNeeded` | 历史裁剪（feature gate `HISTORY_SNIP`），产出边界消息 | `query.ts:396-410` |
| 3 | `microcompactMessages` | 清空旧工具结果内容 | `query.ts:412-426` |
| 4 | `applyCollapsesIfNeeded` | 上下文折叠（feature gate `CONTEXT_COLLAPSE`），只读投影，不 yield | `query.ts:428-447` |
| 5 | `autoCompactIfNeeded` | 生成摘要式压缩 | `query.ts:453-467` |

注释给出的顺序理由：tool result 预算必须在 microcompact 之前，因为缓存式 microcompact 只按 `tool_use_id` 匹配、不看内容（`query.ts:369-372`）；collapse 在 autocompact 之前，因为 collapse 把占用降到 autocompact 阈值以下时，autocompact 就是 no-op，从而保留更细的上下文（`query.ts:428-432`）。

### 6.2 阈值怎么算

`src/services/compact/autoCompact.ts`：

```text
effectiveContextWindow = contextWindow(model) - min(maxOutputTokens(model), 20000)
autoCompactThreshold   = effectiveContextWindow - 13000
warningThreshold       = threshold - 20000
errorThreshold         = threshold - 20000
blockingLimit          = effectiveContextWindow - 3000
```

- 预留摘要输出：`MAX_OUTPUT_TOKENS_FOR_SUMMARY = 20_000`，`autoCompact.ts:28-37`；
- `AUTOCOMPACT_BUFFER_TOKENS = 13_000`、`WARNING/ERROR = 20_000`、`MANUAL_COMPACT_BUFFER_TOKENS = 3_000`，`autoCompact.ts:62-65`；
- 计算函数 `getAutoCompactThreshold` / `calculateTokenWarningState`，`autoCompact.ts:72-145`；
- 支持 `CLAUDE_CODE_AUTO_COMPACT_WINDOW` 收紧窗口（`autoCompact.ts:40-46`）和 `CLAUDE_AUTOCOMPACT_PCT_OVERRIDE`（`autoCompact.ts:79-88`）；
- 连续失败 3 次后熔断，不再尝试（`MAX_CONSECUTIVE_AUTOCOMPACT_FAILURES = 3`，`autoCompact.ts:67-70`、`257-265`、`334-349`）。

硬阻塞判断只在「没有发生压缩、且不是 compact/session_memory 查询、且未启用 reactive/collapse 兜底」时执行（`query.ts:592-648`）。

### 6.3 microcompact 具体做什么

- 只处理白名单工具：`Read`、shell 工具、`Grep`、`Glob`、`WebSearch`、`WebFetch`、`Edit`、`Write`（`microCompact.ts:41-50`）。
- 时间触发：距最后一条主线程 assistant 消息超过 `gapThresholdMinutes`（默认 60 分钟）时，认为服务端 prompt cache 已失效；保留最近 `keepRecent` 条（默认 5，下限 1），其余 tool_result 内容替换为 `[Old tool result content cleared]`（`microCompact.ts:446-530`；配置默认值 `timeBasedMCConfig.ts:30-34`）。
- 缓存式 microcompact（`CACHED_MICROCOMPACT`）只对主线程、受支持模型启用（`microCompact.ts:276-286`）。
- 旧版（基于阈值的）microcompact 路径已被删除；外部构建/子 agent 在此阶段**不做任何压缩**，交给 autocompact（`microCompact.ts:288-292`）。

对 Go 实现的含义：microcompact 不是「把上下文压到阈值以下」的主力，它只是缓存失效时的减负；真正兜底的是 autocompact。

### 6.4 autocompact 生成什么、保留什么

`autoCompactIfNeeded` 先尝试 session memory 压缩，失败/不可用才走完整摘要压缩（`autoCompact.ts:287-321`）。

完整压缩 `compactConversation`（`compact.ts:387-748`）：

1. 跑 `PreCompact` hooks，hook 可追加自定义指令（`compact.ts:411-424`）。
2. 用一个「压缩专用 user 消息」请求模型生成摘要；若摘要请求本身 413，就截断头部重试（`compact.ts:440-491`）。
3. **清空 `readFileState` 和 `loadedNestedMemoryPaths`**（`compact.ts:517-522`）。
4. 重新注入最近文件内容：最多 5 个文件、每个 ≤5000 token、总预算 50000 token（常量 `compact.ts:122-124`；实现 `1415-1464`）。
5. 追加附件：异步 agent 状态、plan 文件、plan mode 指令、已调用 Skill 内容、deferred tools 增量、agent 列表、MCP 指令（`compact.ts:532-585`）。
6. 跑 `SessionStart` hooks，结果作为 hookResults 保留（`compact.ts:587-594`）。
7. 生成 compact boundary，带 `trigger`（auto/manual）、`preTokens`、`logicalParentUuid`（`compact.ts:596-611`；构造函数 `utils/messages.ts:4530-4554`）。
8. 摘要作为一条 `isCompactSummary: true` 的 user 消息（`compact.ts:614-624`）。

最终顺序固定为：

```text
boundaryMarker → summaryMessages → messagesToKeep → attachments → hookResults
```

`buildPostCompactMessages`，`compact.ts:325-338`。

两点关键差异：

- **完整压缩（autocompact / 手动 /compact）没有 `messagesToKeep`**：`compactConversation` 的返回里不含该字段（`compact.ts:738-748`），即压缩后只剩摘要 + 附件，不保留最近消息。
- **session memory 压缩保留最近消息**：从「最后一条已总结消息」之后开始，向前扩展到至少 `minTokens=10000`、至少 `minTextBlockMessages=5` 条含文本消息，硬上限 `maxTokens=40000`，并且不切断 tool_use/tool_result 对（`sessionMemoryCompact.ts:57-61`、`324-397`、`487-497`）。session memory 压缩的摘要来自已有的 session memory 文件，不额外调用模型（`sessionMemoryCompact.ts:459-482`、`498-500` 明确写「SM-compact has no compact-API-call」）。

### 6.5 压缩结果如何回注循环

- 压缩成功：`buildPostCompactMessages` 的每条消息立即 `yield`，然后 `messagesForQuery = postCompactMessages`，本轮继续用压缩后的上下文请求模型（`query.ts:528-535`）。
- 压缩后 `autoCompactTracking` 重置 `turnId/turnCounter/consecutiveFailures`（`query.ts:517-526`）。
- `QueryEngine` 收到 `compact_boundary` 后会把内存消息数组截断到边界，释放压缩前消息（`QueryEngine.ts:916-942`）。

### 6.6 上下文组装本身

`src/context.ts` 只负责「静态上下文」：

- `getSystemContext()`：git 分支、主分支、`git status`、最近 5 条提交；status 超过 2000 字符截断（`context.ts:20`、`36-111`、`116-150`）。
- `getUserContext()`：`CLAUDE.md` 内容 + 当前日期（`context.ts:155-189`）。

注入方式：

- `systemContext` 追加到 system prompt 末尾，格式 `key: value`（`utils/api.ts:437-447`；调用点 `query.ts:449-451`）；
- `userContext` 作为一条 meta user 消息（`<system-reminder>` 包裹）**放在消息数组最前面**（`utils/api.ts:449-474`；调用点 `query.ts:660`）。

注意这两者都是每次请求前拼进去的，不写进持久化历史（`prependUserContext` 只在 `messagesForQuery` 上操作，`query.ts:660`）。

---

## 7. 会话如何持久化与恢复

### 7.1 存储形态：追加式 JSONL + parentUuid 链

- 每个会话一个文件：`<projectDir>/<sessionId>.jsonl`（`sessionStorage.ts:202-205`）。
- 每条消息一行，`appendFileSync` 追加，权限 `0o600`（`sessionStorage.ts:2572-2584`）。
- 每条记录除消息本体外还带：`parentUuid`、`logicalParentUuid`、`isSidechain`、`sessionId`、`cwd`、`version`、`gitBranch`、`slug`、`userType`、`entrypoint`（`sessionStorage.ts:1039-1064`）。
- `parentUuid` 默认指向上一条链参与者；**tool_result 消息覆盖为 `sourceToolAssistantUUID`**（`sessionStorage.ts:1028-1037`）。
- **compact boundary 的 `parentUuid` 强制为 `null`**，原父节点存进 `logicalParentUuid`（`sessionStorage.ts:1040-1041`）。这是让 `--resume` 在边界处截断、不加载压缩前历史的关键。

### 7.2 写入调度

- 写请求进每个文件一条队列，`FLUSH_INTERVAL_MS = 100` 毫秒批量 drain（`sessionStorage.ts:567`、`606-632`）。
- 单批上限 `MAX_CHUNK_BYTES = 100MB`（`sessionStorage.ts:568`），超过就分段 flush（`645-673`）。
- 测试/桌面场景有显式 `flushSessionStorage()`；`QueryEngine` 在紧凑模式或 cowork 模式下会在关键点 await flush（`QueryEngine.ts:456-462`、`1073-1080`）。

### 7.3 写什么、何时写

`QueryEngine.submitMessage`：

- 用户消息在进入 query 循环**之前**就落盘。理由写在注释里：如果进程在 API 返回前被杀，transcript 至少能 resume（`QueryEngine.ts:436-463`）。
- 循环中按消息类型写：
  - assistant → fire-and-forget（避免阻塞 generator；写队列保序）`QueryEngine.ts:717-731`；
  - user / compact boundary → await（`730`）；
  - progress / attachment → 也 inline 记录，用于 resume 时的 chain 正确性（`QueryEngine.ts:771-783`、`829-835`）。
- `recordTranscript` 会先按 uuid 去重，只追加新消息；已记录的连续前缀会作为下一个 `parentUuid`（`sessionStorage.ts:1408-1449`）。
- 子 agent 走独立 sidechain 文件：`.../subagents/agent-<id>.jsonl`（`sessionStorage.ts:236-257`、`1451-1462`）。

### 7.4 恢复

`loadConversationForResume`（`conversationRecovery.ts:456-...`）：

1. 读全部行 → `getLastSessionLog(sessionId)`（`conversationRecovery.ts:522`）。
2. `getLastSessionLog` 找到**最新一条非 sidechain 消息**，从它出发 `buildConversationChain` 回溯到根（`sessionStorage.ts:3899-3904`）。
3. `buildConversationChain` 沿 `parentUuid` 单链回溯并反转；检测到环就截断（`sessionStorage.ts:2069-2094`）。
4. 并行 tool_use 会产生 DAG（多个 assistant 同 message.id），单链回溯会丢兄弟节点，因此有 `recoverOrphanedParallelToolResults` 后处理把同 message.id 的兄弟 assistant 和 tool_result 补回（`sessionStorage.ts:2096-2138`）。
5. 反序列化时做 API 合法性清洗（`conversationRecovery.ts:164-224`）：
   - 删除未配对的 tool_use assistant 消息（`186-189`）；
   - 删除只有 thinking 的孤儿 assistant 消息（`191-196`）；
   - 删除只有空白的 assistant 消息（`198-202`）；
   - 如果检测到「turn 中断」，追加一条 meta 的 `Continue from where you left off.`（`209-224`）。

### 7.5 transcript 读取上限

单次读取上限 50MB（`MAX_TRANSCRIPT_READ_BYTES`，`sessionStorage.ts:227-229`）。这是明确的产品限制，Go 实现若照搬需注意长会话。

---

## 8. 内置工具契约：Read 与 Bash

### 8.1 通用 Tool 契约

`Tool` 类型要求（`Tool.ts:362-...`）：

| 成员 | 作用 | 位置 |
|---|---|---|
| `name` / `aliases` | 查找与兼容旧名 | `Tool.ts:348-360`、`456` |
| `inputSchema` | Zod schema，执行前 `safeParse` | `Tool.ts:394`；使用点 `toolExecution.ts:614-616` |
| `description(input, opts)` | 面向模型的描述 | `Tool.ts:386-393` |
| `prompt(opts)` | 工具说明文本 | `Tool.ts:518-523` |
| `call(input, ctx, canUseTool, parentMessage, onProgress)` | 实际执行 | `Tool.ts:379-385` |
| `isConcurrencySafe(input)` | 并发分批依据 | `Tool.ts:402`；使用点 `toolOrchestration.ts:98-108` |
| `isReadOnly(input)` | 只读语义 | `Tool.ts:404` |
| `validateInput` | 执行前的业务校验（不弹 UI） | `Tool.ts:489-492` |
| `checkPermissions` | 工具级权限判定，仅在 validate 通过后调用 | `Tool.ts:494-503` |
| `maxResultSizeChars` | 超限结果落盘阈值 | `Tool.ts:457-466` |
| `mapToolResultToToolResultBlockParam` | 结果 → API block | 使用点 `toolExecution.ts:1292-1301` |

`buildTool` 填充的默认值刻意「fail-closed」：

```text
isConcurrencySafe → false（假定不安全）
isReadOnly        → false（假定会写）
checkPermissions  → allow（交给通用权限系统再判）
```

`Tool.ts:743-769`；实现 `783-792`。

### 8.2 `FileReadTool`

- 名字 `Read`，`maxResultSizeChars: Infinity`（永不落盘，避免 Read→文件→Read 循环），`strict: true`（`FileReadTool.ts:337-343`）。
- `isConcurrencySafe() → true`、`isReadOnly() → true`（`373-378`）。
- 输入：`file_path`（绝对路径）、`offset`（1-based，非负）、`limit`（正整数）、`pages`（PDF 页码范围）（`FileReadTool.ts:227-243`）。
- 两个上限（`limits.ts:1-18`）：
  - `maxSizeBytes = 256KB`：读之前按**整个文件大小**拦截；
  - `maxTokens = 25000`：读之后按实际输出 token 拦截。
  - 环境变量 `CLAUDE_CODE_FILE_READ_MAX_OUTPUT_TOKENS` 优先（`limits.ts:24-33`、`67-74`）。
- 权限：`checkReadPermissionForTool`（`FileReadTool.ts:398-405`）；`validateInput` 里还会直接检查 deny 规则（`FileReadTool.ts:442-457`）。
- 结果去重：同一路径、同 `offset`/`limit`、且文件 mtime 未变化时，不重复读内容，直接返回 `type: 'file_unchanged'` 的结果（`FileReadTool.ts:536-573`）；有 `tengu_read_dedup_killswitch` 开关可关闭（`536-539`）。
- `backfillObservableInput` 会把 `file_path` 展开成绝对路径，防止 hook allowlist 被 `~`/相对路径绕过（`FileReadTool.ts:388-394`）。

### 8.3 `BashTool`

- `maxResultSizeChars: 30_000`：超过 30K 字符的结果落盘，只给模型预览（`BashTool.tsx:423-424`）。
- `isReadOnly(input)` 基于命令静态分析 `checkReadOnlyConstraints`（`BashTool.tsx:437-441`）。
- `isConcurrencySafe(input)` 直接复用 `isReadOnly`（`BashTool.tsx:434-436`）。
- `validateInput` 会拦截阻塞式 sleep 模式，要求改用后台任务（`BashTool.tsx:524-538`）。
- 权限：交给 `bashToolHasPermission`（`BashTool.tsx:539-541`），支持命令前缀规则、沙箱、分类器等。
- 超时：默认 2 分钟、最大 10 分钟，可用 `BASH_DEFAULT_TIMEOUT_MS` / `BASH_MAX_TIMEOUT_MS` 覆盖（`utils/timeouts.ts:2-3`、`12-39`；使用点 `BashTool.tsx:860`）。
- 进度：`call` 里消费 shell 异步生成器，每个进度块调用一次 `onProgress`，带上 `output/fullOutput/elapsedTimeSeconds/totalLines/totalBytes/taskId/timeoutMs`（`BashTool.tsx:646-679`）。

---

## 9. 模型请求、重试与错误

### 9.1 请求与流

- 流式请求入口 `queryModelWithStreaming`（`claude.ts:752`）。
- SDK 的自动重试被关闭（`maxRetries: 0`），改由自研 `withRetry` 控制（`claude.ts:1778-1785`）。
- 直接用 raw stream 而不是 `BetaMessageStream`，避免每个 `input_json_delta` 都做 partial JSON 解析（`claude.ts:1818-1821`）。
- 流空闲看门狗：超过 `STREAM_IDLE_TIMEOUT_MS` 没有 chunk 就 abort，并回退到非流式请求（`claude.ts:1868-...`、`2305-2335`）。

### 9.2 重试策略

`withRetry`（`withRetry.ts:170-...`）：

- 默认最多 10 次重试（`DEFAULT_MAX_RETRIES = 10`，`withRetry.ts:52`）。
- 529（overloaded）单独计数：达到 `MAX_529_RETRIES = 3` 且配置了 `fallbackModel` 时抛 `FallbackTriggeredError`；没有 fallback 时，外部用户直接失败，内部用户继续走普通重试（`withRetry.ts:54`、`326-365`）。计数只对 `FALLBACK_FOR_ALL_PRIMARY_MODELS` 或「非订阅用户 + 非自定义 Opus 模型」生效（`withRetry.ts:327-333`）。
- 529 只对「用户在等结果」的前台 query source 重试；后台任务（摘要、标题、建议、分类器）立即放弃，避免容量雪崩时放大请求（`withRetry.ts:57-89`、`316-325`）。
- 退避：`min(500ms * 2^(attempt-1), 32000ms)` + 0~25% 抖动；优先使用 `retry-after` 头（`withRetry.ts:530-548`）。
- 是否重试：`x-should-retry` 头优先；408/409/429 可重试；401/403 等按头处理（`withRetry.ts:696-...`）。
- 重试过程中会 `yield createSystemAPIErrorMessage(...)`，前端据此显示重试状态（`withRetry.ts:506-510`；`QueryEngine.ts:943-955` 转成 `api_retry` 事件）。

### 9.3 模型 fallback

- 连续 529 达到上限且配置了 `fallbackModel` → 抛 `FallbackTriggeredError`（`withRetry.ts:160-168`、`335-351`）。
- `query.ts` 捕获后：为已产出的 tool_use 补错误结果、清空 assistant/toolResults/toolUseBlocks、丢弃 StreamingToolExecutor、切换 `currentModel`、重新请求（`query.ts:893-951`）。
- 切换时会 strip thinking signature block，避免签名与模型绑定导致 400（`query.ts:924-929`）。

### 9.4 错误如何不打断循环

- 模型流里的可恢复错误可以先「暂扣」，等恢复逻辑尝试完再决定是否展示（`query.ts:788-822`）。
- 工具错误全部变成 tool_result（见第 3 节）。
- `max_output_tokens` / `model_context_window_exceeded` 都转成 `apiError: 'max_output_tokens'` 的合成 assistant 消息，复用同一套续写恢复（`claude.ts:2266-2292`）。

---

## 10. 与 Go 实现的模块对应

| 参考实现 | Go 目标包（按 `AGENTS.md`） | 需要保留的核心语义 |
|---|---|---|
| `QueryEngine.ts` | `internal/agent` | 一会话一实例；turn 级状态；usage/denial 累计；每 turn 先落盘 user 消息 |
| `query.ts` | `internal/agent` | 单循环状态机；`tool_use` 作为唯一推进信号；退出原因枚举 |
| `query/stopHooks.ts` | `internal/hook` + `internal/agent` | Stop hook 可阻止继续或注入 blocking 错误 |
| `query/tokenBudget.ts` | `internal/agent` | 90% / 3 次 / 500 token 的续跑判定 |
| `Tool.ts` / `tools.ts` | `internal/tool` | 契约字段齐全；默认值 fail-closed |
| `toolOrchestration.ts` | `internal/tool` | 按 `isConcurrencySafe` 分批，上限 10，写工具串行 |
| `toolExecution.ts` | `internal/tool` | 校验→hook→权限→执行；所有失败路径生成 tool_result |
| `useCanUseTool.tsx` / `permissions.ts` | `internal/permission` | 模式+规则+工具级判定；`ask` 阻塞等待前端；`dontAsk` 最后转 deny |
| `services/api/*` | `internal/llm` | 原始 SSE 增量转发；自研重试；fallback 模型 |
| `services/compact/*` | `internal/context` | 五段流水线；阈值公式；压缩后保留 boundary+summary+attachments |
| `sessionStorage.ts` | `internal/session` | JSONL 追加；parentUuid 链；compact boundary 断链；异步批量 flush |
| `context.ts` | `internal/context` | git 状态/CLAUDE.md/日期注入；systemContext 拼系统提示，userContext 拼首条 user 消息 |

---

## 11. 无法确认 / 不存在的部分

- `src/types/message.ts` 不存在：`Message` / `StreamEvent` / `TombstoneMessage` 的完整字段定义无法确认（import 见 `query.ts:30-39`）。
- `src/query/transitions.ts` 不存在：`Terminal` / `Continue` 的联合类型无法确认（import 见 `query.ts:104`）。本文的退出原因表是从 `return` 字面量汇总的。
- `src/services/compact/reactiveCompact.ts` 不存在：reactive compact 的内部策略无法确认，只能确认调用契约（`query.ts:1119-1135`）。
- `src/services/contextCollapse/` 为空目录：折叠算法无法确认，只能确认调用点与「先折叠再 autocompact」的顺序约束（`query.ts:428-447`）。
- 未在本项目源码中找到「桌面端审批 UI」的实现（参考实现是 Ink/REPL 终端 UI，`interactiveHandler.ts` 推进的是终端确认队列）；`anc-dsk` 的 Wails 事件映射需要自行设计，参考实现只能提供「ask 必须阻塞到用户回调」这一语义。
- `src/services/compact/microCompact.ts` 明确写明旧版阈值型 microcompact 已被删除（`microCompact.ts:288-292`），因此「microcompact 按 token 阈值触发」在本参考版本中**不成立**。
