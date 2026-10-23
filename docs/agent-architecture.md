# b-desktop Agent 核心运行时设计

> 范围：`internal/llm`、`internal/tool`、`internal/tool/builtin`、`internal/permission`、`internal/context`、`internal/session`、`internal/agent`、`internal/app`。
> 依据：`AGENTS.md`（行为约束与包划分）、`internal/app/app.go`（现有绑定形态）、`docs/research/{cc,codex,zed,zcode}.md`（参考实现的 file:line 结论）。
> 约束：只用标准库；不新增 `go.mod` 依赖；导出标识符带 Go doc 注释；内部方法首参 `context.Context`；错误用 `%w` 包装；不引入包级可变全局状态。
> 第 12 节记录本轮评审的逐条处置；标注「已实测」的结论都附了实际执行的命令与输出。

---

## 0. 结论先行

- 一个会话一个 `agent.Engine`；`Engine.Run` 是唯一入口，内部是**单层状态机循环**，不是递归（cc.md:77 说明参考实现的 `query_recursive_call` 实际是 `continue`）。
- **唯一推进信号是工具调用**：本轮 assistant 消息里出现 `tool_use` 就执行并把 `tool_result` 回灌，没有才结束（cc.md:54-77、zed.md:89-99）。provider 的 `stop_reason` 不参与控制流。
- **每个 `tool_use` 有且只有一个 `tool_result`**：未知工具、参数非法、权限拒绝、用户取消、ctx 取消、panic 全部在 `tool.Call` 与 `agent` 兜底处转成结构化结果（cc.md:139-152、zed.md:155、codex.md:148-155）。
- **流式事件逐条转发**，粒度就是 provider delta（cc.md:258-299、codex.md:345-353、zcode.md:150-154）；后端不聚合文本，前端自行合帧。
- **事件只有一个发射者**：所有 `agent.Event` 只从 turn goroutine 调用一次 `EventFunc`；工具进度通过内部 channel 汇入该 goroutine 后再转发（§6.5）。这是对评审第 7 条的修正，旧稿「循环 goroutine 同步调用」与「工具 goroutine 直接调 emit」并存的说法已删除。
- **权限检查在工具执行之前**，判定只有 `allow/ask/deny`；`ask` 阻塞等待前端应答，`ctx` 取消按取消处理，**禁止默认放行**（cc.md:232-250、codex.md:203）。
- **压缩在每次模型调用前执行**：`microCompact → autoCompact`，阈值与保留策略见 §7（cc.md:305-345、zcode.md:173-206）。
- 会话是**追加式 JSONL**，压缩写边界记录，恢复时「边界摘要 + 边界之后的记录」重放（cc.md:400-442、codex.md:288-324）。

依赖方向（无环）：

```text
llm        → stdlib
tool       → stdlib
permission → tool          // 仅用 tool.MatchPath，见 §5.5
session    → llm
context    → llm, tool
builtin    → tool
agent      → llm, tool, permission, session, context
app        → agent, llm, tool, tool/builtin, permission, session, context
```

`permission` 依赖 `tool` 是为了消除路径匹配与路径解析的第二份实现（评审第 26 条）；`tool` 不反向依赖，仍然无环。`internal/context` 与标准库 `context` 同名，引用方统一别名 `sysctx`（agent/app 中为 `sysctx "b-desktop/internal/context"`）。

---

## 1. 包与文件清单

### 1.1 internal/llm —— 模型传输层

| 文件 | 职责（一句话） |
|---|---|
| `types.go` | 定义 `Role`、`BlockType`、`ContentBlock`、`Message`、`Usage`、`ToolSpec`、`Request`、`StopReason`、`ModelInfo` 与构造函数。 |
| `event.go` | 定义 `StreamEvent`、`EventType` 常量、`StreamError`、`ErrorKind`、`RetryInfo`。 |
| `provider.go` | 定义 `Provider`/`Stream` 接口、`HTTPOptions`、默认常量、错误哨兵、`ModelInfoOf`。 |
| `sse.go` | 通用 SSE 读取器：`http.Client` 发请求、逐行解码 `data:`、首字节超时、空闲超时、读取 goroutine 生命周期。 |
| `retry.go` | 可重试判定、指数退避 + 抖动、`Retry-After` 解析、ctx 感知等待。 |
| `anthropic.go` | Anthropic Messages SSE → `StreamEvent` 映射与请求体编码。 |
| `openai.go` | OpenAI Chat Completions SSE → `StreamEvent` 映射与请求体编码（含块索引分配与 `tool_use_stop` 收尾）。 |
| `llm_test.go` | `httptest` 假服务器验证两种协议、重试、超时、ctx 取消、goroutine 退出。 |
| `llmtest/provider.go` | 脚本化 fake `Provider`，供 `agent`/`context` 测试使用（仅测试代码引用）。 |

### 1.2 internal/tool —— 工具契约与调度

| 文件 | 职责（一句话） |
|---|---|
| `tool.go` | `Tool` 接口、`Result`、`Progress`、`Spec`、`PathAware`、`DecodeInput`、`Call`（错误/panic/取消兜底）。 |
| `registry.go` | `Registry`：重名检测、查找、导出 `[]Spec`。 |
| `group.go` | `Group`：按「并发安全 + 允许执行」把同一轮调用切成批次。 |
| `match.go` | `MatchPath`：支持 `**` 的递归路径匹配，`builtin` 与 `permission` 共用。 |
| `tool_test.go` | 契约兜底与 panic 转结果测试。 |
| `group_test.go` | 分批规则与上限测试。 |
| `match_test.go` | `**`、`*`、`?`、字符类、非法模式的表驱动测试。 |

### 1.3 internal/tool/builtin —— 内置工具

| 文件 | 职责（一句话） |
|---|---|
| `paths.go` | 路径归一化、符号链接解析与越界检查（`Resolve(root, p)`）。 |
| `read.go` | `Read`：带行号读文件，大小与 token 上限。 |
| `write.go` | `Write`：原子写（临时文件 + rename），自动建目录。 |
| `edit.go` | `Edit`：精确字符串替换，唯一匹配校验。 |
| `glob.go` | `Glob`：`WalkDir` + `tool.MatchPath`，按 mtime 排序。 |
| `grep.go` | `Grep`：`regexp` 逐文件搜索，跳过二进制与大目录。 |
| `bash.go` | `Bash`：执行 shell，超时、进程组终止与输出截断。 |
| `proc_unix.go` | Unix 进程组创建与整组终止（`Setpgid` + `kill(-pgid)`）。 |
| `proc_windows.go` | Windows 进程组创建与整组终止（`CREATE_NEW_PROCESS_GROUP` + `taskkill /T /F`）。 |
| `todo.go` | `TodoWrite`：会话内待办列表，渲染成文本回灌。 |
| `builtin_test.go` | 在 `t.TempDir()` 内验证 7 个工具的正常与错误路径。 |

### 1.4 internal/permission —— 权限判定

| 文件 | 职责（一句话） |
|---|---|
| `mode.go` | 五档 `Mode` 枚举、`ParseMode`、各模式的默认策略。 |
| `rule.go` | `Rule`/`Effect`/`Source`/`MatchMode` 与匹配逻辑（委托 `tool.MatchPath`）。 |
| `engine.go` | `Engine`：判定优先级、模式叠加、会话级规则增删、`Check`。 |
| `broker.go` | `Broker`：无死锁的异步审批通道（容量 1、发送不阻塞、ctx 取消可清理）。 |
| `safety.go` | 不可被覆盖的内置硬拒绝规则。 |
| `permission_test.go` | 模式 × 规则矩阵、审批竞态与取消测试。 |

### 1.5 internal/session —— 会话持久化

| 文件 | 职责（一句话） |
|---|---|
| `record.go` | `Record`/`RecordType`/`Compact`/`TurnEnd`/`Meta` 与单行 JSONL 编解码。 |
| `store.go` | `Store` 接口、`Info`、测试用 `MemStore`、`ValidateID`。 |
| `file.go` | `FileStore`：每会话一个 `.jsonl`，0600 权限、同步追加、`Flush`、`Delete`、`Close`。 |
| `replay.go` | `Replay`：定位最近压缩边界、注入摘要、按 tool_use 顺序重建结果消息。 |
| `session_test.go` | 追加/重放/边界/中断/并发写/非法 ID 测试。 |

### 1.6 internal/context —— 系统提示与压缩

| 文件 | 职责（一句话） |
|---|---|
| `system.go` | `Builder`：环境信息、`AGENTS.md`/`CLAUDE.md`、git 状态、工具清单、日期。 |
| `tokens.go` | token 估算（中英文分别按字符类折算）。 |
| `budget.go` | `Budget`：有效窗口、自动压缩阈值（含下限钳制）、阻塞线、状态判定。 |
| `compact.go` | `Compactor`：`BeforeCall`、`MicroCompact`、`AutoCompact`、摘要提示词、进度回调。 |
| `context_test.go` | 阈值数学（含小窗口）、微压缩替换、自动压缩保留策略、失败熔断。 |

### 1.7 internal/agent —— Agent 运行时

| 文件 | 职责（一句话） |
|---|---|
| `config.go` | `Config` 与 `Validate`、`Input`、`Result`/`StopReason`、错误哨兵。 |
| `event.go` | `Event`、`EventType`、`EventFunc` 与前端视图结构。 |
| `engine.go` | `Engine` 生命周期：构造、恢复、`Run` 入口、`Cancel`、并发与历史快照。 |
| `loop.go` | 单轮状态机：压缩 → 请求 → 判定 → 工具 → 回灌 → 退出，含 panic 兜底。 |
| `stream.go` | 消费 `llm.Stream`，按 `(BlockIndex, Kind)` 累积 assistant 消息并逐条转发增量事件。 |
| `tools.go` | 权限预检、分批执行、进度汇入、`tool_result` 兜底与持久化。 |
| `approval.go` | `ask` 流程：`Broker` 等待、事件推送、always 规则推导与写回。 |
| `prompt.go` | 请求组装：system + 工具规格 + 消息历史。 |
| `agent_test.go` | 循环、快照并发、取消、最大轮次、panic 恢复。 |
| `tools_test.go` | 工具分发、配对完整性、拒绝与异常、进度实时性。 |
| `approval_test.go` | ask 允许/拒绝/always/取消四条路径。 |

### 1.8 internal/app —— 绑定与装配

| 文件 | 职责（一句话） |
|---|---|
| `app.go` | `App` 结构、Wails 生命周期、会话运行时注册表、`Shutdown` 清理（改造现有文件，保留 `Greet`）。 |
| `config.go` | 从环境变量读取 provider/模型/权限模式/数据目录/工作目录。 |
| `wiring.go` | 按会话装配 `llm.Provider`、`tool.Registry`、`permission.Engine`、`session.Store`、`sysctx.Builder/Compactor`、`agent.Engine`。 |
| `bindings_agent.go` | `StartTurn`、`CancelTurn`、`GetHistory`、`ResumeSession`、`CloseSession`、`DeleteSession`。 |
| `bindings_permission.go` | `RespondPermission`、`SetPermissionMode`、`ListRules`、`RemoveRule`、`GetPendingApprovals`。 |
| `bindings_session.go` | `ListSessions`、`LoadSession`、`GetConfig`。 |
| `events.go` | `Emitter`（全局单实例、互斥 + 自增 `seq`）与可注入的 `Sender`。 |
| `dto.go` | 前端 DTO 与内部类型的转换。 |
| `app_test.go` | DTO 转换、事件串行化与 seq、装配冒烟（fake Provider + fake Sender）。 |

### 1.9 文件 → 实现者归属表

7 路并行流（W1–W7）实现基础模块，W8 为装配层，等 W1–W7 编译通过后落地；契约在本文件中冻结，任何跨流签名变更须先改本文档。

| 文件 | 归属 |
|---|---|
| `internal/llm/types.go` | W1 模型层 |
| `internal/llm/event.go` | W1 模型层 |
| `internal/llm/provider.go` | W1 模型层 |
| `internal/llm/sse.go` | W1 模型层 |
| `internal/llm/retry.go` | W1 模型层 |
| `internal/llm/anthropic.go` | W1 模型层 |
| `internal/llm/openai.go` | W1 模型层 |
| `internal/llm/llm_test.go` | W1 模型层 |
| `internal/llm/llmtest/provider.go` | W1 模型层（契约见 §9.3，供 W6/W7 测试） |
| `internal/tool/tool.go` | W2 工具层 |
| `internal/tool/registry.go` | W2 工具层 |
| `internal/tool/group.go` | W2 工具层 |
| `internal/tool/match.go` | W2 工具层 |
| `internal/tool/tool_test.go` | W2 工具层 |
| `internal/tool/group_test.go` | W2 工具层 |
| `internal/tool/match_test.go` | W2 工具层 |
| `internal/tool/builtin/paths.go` | W3 内置工具层 |
| `internal/tool/builtin/read.go` | W3 内置工具层 |
| `internal/tool/builtin/write.go` | W3 内置工具层 |
| `internal/tool/builtin/edit.go` | W3 内置工具层 |
| `internal/tool/builtin/glob.go` | W3 内置工具层 |
| `internal/tool/builtin/grep.go` | W3 内置工具层 |
| `internal/tool/builtin/bash.go` | W3 内置工具层 |
| `internal/tool/builtin/proc_unix.go` | W3 内置工具层 |
| `internal/tool/builtin/proc_windows.go` | W3 内置工具层 |
| `internal/tool/builtin/todo.go` | W3 内置工具层 |
| `internal/tool/builtin/builtin_test.go` | W3 内置工具层 |
| `internal/permission/mode.go` | W4 权限层 |
| `internal/permission/rule.go` | W4 权限层 |
| `internal/permission/engine.go` | W4 权限层 |
| `internal/permission/broker.go` | W4 权限层 |
| `internal/permission/safety.go` | W4 权限层 |
| `internal/permission/permission_test.go` | W4 权限层 |
| `internal/session/record.go` | W5 会话层 |
| `internal/session/store.go` | W5 会话层 |
| `internal/session/file.go` | W5 会话层 |
| `internal/session/replay.go` | W5 会话层 |
| `internal/session/session_test.go` | W5 会话层 |
| `internal/context/system.go` | W6 上下文层 |
| `internal/context/tokens.go` | W6 上下文层 |
| `internal/context/budget.go` | W6 上下文层 |
| `internal/context/compact.go` | W6 上下文层 |
| `internal/context/context_test.go` | W6 上下文层 |
| `internal/agent/config.go` | W7 Agent 层 |
| `internal/agent/event.go` | W7 Agent 层 |
| `internal/agent/engine.go` | W7 Agent 层 |
| `internal/agent/loop.go` | W7 Agent 层 |
| `internal/agent/stream.go` | W7 Agent 层 |
| `internal/agent/tools.go` | W7 Agent 层 |
| `internal/agent/approval.go` | W7 Agent 层 |
| `internal/agent/prompt.go` | W7 Agent 层 |
| `internal/agent/agent_test.go` | W7 Agent 层 |
| `internal/agent/tools_test.go` | W7 Agent 层 |
| `internal/agent/approval_test.go` | W7 Agent 层 |
| `internal/app/app.go` | W8 装配层 |
| `internal/app/config.go` | W8 装配层 |
| `internal/app/wiring.go` | W8 装配层 |
| `internal/app/bindings_agent.go` | W8 装配层 |
| `internal/app/bindings_permission.go` | W8 装配层 |
| `internal/app/bindings_session.go` | W8 装配层 |
| `internal/app/events.go` | W8 装配层 |
| `internal/app/dto.go` | W8 装配层 |
| `internal/app/app_test.go` | W8 装配层 |
| `frontend/src/types/agent.ts` | W8 装配层（手工维护的事件/事件载荷类型，必须与 §3 事件表逐字段一致；不是 Wails 生成物） |
| `AGENTS.md` | 第 0b 步（契约同步：checkPermissions / 结果映射 / 本轮不做的扩展） |
| `docs/agent-architecture.md` | 契约冻结文档（任何跨流签名变更先改这里，再改代码） |

并行规则：

1. 每路只写自己名下的文件，禁止改动他人文件；需要对方新增导出能力时，先改本文档的契约段再实现。
2. W1 的 `llmtest` 与 W2 的 `tool.MatchPath` 契约先于依赖方落地（§9.3、§2.2 已给完整签名），W3/W4/W6/W7 不等待对方实现。
3. 编译验证顺序：W1/W2/W5 无内部依赖可先 `go build ./internal/...`；W3 依赖 W2；W4 依赖 W2（`tool.MatchPath`）；W6 依赖 W1+W2；W7 依赖全部；W8 最后。

---

## 2. 精确 Go 契约

以下签名即实现标准，字段名、JSON tag、方法集不得擅自变更。

### 2.1 internal/llm

```go
// Package llm 定义模型传输层的统一请求、流式事件与 Provider 接口。
// 该包不感知工具执行、权限与会话，只负责「把一次对话请求变成一串流事件」。
package llm

// Role 是消息角色。工具结果作为 RoleUser 消息回灌，与 Anthropic/OpenAI 的线格式一致。
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// BlockType 是内容块的判别字段。
type BlockType string

const (
	BlockText       BlockType = "text"
	BlockThinking   BlockType = "thinking"
	BlockToolUse    BlockType = "tool_use"
	BlockToolResult BlockType = "tool_result"
)

// ContentBlock 是 JSON 联合体：只使用与 Type 对应的字段，其余字段为零值。
//   - BlockText：Text
//   - BlockThinking：Thinking、Signature
//   - BlockToolUse：ToolUseID、ToolName、Input
//   - BlockToolResult：ToolUseID、Text、IsError
type ContentBlock struct {
	Type      BlockType       `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	ToolUseID string          `json:"toolUseId,omitempty"`
	ToolName  string          `json:"toolName,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	IsError   bool            `json:"isError,omitempty"`
}

// TextBlock 构造文本块。
func TextBlock(text string) ContentBlock

// ThinkingBlock 构造思考块。sig 为空表示该 provider 不返回签名。
func ThinkingBlock(text, sig string) ContentBlock

// ToolUseBlock 构造工具调用块。input 必须是合法 JSON 对象。
func ToolUseBlock(id, name string, input json.RawMessage) ContentBlock

// ToolResultBlock 构造工具结果块；content 为空时写入占位文本。
func ToolResultBlock(id string, isError bool, content string) ContentBlock

// Clone 返回深拷贝（Input 与 Blocks 都复制），用于历史快照。
func (m Message) Clone() Message

// StopReason 是模型停止原因，仅用于观测，不参与循环控制。
type StopReason string

const (
	StopEndTurn   StopReason = "end_turn"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
	StopRefusal   StopReason = "refusal"
	StopAborted   StopReason = "aborted"
)

// Usage 是一次模型调用的 token 统计。
type Usage struct {
	InputTokens              int `json:"inputTokens"`
	OutputTokens             int `json:"outputTokens"`
	CacheReadInputTokens     int `json:"cacheReadInputTokens,omitempty"`
	CacheCreationInputTokens int `json:"cacheCreationInputTokens,omitempty"`
}

// Total 返回输入与输出 token 之和。
func (u Usage) Total() int

// Merge 合并同一消息在不同 SSE 事件里分段上报的 usage：other 中非零的字段
// 覆盖 u 的对应字段（Anthropic 的输入 token 在 message_start、累计输出在 message_delta）。
// Merge 只能用于「同一字段可能被重复上报」的场景，不能用于多步累加。
func (u Usage) Merge(other Usage) Usage

// Sum 逐字段相加，用于把多个模型步的 usage 累加成一整轮或整个会话的用量。
// 与 Merge 的语义区别（评审第 2 轮第 4 条）：Merge 取最新值，Sum 求总量。
func (u Usage) Sum(other Usage) Usage

// Message 是一条对话消息。一条 assistant 消息可包含多个块（文本 + 多个 tool_use）。
// Time 由 agent 在消息产生时写入，并随 JSONL 持久化；Replay 会把它填回。
type Message struct {
	ID         string         `json:"id"`
	Role       Role           `json:"role"`
	Blocks     []ContentBlock `json:"blocks"`
	Model      string         `json:"model,omitempty"`
	StopReason StopReason     `json:"stopReason,omitempty"`
	Usage      *Usage         `json:"usage,omitempty"`
	Time       time.Time      `json:"time,omitempty"`
}

// HasToolUse 报告消息中是否至少有一个 tool_use 块。
func (m Message) HasToolUse() bool

// ToolUses 返回消息中的全部 tool_use 块，保持原有顺序。
func (m Message) ToolUses() []ContentBlock

// ToolSpec 是暴露给模型的工具规格。
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Thinking 是扩展思考配置。nil 表示不启用。
// Anthropic 要求 BudgetTokens >= 1024 且 < 本次请求的 MaxTokens，且与 Temperature 互斥，
// 违反时适配器在发请求前返回 ErrInvalidRequest（包装 *StreamError）。
type Thinking struct {
	Enabled      bool `json:"enabled"`
	BudgetTokens int  `json:"budgetTokens,omitempty"`
}

// Request 是一次模型调用。Messages 必须满足「assistant 的 tool_use 都有对应 tool_result」。
type Request struct {
	Model         string
	System        string
	Messages      []Message
	Tools         []ToolSpec
	MaxTokens     int
	Temperature   *float64
	Thinking      *Thinking
	StopSequences []string
	RequestID     string
}

// ModelInfo 是模型的静态能力与容量。
type ModelInfo struct {
	ID            string
	ContextWindow int
	MaxOutput     int
	SupportsTools bool
}

// ModelInfoOf 返回已知模型的容量。未知模型返回 200000 上下文 / 8192 输出，
// 且 SupportsTools 为 true：第三方端点多数支持工具，不能因为不认识模型名就静默关闭工具。
func ModelInfoOf(model string) ModelInfo
```

```go
// EventType 是流事件的判别字段。
type EventType string

const (
	EventMessageStart   EventType = "message_start"
	EventBlockStart     EventType = "block_start"
	EventTextDelta      EventType = "text_delta"
	EventThinkingDelta  EventType = "thinking_delta"
	EventSignature      EventType = "signature"
	EventToolUseStart   EventType = "tool_use_start"
	EventToolInputDelta EventType = "tool_input_delta"
	EventToolUseStop    EventType = "tool_use_stop"
	EventMessageDelta   EventType = "message_delta"
	EventMessageStop    EventType = "message_stop"
	EventPing           EventType = "ping"
	EventRetry          EventType = "retry"
	EventError          EventType = "error"
)

// StreamEvent 是 Provider 产出的最小事件单位，字段按 Type 解释。
//
// Index 的契约（评审第 2 轮第 2 条）：它是「assistant 消息内的块序号」，由适配器在块创建时
// 分配，同一消息内唯一且单调递增；不同类型绝不能复用同一个 Index。Anthropic 直接使用
// provider 的 content block index；OpenAI 必须自行分配（见 §4.2）。
//
// Kind 的契约（评审第 2 轮第 1 条）：EventBlockStart / EventTextDelta / EventThinkingDelta /
// EventSignature / EventToolInputDelta / EventToolUseStart / EventToolUseStop 这 7 类
// **块级事件必须携带 Kind**，取值就是该块的类型。agent 以 (Index, Kind) 为累加器键；
// Kind 为空的块级事件被视为适配器缺陷：agent 先按 Index 查已打开的块，查不到就丢弃该事件
// 并记录一条 error（不再猜测成文本或工具），避免参数 JSON 被写进幽灵块。
type StreamEvent struct {
	Type        EventType    `json:"type"`
	Index       int          `json:"index"`
	Kind        BlockType    `json:"kind"`
	MessageID   string       `json:"messageId,omitempty"`
	Text        string       `json:"text,omitempty"`
	Thinking    string       `json:"thinking,omitempty"`
	Signature   string       `json:"signature,omitempty"`
	ToolUseID   string       `json:"toolUseId,omitempty"`
	ToolName    string       `json:"toolName,omitempty"`
	PartialJSON string       `json:"partialJson,omitempty"`
	StopReason  StopReason   `json:"stopReason,omitempty"`
	Usage       *Usage       `json:"usage,omitempty"`
	Error       *StreamError `json:"error,omitempty"`
	Retry       *RetryInfo   `json:"retry,omitempty"`
}

// RetryInfo 描述一次即将发生的重试，用于前端展示。
type RetryInfo struct {
	Attempt int    `json:"attempt"`
	Max     int    `json:"max"`
	DelayMS int64  `json:"delayMs"`
	Reason  string `json:"reason"`
}

// ErrorKind 是错误分类，决定是否重试与前端文案。
type ErrorKind string

const (
	ErrKindAuth            ErrorKind = "auth"
	ErrKindRateLimit       ErrorKind = "rate_limit"
	ErrKindOverloaded      ErrorKind = "overloaded"
	ErrKindInvalidRequest  ErrorKind = "invalid_request"
	ErrKindContextOverflow ErrorKind = "context_overflow"
	ErrKindNetwork         ErrorKind = "network"
	ErrKindServer          ErrorKind = "server"
	ErrKindCanceled        ErrorKind = "canceled"
	ErrKindUnknown         ErrorKind = "unknown"
)

// StreamError 是流失败的唯一载体，实现 error 接口。
type StreamError struct {
	Kind      ErrorKind `json:"kind"`
	Message   string    `json:"message"`
	Status    int       `json:"status,omitempty"`
	Retryable bool      `json:"retryable"`
	Provider  string    `json:"provider,omitempty"`
	// cause 非导出，仅用于 Unwrap 保留 context.Canceled 等错误链。
	cause error
}

// Error 实现 error 接口。
func (e *StreamError) Error() string

// Unwrap 返回底层原因；ctx 取消时为 context.Canceled / context.DeadlineExceeded。
func (e *StreamError) Unwrap() error

// ContextOverflow 报告错误是否为上下文超窗。
func ContextOverflow(err error) bool
```

```go
// Provider 是一个模型供应商适配器。实现必须是并发安全的。
type Provider interface {
	// Name 返回供应商标识，例如 "anthropic"、"openai"。
	Name() string
	// Stream 发起一次流式请求。返回错误表示请求未能建立（此时已完成重试）；
	// 建立后的失败通过 Stream.Recv 返回，不会同时在两个地方报错。
	Stream(ctx context.Context, req Request) (Stream, error)
}

// Stream 是一次进行中的流。调用方必须 `defer stream.Close()`，并在 Recv 返回错误后立即 Close。
type Stream interface {
	// Recv 返回下一个事件。流正常结束返回 io.EOF；
	// 失败返回 *StreamError；ctx 取消返回包装了 context.Canceled 的错误。
	// 只允许一个 goroutine 调用；ctx 用于取消等待与空闲超时，不存入结构体。
	Recv(ctx context.Context) (StreamEvent, error)
	// Close 释放连接并等待读取 goroutine 退出，可重复调用且不会因事件通道写满而阻塞。
	Close() error
}

// HTTPOptions 是两种 HTTP Provider 的公共配置。
type HTTPOptions struct {
	BaseURL           string        // 为空时使用供应商默认地址
	APIKey            string
	HTTPClient        *http.Client  // nil 时使用内部默认客户端（Timeout 为 0，超时由 ctx 与 idle 控制）
	MaxRetries        int           // 默认 5
	BaseBackoff       time.Duration // 默认 500ms
	MaxBackoff        time.Duration // 默认 30s
	FirstTokenTimeout time.Duration // 默认 60s
	IdleTimeout       time.Duration // 默认 120s
	Headers           map[string]string
	UserAgent         string
}

// Anthropic 是 Anthropic Messages 协议的 Provider 实现，并发安全。
type Anthropic struct{ /* ... */ }

// OpenAI 是 OpenAI Chat Completions 协议的 Provider 实现，并发安全。
type OpenAI struct{ /* ... */ }

// NewAnthropic 创建 Anthropic Messages Provider。
func NewAnthropic(opts HTTPOptions) *Anthropic

// NewOpenAI 创建 OpenAI Chat Completions Provider。
func NewOpenAI(opts HTTPOptions) *OpenAI

// ErrMissingAPIKey 表示未配置密钥。
var ErrMissingAPIKey = errors.New("llm: missing api key")

// ErrInvalidRequest 表示请求参数不满足供应商约束（例如 thinking 预算非法）。
var ErrInvalidRequest = errors.New("llm: invalid request")
```

### 2.2 internal/tool

```go
// Package tool 定义工具契约、注册表、并发分批与执行兜底。
// 该包不依赖 llm：工具结果只有文本，由 agent 负责转换成 llm.ContentBlock。
package tool

// Progress 是一次工具进度回报。
type Progress struct {
	Text      string `json:"text,omitempty"`
	Bytes     int64  `json:"bytes,omitempty"`
	Total     int64  `json:"total,omitempty"`
	ElapsedMS int64  `json:"elapsedMs,omitempty"`
}

// ProgressFunc 报告进度。实现必须非阻塞；ctx 取消后应尽快返回。
type ProgressFunc func(Progress)

// Result 是工具执行结果。Content 回灌给模型，Display 给 UI（为空则复用 Content）。
type Result struct {
	Content   string            `json:"content"`
	Display   string            `json:"display,omitempty"`
	IsError   bool              `json:"isError,omitempty"`
	Truncated bool              `json:"truncated,omitempty"`
	Meta      map[string]string `json:"meta,omitempty"`
}

// ErrorResult 构造错误结果；格式串与 fmt.Errorf 相同。
func ErrorResult(format string, args ...any) Result

// Tool 是一个可被模型调用的工具。实现必须并发安全（同一实例可能并行执行）。
type Tool interface {
	// Name 返回工具名，必须全局唯一且与模型看到的一致。
	Name() string
	// Description 返回给模型看的用途说明。
	Description() string
	// InputSchema 返回 JSON Schema 文本。
	InputSchema() json.RawMessage
	// ReadOnly 报告该次调用是否只读；只读调用不会修改文件系统与进程状态。
	ReadOnly(input json.RawMessage) bool
	// ConcurrencySafe 报告该次调用能否与其他安全调用并行。
	ConcurrencySafe(input json.RawMessage) bool
	// Run 执行工具。返回 error 表示内部失败，由 Call 转成错误结果；ctx 取消应尽快返回。
	Run(ctx context.Context, input json.RawMessage, progress ProgressFunc) (Result, error)
}

// PathAware 由文件类工具实现，返回本次调用会触及的绝对路径（已解析符号链接）。
// agent 在权限预检前调用它，把结果填进 permission.Request.Paths，
// 避免在 permission 包里重复解析 file_path。
// 返回错误表示输入不可解析，agent 直接生成「invalid input」结果，不再执行工具。
type PathAware interface {
	AffectedPaths(input json.RawMessage) ([]string, error)
}

// AlwaysAllow 由「无文件、无进程副作用、策略上默认放行」的工具实现（例如 TodoWrite）。
// agent 据此设置 permission.Request.NoApproval，permission 不需要认识具体工具名。
type AlwaysAllow interface {
	AlwaysAllow() bool
}

// Spec 是工具的对外规格（agent 转换成 llm.ToolSpec）。
type Spec struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// SpecOf 返回工具的规格。
func SpecOf(t Tool) Spec

// DecodeInput 把模型给的 JSON 解到 dst，并包装错误以指出工具名与字段问题。
func DecodeInput(raw json.RawMessage, dst any) error

// Call 执行一次工具调用并保证永不返回 error：
// 输入非法、Run 返回错误、发生 panic、ctx 已取消，都会转换成 IsError 的 Result。
// ctx 已取消时不再调用 Run。
func Call(ctx context.Context, t Tool, input json.RawMessage, progress ProgressFunc) Result

// MatchPath 报告 path 是否匹配 glob 模式，支持 `**`（跨任意层目录，含零层）、
// `*`、`?` 与字符类。模式与路径都用 `/` 分隔；非法模式返回 false。
// 不使用 filepath.Match 作为整体匹配器（它把 `**` 当普通 `*`，且 `*` 不跨 `/`，见 §12）。
func MatchPath(pattern, path string) bool
```

```go
// Registry 保存本次会话可用的工具集合，读写安全。
type Registry struct {
	mu     sync.RWMutex
	byName map[string]Tool
	order  []string
}

// NewRegistry 创建注册表；重复工具名返回错误。
func NewRegistry(tools ...Tool) (*Registry, error)

// Register 注册一个工具；重名返回错误。
func (r *Registry) Register(t Tool) error

// Lookup 按名查找工具。
func (r *Registry) Lookup(name string) (Tool, bool)

// Specs 按注册顺序返回全部规格。
func (r *Registry) Specs() []Spec

// Names 按注册顺序返回工具名。
func (r *Registry) Names() []string
```

```go
// Call 是一次待执行的工具调用。Index 是它在**整条模型回复**中的全局位置
// （从 0 开始，跨批次不变）；并发执行与结果回填都以它为准，不能用批次内下标。
type Call struct {
	Index     int
	ToolUseID string
	Name      string
	Input     json.RawMessage
}

// Batch 是一批可执行调用。Concurrent 为 true 时批次内可并发执行。
type Batch struct {
	Calls      []Call
	Concurrent bool
}

// Group 按 ConcurrencySafe 与 allowed 把调用切成批次，并保证顺序：
// 连续的安全调用合并成一批（每批不超过 limit），不安全调用各自成批。
// allowed(i) 为 false 的调用（拒绝/待审批）单独成批且 Concurrent=false。
func Group(calls []Call, r *Registry, allowed func(Call) bool, limit int) []Batch
```

### 2.3 internal/permission

```go
// Package permission 负责权限模式、规则匹配与审批通道；
// 它不做 IO，也不依赖 Wails，判定结果是纯数据。
package permission

// Mode 是桌面端展示的五档权限模式。
type Mode string

const (
	ModeDefault           Mode = "default"
	ModeAcceptEdits       Mode = "acceptEdits"
	ModePlan              Mode = "plan"
	ModeBypassPermissions Mode = "bypassPermissions"
	ModeDontAsk           Mode = "dontAsk"
)

// ParseMode 解析模式字符串，未知值返回错误。
func ParseMode(s string) (Mode, error)

// Effect 是一次权限判定的结果。
type Effect string

const (
	EffectAllow Effect = "allow"
	EffectAsk   Effect = "ask"
	EffectDeny  Effect = "deny"
)

// Source 是规则来源，用于展示与优先级诊断。
type Source string

const (
	SourceBuiltin Source = "builtin"
	SourceUser    Source = "user"
	SourceProject Source = "project"
	SourceSession Source = "session"
)

// MatchMode 决定 Pattern 如何与输入比较。规则必须显式指定（评审第 2 轮第 5 条）：
// 引擎不认识工具名，无法替某条规则猜默认模式，因此 MatchMode 为空视为非法规则。
//   - MatchExact：整串相等；Bash 的 always 规则用它，复合命令也安全
//   - MatchPrefix：前缀匹配；仅当命令不含 shell 元字符时生效
//   - MatchGlob：路径 glob，委托 tool.MatchPath
//   - MatchJSON：输入 JSON 的子串匹配（仅用于自建规则，不作为 always 规则的默认值）
type MatchMode string

const (
	MatchExact  MatchMode = "exact"
	MatchPrefix MatchMode = "prefix"
	MatchGlob   MatchMode = "glob"
	MatchJSON   MatchMode = "json"
)

// Rule 是一条权限规则。Tool 支持 "*"；Pattern 为空表示该工具的所有调用
// （MatchGlob 除外：glob 规则的空 Pattern 非法）。
type Rule struct {
	ID        string    `json:"id"`
	Tool      string    `json:"tool"`
	Pattern   string    `json:"pattern,omitempty"`
	MatchMode MatchMode `json:"matchMode"` // 必填；空值在 NewEngine/AddRule 中被拒绝
	Effect    Effect    `json:"effect"`
	Source    Source    `json:"source"`
	Note      string    `json:"note,omitempty"`
}

// Request 是一次权限判定输入。
// Paths、WriteFiles 与 NoApproval 由 agent 通过 tool.PathAware / tool.AlwaysAllow 填充，
// permission 不解析工具参数里的路径字段，也不认识具体工具名。
type Request struct {
	SessionID  string
	ToolName   string
	Input      json.RawMessage
	ReadOnly   bool
	WriteFiles bool     // 本次调用会写文件（ReadOnly=false 且 Paths 非空）
	NoApproval bool     // 工具声明无副作用且策略上默认放行（tool.AlwaysAllow）
	Paths      []string // 绝对路径，已解析符号链接
	WorkDir    string
}

// Decision 是判定输出。EffectAllow/EffectDeny 可直接执行；
// EffectAsk 需要调用方走 Broker 审批，不得默认放行。
type Decision struct {
	Effect         Effect `json:"effect"`
	Reason         string `json:"reason"`
	RuleID         string `json:"ruleId,omitempty"`
	MatchedPattern string `json:"matchedPattern,omitempty"`
}

// Allowed 报告决策是否为 allow。
func (d Decision) Allowed() bool

// Config 是 Engine 的构造参数。
type Config struct {
	Mode    Mode
	WorkDir string
	Rules   []Rule
}

// Engine 持有会话级规则与模式，所有方法并发安全。
type Engine struct {
	mu      sync.RWMutex
	mode    Mode
	workDir string
	rules   []Rule
	seq     uint64
}

// NewEngine 创建引擎；Mode 非法、规则 MatchMode 为空、MatchGlob 规则 Pattern 为空时返回错误。
func NewEngine(cfg Config) (*Engine, error)

// Mode 返回当前模式。
func (e *Engine) Mode() Mode

// SetMode 切换模式。
func (e *Engine) SetMode(m Mode) error

// Rules 返回规则快照。
func (e *Engine) Rules() []Rule

// AddRule 追加规则并分配 ID（ID 为空时）；MatchMode 为空返回 ErrRuleInvalid。
func (e *Engine) AddRule(r Rule) (Rule, error)

// RemoveRule 删除规则，返回是否命中。
func (e *Engine) RemoveRule(id string) bool

// Check 按 §5.2 的顺序判定，不做任何阻塞等待，也不读取或修改审批状态。
func (e *Engine) Check(ctx context.Context, req Request) (Decision, error)

// ErrRuleInvalid 表示规则非法：MatchMode 为空、MatchGlob 无 Pattern、Effect 未知。
var ErrRuleInvalid = errors.New("permission: invalid rule")
```

```go
// Response 是前端对一次审批请求的答复。
// agent 根据 Option 自行推导要写回的规则（§5.6），因此这里没有 Rule 字段。
type Response struct {
	// Option 取值：allow_once、allow_always、deny_once、deny_always。
	Option string `json:"option"`
}

// 审批选项常量。
const (
	OptionAllowOnce   = "allow_once"
	OptionAllowAlways = "allow_always"
	OptionDenyOnce    = "deny_once"
	OptionDenyAlways  = "deny_always"
)

// ApprovalOption 是展示给用户的一个审批选项。
type ApprovalOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// ApprovalView 是一次待审批请求的完整快照。它同时用于事件推送与「重新枚举待审批」，
// 因此必须包含前端弹窗所需的全部字段（评审第 3 轮第 2 条）。
type ApprovalView struct {
	RequestID string           `json:"requestId"`
	SessionID string           `json:"sessionId"`
	ToolName  string           `json:"toolName"`
	Input     json.RawMessage  `json:"input"`
	Reason    string           `json:"reason"`
	Options   []ApprovalOption `json:"options"`
}

// pendingEntry 是一个待审批项。ch 容量固定为 1，且每个 entry 最多投递一次。
type pendingEntry struct {
	view   ApprovalView
	ch     chan Response
	closed bool
}

// Broker 是 ask 决策的异步通道。并发规则（评审第 4 条）：
//  1. 所有对 map 与 entry 状态的读写都在 b.mu 内完成；
//  2. Prepare 先登记再返回，调用方随后才通知前端；
//  3. Deliver 在持锁期间只向容量为 1 的通道做一次非阻塞发送，绝不阻塞；
//  4. 清理函数与 Deliver 都通过 closed 标志保证只关闭一次通道；
//  5. 等待方只做 select，不持锁。
// 因此「等待方因 ctx 取消去清理」与「前端此刻投递应答」不会互相死锁。
// 每个待审批项保存完整 ApprovalView，供按会话枚举与重连恢复（评审第 3 轮第 2 条）。
type Broker struct {
	mu      sync.Mutex
	pending map[string]*pendingEntry
}

// NewBroker 创建空 Broker。
func NewBroker() *Broker

// Prepare 登记 view.RequestID 并返回应答通道与幂等清理函数。
// 调用方必须在通知前端之前调用；view 会被完整保存，供 Pending 返回。
func (b *Broker) Prepare(view ApprovalView) (<-chan Response, func())

// Deliver 投递应答；未知或已结束的 requestID 返回 false（幂等）。
func (b *Broker) Deliver(requestID string, resp Response) bool

// Pending 返回该会话当前待审批请求的完整快照（按登记顺序）。
func (b *Broker) Pending(sessionID string) []ApprovalView

// CancelSession 关闭该会话全部待审批项并返回其 requestID（会话关闭时调用）。
func (b *Broker) CancelSession(sessionID string) []string
```

### 2.4 internal/session

```go
// Package session 负责追加式 transcript 的写入与重放。
package session

// RecordType 是 JSONL 行类型。
type RecordType string

const (
	RecordSessionMeta RecordType = "session_meta"
	RecordMessage     RecordType = "message"
	RecordCompact     RecordType = "compact"
	RecordUsage       RecordType = "usage"
	RecordMode        RecordType = "mode"
	RecordTurnEnd     RecordType = "turn_end"
)

// Meta 是会话元信息，由 CreateSession 写入首行。落盘时转换成 Record 的字段（见 §8.1）：
// ID → SessionID，CreatedAt → Time，其余进 Record.Meta。
type Meta struct {
	ID        string    `json:"id"`
	Title     string    `json:"title,omitempty"`
	WorkDir   string    `json:"workDir"`
	Model     string    `json:"model,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// Compact 是压缩边界记录。
type Compact struct {
	Trigger    string `json:"trigger"` // auto | manual
	PreTokens  int    `json:"preTokens"`
	PostTokens int    `json:"postTokens"`
	Summary    string `json:"summary"`
	Model      string `json:"model"`
}

// TurnEnd 是一轮结束记录。
type TurnEnd struct {
	TurnID     string `json:"turnId"`
	StopReason string `json:"stopReason"`
	Error      string `json:"error,omitempty"`
	Turns      int    `json:"turns"`
}

// Record 是 JSONL 的一行。Type 决定哪些字段有效：
//   - session_meta：Meta["workDir"]、Meta["title"]、Meta["model"]
//   - message：Message
//   - compact：Compact
//   - usage：Usage
//   - mode：Meta["mode"]
//   - turn_end：TurnEnd
type Record struct {
	ID        string            `json:"id"`
	ParentID  string            `json:"parentId,omitempty"`
	Type      RecordType        `json:"type"`
	SessionID string            `json:"sessionId"`
	Time      time.Time         `json:"time"`
	Message   *llm.Message      `json:"message,omitempty"`
	Compact   *Compact          `json:"compact,omitempty"`
	Usage     *llm.Usage        `json:"usage,omitempty"`
	TurnEnd   *TurnEnd          `json:"turnEnd,omitempty"`
	Meta      map[string]string `json:"meta,omitempty"`
}

// Encode 把记录编码成单行 JSON（不含换行）。
func Encode(r Record) ([]byte, error)

// Decode 解析一行 JSON。空行与未知类型返回错误。
func Decode(line []byte) (Record, error)

// ValidateID 校验会话 ID：只允许字母、数字、下划线与连字符，长度 1–64，
// 且首字符必须是字母或数字。用于阻断 `../` 之类的路径穿越。
func ValidateID(id string) error
```

```go
// Info 是会话列表项。
type Info struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	WorkDir   string    `json:"workDir"`
	Messages  int       `json:"messages"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Store 是会话持久化接口。Append 是同步写入：返回 nil 表示数据已经交给内核
// （写入 OS 页缓存），Flush 额外做 fsync。实现不得做「只进内存队列」的假写入。
type Store interface {
	// CreateSession 创建会话并写入 session_meta 首行；已存在返回 ErrSessionExists。
	CreateSession(ctx context.Context, meta Meta) error
	// Append 追加一条记录；写入失败必须返回错误。
	Append(ctx context.Context, rec Record) error
	// Flush 把数据 fsync 到磁盘。
	Flush(ctx context.Context) error
	// Load 读取一个会话的全部记录（上限 50MB）。会话不存在返回 ErrSessionNotFound；
	// 首行不是 session_meta 返回 ErrInvalidTranscript。
	Load(ctx context.Context, sessionID string) ([]Record, error)
	// List 返回全部会话摘要，按 UpdatedAt 倒序。
	List(ctx context.Context) ([]Info, error)
	// Delete 删除一个会话文件。
	Delete(ctx context.Context, sessionID string) error
	// Close 关闭底层文件句柄；可重复调用。
	Close() error
}

// 错误哨兵。
var (
	ErrSessionExists   = errors.New("session: already exists")
	ErrSessionNotFound = errors.New("session: not found")
	ErrInvalidID       = errors.New("session: invalid id")
	ErrInvalidTranscript = errors.New("session: invalid transcript")
)

// FileStore 是每会话一个 JSONL 文件的实现。进程内只应存在一个实例（由 App 持有）；
// 实例内部按会话维护 fileWriter（每文件一把锁 + 一个句柄），List/Load 作用于没有运行时的
// 会话时也走同一实例，不会出现多实例写同一文件（评审第 2 轮第 19 条）。
type FileStore struct{ /* ... */ }

// NewFileStore 创建存储；dir 不存在时以 0700 创建。
func NewFileStore(dir string) (*FileStore, error)

// MemStore 是内存实现，供测试与无盘场景使用。
type MemStore struct{ /* ... */ }

// NewMemStore 创建内存存储。
func NewMemStore() *MemStore
```

```go
// Loaded 是重放结果。
type Loaded struct {
	Messages        []llm.Message
	Records         int
	Meta            Meta      // 由首行 session_meta 重建（ID/Title/WorkDir/Model/CreatedAt）
	Boundary        *Compact
	Usage           llm.Usage // 全部 usage 记录的 Sum
	LastUsage       llm.Usage // 最后一条 usage 记录；零值表示无，供压缩判断当前上下文规模
	Mode            string    // 最后一条 mode 记录；空表示未记录
	LastAssistantAt time.Time // 最后一条 assistant 消息的时间，供 microCompact 判断
	Interrupted     bool      // 最后一条 assistant 的 tool_use 没有对应结果
}

// Replay 从记录重建消息历史：
//  0. 首行必须是 session_meta，重建 Loaded.Meta；缺失或类型不符返回 ErrInvalidTranscript；
//  1. 找最后一条 compact 记录，其 Summary 变成一条 user 消息；
//  2. 只取该记录之后的消息；
//  3. 按 §8.4 的算法为每个 assistant 消息补齐/重建紧随其后的 tool_result 消息；
//  4. 丢弃空 assistant 消息；
//  5. 用 Usage.Sum 累加全部 usage 记录，最后一条记入 LastUsage；读取最后一条 mode 记录、取最后一条 assistant 的 Message.Time。
func Replay(recs []Record) (Loaded, error)
```

### 2.5 internal/context

```go
// Package context 负责系统提示组装、token 估算与上下文压缩。
// 引用本包时使用别名 sysctx，避免与标准库 context 冲突。
package context

// Options 是 Builder 的构造参数。
type Options struct {
	WorkDir             string
	Model               string
	ContextWindow       int           // 0 时取 llm.ModelInfoOf(Model)
	MaxOutputTokens     int
	MaxInstructionBytes int           // 默认 32KB
	GitTimeout          time.Duration // 默认 2s
	Now                 func() time.Time
	// ReadFile 读取指令文件；nil 时用 os.ReadFile。测试注入用（评审第 3 轮缺失项 11）。
	ReadFile func(path string) ([]byte, error)
	// RunGit 执行 git 查询；nil 时用 exec.CommandContext（受 GitTimeout 约束）。
	// 签名固定为 (ctx, dir, args...) → stdout。
	RunGit func(ctx context.Context, dir string, args ...string) ([]byte, error)
}

// Builder 组装系统提示。所有方法并发安全。
// 缓存分两层（评审第 23 条）：
//   - 静态层（身份、项目指令、工具清单、日期）：按 (cwd, 指令文件 mtime+size, 工具名, 日期) 缓存，
//     只有指令文件或工具集变化时才重算；
//   - 动态层（git 状态）：每次 System 调用重新获取，但同一 turn 内复用，
//     由 BeginTurn 显式开启新 turn，不再用 Invalidate 每轮清空静态缓存。
type Builder struct{ /* ... */ }

// NewBuilder 创建 Builder。
func NewBuilder(opts Options) *Builder

// BeginTurn 标记新 turn 开始：只让动态层（git）失效，静态层保持不变。
func (b *Builder) BeginTurn()

// Invalidate 清空全部缓存；仅在切换工作目录、模型或检测到指令文件变化时调用。
func (b *Builder) Invalidate()

// System 返回完整系统提示：身份、环境、项目指令、git 状态、工具清单、日期。
// git 不可用或超时时省略该段，不返回错误。
func (b *Builder) System(ctx context.Context, tools []tool.Spec) (string, error)

// EstimateString 估算文本 token 数：ASCII 约 4 字符/token，非 ASCII 约 1 字符/token。
func EstimateString(s string) int

// EstimateMessages 估算消息数组 token 数（每条消息 +4，每个块 +6）。
func EstimateMessages(msgs []llm.Message) int
```

```go
// Budget 是压缩阈值参数。零值字段在 NewBudget 中填默认值。
type Budget struct {
	ContextWindow      int
	MaxOutputTokens    int // <=0 时按 8192
	AutoBuffer         int // 默认 13000
	ReservedForSummary int // 默认 20000
	BlockingBuffer     int // 默认 3000
}

// NewBudget 填充默认值并计算派生阈值，所有阈值都做下限钳制（评审第 19 条）：
//
//	reset         = min(ReservedForSummary, max(2048, ContextWindow/4))
//	effective     = max(4096, ContextWindow - min(MaxOutputTokens, reset))
//	autoThreshold = max(effective/2, effective - AutoBuffer)      // 绝不为负，且不超过 effective
//	warning       = max(0, autoThreshold - 20000)
//	blocking      = max(autoThreshold, effective - BlockingBuffer)
//
// 小窗口模型（例如 8k）得到 effective=6144、auto=3072、blocking=3144，全部为正。
func NewBudget(b Budget) Budget

// AutoCompactEnabled 报告是否启用摘要式压缩。ContextWindow < 32000 时禁用，
// 只保留 microCompact 与阻塞线，避免「摘要请求自身超过窗口」导致的必然失败。
func (b Budget) AutoCompactEnabled() bool

// EffectiveWindow 返回有效窗口。
func (b Budget) EffectiveWindow() int

// AutoCompactThreshold 返回自动压缩阈值。
func (b Budget) AutoCompactThreshold() int

// WarningThreshold 返回警告阈值。
func (b Budget) WarningThreshold() int

// BlockingLimit 返回阻塞线。
func (b Budget) BlockingLimit() int

// Status 返回 ok / warning / auto / blocking。
func (b Budget) Status(tokens int) string
```

```go
// CompactPhase 是压缩进度阶段。
type CompactPhase string

const (
	CompactPhaseStart  CompactPhase = "start"
	CompactPhaseDelta  CompactPhase = "delta"
	CompactPhaseDone   CompactPhase = "done"
	CompactPhaseFailed CompactPhase = "failed"
)

// CompactProgress 是一次压缩进度。Failed 阶段带 Err，供前端结束「压缩中」状态。
type CompactProgress struct {
	Phase CompactPhase
	Text  string
	Err   string
}

// CompactProgressFunc 接收压缩进度。可能为 nil；实现必须非阻塞。
// 它由 turn goroutine 调用，因此可以安全地转发为 agent 事件（评审第 3 条）。
type CompactProgressFunc func(CompactProgress)

// BeforeCallInput 是每次模型调用前的压缩输入。
type BeforeCallInput struct {
	Messages        []llm.Message
	LastAssistantAt time.Time // 零值表示未知（新会话）
	LastUsage       llm.Usage // 最近一次 provider 返回的 usage；零值表示无
}

// CompactorConfig 是 Compactor 的构造参数。
type CompactorConfig struct {
	Provider              llm.Provider
	Model                 string
	Budget                Budget
	KeepMinTokens         int           // 默认 10000
	KeepMaxTokens         int           // 默认 40000
	KeepMinMessages       int           // 默认 4
	KeepRecentToolResults int           // 默认 5
	MicroIdle             time.Duration // 默认 60m
	Now                   func() time.Time
}

// Compactor 在每次模型调用前执行 microCompact → autoCompact。
type Compactor struct{ /* ... */ }

// NewCompactor 创建压缩器。
func NewCompactor(cfg CompactorConfig) (*Compactor, error)

// CompactInfo 描述一次压缩。Compactor 不持有 Store，也不写任何记录；
// agent 用这些字段构造 session.Record{Type: RecordCompact}（评审第 3 轮缺失项 10）。
type CompactInfo struct {
	Trigger      string
	Model        string
	Summary      string
	PreTokens    int
	PostTokens   int
	KeptMessages []llm.Message
}

// BeforeCallResult 是 BeforeCall 的输出。
type BeforeCallResult struct {
	Messages  []llm.Message
	Compacted bool
	Micro     bool
	Info      *CompactInfo
}

// BeforeCall 按需压缩。触发条件同时使用「距最后一条 assistant 的时间」
// 与「最近一次真实 usage」，冷恢复后两者都由 session.Loaded 提供。
// 压缩失败不阻断调用：返回原消息、Compacted=false，错误由调用方记录。
//
// progress 契约（评审第 2 轮缺失项 2）：一旦决定压缩，必然依次回调
// Start → Done 或 Start → Failed（用 defer 保证），调用方只凭这两个终态结束 UI 状态。
func (c *Compactor) BeforeCall(ctx context.Context, in BeforeCallInput, progress CompactProgressFunc) (BeforeCallResult, error)

// Budget 返回构造时使用的冻结预算，供 agent 做阻塞线判定。
// agent 必须使用这一个来源，不能自行 NewBudget，否则两侧阈值会漂移（评审第 2 轮第 6 条）。
func (c *Compactor) Budget() Budget

// MicroCompact 把最近 keepRecent 组之外的旧工具结果替换为
// "[old tool result cleared]"，返回节省的 token 估算；节省不足 256 token 时放弃替换。
func MicroCompact(msgs []llm.Message, keepRecent int) (out []llm.Message, cleared int, savedTokens int)

// AutoCompact 调用模型生成摘要并返回「摘要消息 + 保留的最近消息」。
// 连续失败 3 次后熔断，直接返回 ErrCompactionDisabled。
func (c *Compactor) AutoCompact(ctx context.Context, msgs []llm.Message, trigger string, progress CompactProgressFunc) (CompactInfo, []llm.Message, error)

// ErrCompactionDisabled 表示压缩已因连续失败熔断。
var ErrCompactionDisabled = errors.New("context: compaction disabled after repeated failures")
```

### 2.6 internal/agent

```go
// Package agent 是 Agent 运行时：单轮循环、流式事件、工具编排与会话落盘。
package agent

// Config 是 Engine 的构造参数。
// 预算的唯一定义在 Compactor.Budget()：请求的 max_tokens 与阻塞线都从它取，
// Engine 不再保存第二份 MaxOutputTokens（评审第 2 轮第 6 条）。
type Config struct {
	SessionID          string
	WorkDir            string
	Provider           llm.Provider
	Model              string
	MaxTurns           int // 0 表示不限
	MaxToolConcurrency int // 0 时取 10
	SystemExtra        string
	Tools              *tool.Registry
	Permissions        *permission.Engine
	Approvals          *permission.Broker
	Builder            *sysctx.Builder
	Compactor          *sysctx.Compactor // 必填；提供 Budget()
	Store              session.Store
	Now                func() time.Time
	NewID              func(prefix string) string
}

// Validate 校验必填项与数值范围（评审第 3 轮缺失项 9）：
//   - 必填：SessionID != ""、Provider、Tools、Permissions、Approvals、Builder、Compactor、Store 非 nil；
//   - MaxTurns >= 0；MaxToolConcurrency >= 0（0 视为 10）；
//   - Model 为空时取 Provider 的默认模型（由 app 保证非空，Validate 允许空并退回 Provider）。
//
// 可选字段的默认值在 NewEngine 中填充，不在 Validate 中改值：
//   - Now   == nil → time.Now
//   - NewID == nil → 内置实现（crypto/rand 16 字节 hex，前缀 + "_"）
func (c Config) Validate() error

// Input 是一轮输入。TurnID 由调用方预先生成，便于绑定层立即返回给前端。
type Input struct {
	TurnID string
	Prompt string
}

// StopReason 是一轮的结束原因。
type StopReason string

const (
	StopCompleted     StopReason = "completed"
	StopMaxTurns      StopReason = "max_turns"
	StopCanceled      StopReason = "canceled"
	StopError         StopReason = "error"
	StopBlockingLimit StopReason = "blocking_limit"
)

// Result 是一轮调用的结果。模型与工具错误不会以 error 返回，而是体现在 StopReason/Err。
type Result struct {
	SessionID  string
	TurnID     string
	StopReason StopReason
	Text       string
	Usage      llm.Usage
	Turns      int
	Err        string
}

// EventFunc 接收事件。契约（评审第 7 条）：只从 turn goroutine 调用，
// 绝不并发调用；实现必须快速返回（绑定层只做加锁、分配 seq、调 Sender），
// 且不得回调 Engine 的任何方法。
type EventFunc func(Event)

// Engine 是一个会话的运行时。并发契约（评审第 6 条）：
//   - runMu 用 TryLock 保证同一时刻只有一个 Run，第二个 Run 立即返回 ErrTurnRunning；
//   - stateMu 保护 history、turnCancel、running，只在短临界区内持有；
//   - History 在 RLock 下返回深拷贝，绑定线程调用不会与循环 goroutine 竞争；
//   - Cancel 只读 stateMu 取 turnCancel，不经过 runMu，因此不会被运行中的 Run 阻塞。
type Engine struct {
	cfg             Config
	runMu           sync.Mutex
	stateMu         sync.RWMutex
	history         []llm.Message
	turnCancel      context.CancelFunc
	running         bool
	lastAssistantAt time.Time  // 供 microCompact 的时间触发；Restore 时从 Loaded 填充
	lastUsage       llm.Usage  // 供 autoCompact 的 usage 触发；每条 assistant 消息完成后立即更新
}

// NewEngine 创建引擎；Config 非法时返回错误。
func NewEngine(cfg Config) (*Engine, error)

// Restore 从 Store 载入历史。必须在第一次 Run 之前调用。
// Store 返回 ErrSessionNotFound 时视为新会话（调用方应已 CreateSession）；
// 其它错误原样返回。成功后把 Loaded.Usage 写入 lastUsage、
// Loaded.LastAssistantAt 写入 lastAssistantAt，冷恢复的第一次压缩判定即使用磁盘数据。
func (e *Engine) Restore(ctx context.Context) error

// History 返回消息历史深拷贝。
func (e *Engine) History() []llm.Message

// Run 执行一轮用户输入。
// 返回 error 仅表示「无法继续」的内部故障（配置非法、transcript 写入失败、并发 Run）；
// 模型错误、工具错误、用户取消、panic 都通过 Result.StopReason 表达，error 为 nil。
// 无论从哪条路径返回（含 panic），turn_end 都恰好发出一次且是本轮最后一个事件。
func (e *Engine) Run(ctx context.Context, in Input, emit EventFunc) (Result, error)

// Cancel 取消进行中的 Run（只取消当前轮的 turnCtx）。没有进行中的轮次时是空操作，
// 不影响同一 Engine 之后的新轮次。
func (e *Engine) Cancel()

// Close 释放引擎持有的资源（不关闭 Store）。
func (e *Engine) Close() error

// ErrTurnRunning 表示同一 Engine 已有运行中的轮次。
var ErrTurnRunning = errors.New("agent: turn already running")
```

```go
// EventType 是推给前端的事件名。
type EventType string

const (
	EventTurnQueued         EventType = "turn_queued"
	EventTurnStart          EventType = "turn_start"
	EventStepStart          EventType = "step_start"
	EventMessageStart       EventType = "message_start"
	EventBlockStart         EventType = "block_start"
	EventTextDelta          EventType = "text_delta"
	EventThinkingDelta      EventType = "thinking_delta"
	EventToolInputDelta     EventType = "tool_input_delta"
	EventToolCallStart      EventType = "tool_call_start"
	EventToolCallReady      EventType = "tool_call_ready"
	EventToolProgress       EventType = "tool_progress"
	EventPermissionRequest  EventType = "permission_request"
	EventPermissionResolved EventType = "permission_resolved"
	EventToolResult         EventType = "tool_result"
	EventUsage              EventType = "usage"
	EventCompactStart       EventType = "compact_start"
	EventCompactEnd         EventType = "compact_end"
	EventRetry              EventType = "retry"
	EventMessageStop        EventType = "message_stop"
	EventMessageDiscard     EventType = "message_discard"
	EventError              EventType = "error"
	EventTurnEnd            EventType = "turn_end"
)

// Event 是推给前端的统一事件，字段按 Type 解释（见 §3）。
// Seq 由 app.Emitter 在发送前分配；agent 内部构造的事件 Seq 恒为 0，
// 因此 agent 包的测试不应断言 Seq（评审第 2 轮第 15 条）。
type Event struct {
	Type       EventType       `json:"type"`
	SessionID  string          `json:"sessionId"`
	TurnID     string          `json:"turnId"`
	Step       int             `json:"step,omitempty"`
	Position   int             `json:"position,omitempty"` // turn_queued 时的队列位置（1 起）
	Seq        int64           `json:"seq"`
	TimeMS     int64           `json:"timeMs"`
	MessageID  string          `json:"messageId,omitempty"`
	BlockIndex int             `json:"blockIndex,omitempty"`
	Text       string          `json:"text,omitempty"`
	ToolUseID  string          `json:"toolUseId,omitempty"`
	ToolName   string          `json:"toolName,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`
	Result     *ToolResultView `json:"result,omitempty"`
	Approval   *permission.ApprovalView `json:"approval,omitempty"`
	Usage      *llm.Usage      `json:"usage,omitempty"`
	Compact    *CompactView    `json:"compact,omitempty"`
	Retry      *llm.RetryInfo  `json:"retry,omitempty"`
	Error      *ErrorView      `json:"error,omitempty"`
	StopReason StopReason      `json:"stopReason,omitempty"`
}

// ToolResultView 是工具结果的前端视图，不包含完整内容（完整内容在历史里）。
type ToolResultView struct {
	Preview   string `json:"preview"`
	IsError   bool   `json:"isError"`
	Truncated bool   `json:"truncated"`
	Bytes     int    `json:"bytes"`
}

// 审批视图直接复用 permission.ApprovalView：它已经包含 requestId/sessionId/toolName/
// input/reason/options，且 Broker 以同一结构保存待审批项，事件与枚举不会出现两份定义。
// （Event.Approval 的类型就是 *permission.ApprovalView。）

// CompactView 是一次压缩的前端视图。Failed=true 时 Error 说明原因，
// 前端据此结束「压缩中」状态（评审第 2 轮缺失项 2）。
type CompactView struct {
	Trigger    string `json:"trigger"`
	PreTokens  int    `json:"preTokens"`
	PostTokens int    `json:"postTokens"`
	Failed     bool   `json:"failed,omitempty"`
	Error      string `json:"error,omitempty"`
}

// ErrorView 是可展示错误。
type ErrorView struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Status  int    `json:"status,omitempty"`
}
```

### 2.7 internal/app

```go
// Sender 是事件发送器，生产实现包一层 runtime.EventsEmit；测试注入内存实现。
// 这是评审第 15/37 条要求的注入缝：没有它，app 测试会在 getEvents 里 log.Fatalf。
type Sender func(ctx context.Context, name string, payload any)

// Emitter 是全局唯一的事件发射器：互斥锁 + 进程内单调 seq（评审第 15 条）。
type Emitter struct {
	mu  sync.Mutex
	seq int64
	snd Sender
}

// Emit 为事件分配 seq 并同步发送。并发安全，调用顺序即 seq 顺序；不返回错误，
// 发送失败由 Sender 自行记录（Wails 的 EventsEmit 本身没有返回值）。
func (e *Emitter) Emit(ctx context.Context, ev agent.Event)
```

#### 配置与默认值（评审缺失项 6）

```go
// ProviderFactory 为会话构造 llm.Provider。生产实现按 Provider/APIKey/BaseURL 创建；
// 测试注入 fake Provider，避免读环境变量或联网（评审第 3 轮缺失项 8）。
type ProviderFactory func(cfg Config, sessionID string) (llm.Provider, error)

// Config 是应用级配置，由 LoadConfig 从环境变量读取。
type Config struct {
	Provider           string // ANC_PROVIDER，默认 "anthropic"
	APIKey             string // ANC_API_KEY，可空（允许无密钥启动）
	BaseURL            string // ANC_BASE_URL，空时用供应商默认地址
	Model              string // ANC_MODEL，无默认值，缺失时 initErr 记录并在 StartTurn 报错
	PermissionMode     string // ANC_PERMISSION_MODE，默认 "default"
	DataDir            string // ANC_DATA_DIR，默认 <os.UserConfigDir()>/b-desktop
	WorkDir            string // ANC_WORKDIR，默认进程启动时的 cwd
	MaxTurns           int    // ANC_MAX_TURNS，默认 100；0 表示不限
	ContextWindow      int    // ANC_CONTEXT_WINDOW，0 时取 llm.ModelInfoOf(Model).ContextWindow（本地模型必须显式设置）
	MaxOutputTokens    int    // ANC_MAX_OUTPUT_TOKENS，默认 llm.ModelInfoOf(Model).MaxOutput
	MaxToolConcurrency int    // ANC_MAX_TOOL_CONCURRENCY，默认 10
	ProviderFactory    ProviderFactory // nil 时用内置构造函数；测试必须注入
}

// LoadConfig 读取环境变量并填充默认值；返回的错误只表示数值非法，
// 缺少 API Key / Model 不算错误，而是由 StartTurn 在调用时报告。
func LoadConfig() (Config, error)
```

| 变量 | 默认 | 缺失时行为 |
|---|---|---|
| `ANC_PROVIDER` | `anthropic` | 取默认 |
| `ANC_API_KEY` | 空 | 允许启动；`StartTurn` 返回包装 `llm.ErrMissingAPIKey` 的错误 |
| `ANC_BASE_URL` | 空 | 用供应商默认地址 |
| `ANC_MODEL` | 空 | 允许启动；`GetConfig().InitError` 与 `StartTurn` 报「model 未配置」 |
| `ANC_PERMISSION_MODE` | `default` | 非法值在启动时记入 `initErr` |
| `ANC_DATA_DIR` | `<UserConfigDir>/b-desktop` | 取默认并创建 |
| `ANC_WORKDIR` | 进程启动 cwd | 取默认 |
| `ANC_MAX_TURNS` | 100 | 取默认；0 表示不限 |
| `ANC_CONTEXT_WINDOW` | `ModelInfoOf(Model).ContextWindow`（未知模型 200000） | 本地模型必须显式设置；否则会被当成 200k，永不触发压缩（评审第 2 轮缺失项 1） |
| `ANC_MAX_OUTPUT_TOKENS` | `ModelInfoOf(Model).MaxOutput` | 取默认 |
| `ANC_MAX_TOOL_CONCURRENCY` | 10 | 取默认 |

工作目录来源：新建会话时把 `Config.WorkDir` 写进 `session_meta.workDir`；恢复旧会话一律以 meta 为准，不再读环境变量。

预算装配：`wiring.go` 用 `sysctx.NewBudget(sysctx.Budget{ContextWindow: cfg.ContextWindow, MaxOutputTokens: cfg.MaxOutputTokens})` 构造**一次**，同一实例传给 `NewCompactor`；`agent.Engine` 只通过 `compactor.Budget()` 读取，保证压缩阈值与阻塞线完全一致（评审第 2 轮第 6 条）。

#### 会话运行时

```go
// sessionRuntime 是一个会话的内存运行时：一个 Engine、一个输入队列。
// Store 由 App 单例持有，runtime 只引用不负责 Close（评审第 2 轮第 19 条）。
//
// cancel 的生命周期（评审第 2 轮第 3 条）：
//   - 不保存任何 ctx（AGENTS.md「不要把 ctx 塞进结构体」）；每轮开始时
//     turnCtx, turnCancel := context.WithCancel(a.ctx)，在 mu 下写入 turnCancel；
//   - CancelTurn 只取消当前 turnCancel，不会毒化后续轮次（下一轮从 a.ctx 重新派生）；
//   - 关闭窗口时 Wails 会取消 a.ctx，所有派生轮次自动收到取消；
//   - 轮次结束时在 mu 下清空 turnCancel（置 nil）并置 running=false。
//
// inputs 用「互斥锁保护的切片 + wake 信号」而不是 channel：interrupt 需要把新输入插到队首，
// 且要能计算队列位置（channel 做不到）。
// 生命周期：队列 goroutine 只消费；CloseSession 在 mu 下置 closed=true 并 close(quit)；
// 永不 close 切片本身，因此不存在 send on closed channel。
type sessionRuntime struct {
	ID         string
	engine     *agent.Engine
	store      session.Store
	mu         sync.Mutex
	pending    []inputItem         // 未开始的输入，FIFO
	wake       chan struct{}       // cap 1，非阻塞唤醒
	quit       chan struct{}
	closed     bool
	running    bool
	turnCancel context.CancelFunc  // 仅当前轮有效，轮次结束置 nil
}

// inputItem 是一条排队的用户输入。
type inputItem struct {
	TurnID  string
	Prompt  string
	Created time.Time
	Behavior string // queue | interrupt
}

// App 是 Wails 的主绑定对象。
type App struct {
	ctx      context.Context // Wails 启动 ctx：携带 "events" 值，所有 EventsEmit 都用它
	cfg      Config
	store    session.Store   // 进程内唯一实例，所有会话共用（评审第 2 轮第 19 条）
	emitter  *Emitter
	broker   *permission.Broker
	mu       sync.Mutex
	sessions map[string]*sessionRuntime
	initErr  error
}

// New 创建应用实例；发送器默认为 runtime.EventsEmit，Provider 由内置工厂构造。
func New() *App

// NewWithSender 创建应用实例并注入发送器（测试用）。
func NewWithSender(s Sender) *App

// NewWithDeps 创建应用实例并注入发送器与 Provider 工厂（测试用，评审第 3 轮缺失项 8）。
// 等价于 NewWithSender(s) 之后把 cfg.ProviderFactory 设为 factory。
func NewWithDeps(s Sender, cfg Config, factory ProviderFactory) *App

// Startup 在窗口加载前由 Wails 调用，保存生命周期 context 并初始化存储。
func (a *App) Startup(ctx context.Context)

// Shutdown 在应用退出时由 Wails 调用（评审缺失项 5）：
//  1. 对每个 sessionRuntime 调用 turnCancel()（若非空）+ close(quit)，再按 3s 超时等待其队列 goroutine 退出；
//  2. engine.Close()；broker.CancelSession(id) 关闭残留审批；
//  3. a.store.Flush(独立 3s 超时 ctx)；
//  4. a.store.Close() 并清空 sessions 表。
// 整个过程幂等，重复调用安全；清理用独立的后台超时 ctx，不复用可能已被取消的入参 ctx。
func (a *App) Shutdown(ctx context.Context)

// StartTurn 入队一轮对话并立即返回 turnID。behavior 取值：
//   - "" 或 "queue"：追加到队尾（默认）；
//   - "interrupt"：若有运行中的轮次，先调 turnCancel，再把本次输入插到队首（下一个执行）。
//
// 若插入时队列非空（或当前有轮次在跑），立即 emit turn_queued{turnId, position}；
// position 是「未开始队列」中的 1 基序号（1 = 下一个执行），正在运行的不计入。
// 队列无上限，但 `pending` 超过 64 条时返回 ErrQueueFull（防止前端失控）。
// sessionID 必须是 ValidateID 接受的字符串；会话不存在时自动 CreateSession
// （标题取输入的前 40 个**字符**，按 []rune 截断，不能按字节切）。
// 未配置 API Key 时返回包装了 llm.ErrMissingAPIKey 的错误。
func (a *App) StartTurn(sessionID, prompt, behavior string) (string, error)

// CancelTurn 取消指定会话进行中的轮次与排队输入。
// 对每个尚未开始的排队项，App 立即 emit 一条 turn_end{stopReason:"canceled"}，
// 保证 StartTurn 返回过的 turnID 一定有终止事件（评审第 2 轮第 8 条）。
func (a *App) CancelTurn(sessionID string) error

// RespondPermission 回应一次审批请求。sessionID 用于校验归属：requestID 不属于该会话时
// 返回 ErrApprovalNotFound。option 必须是 permission.Option* 之一，否则返回 ErrInvalidOption。
func (a *App) RespondPermission(sessionID, requestID, option string) error

// GetPendingApprovals 返回该会话尚未应答的审批请求（前端刷新后可恢复弹窗）。
func (a *App) GetPendingApprovals(sessionID string) ([]ApprovalViewDTO, error)

// SetPermissionMode 切换会话权限模式，并写入一条 mode 记录。
func (a *App) SetPermissionMode(sessionID, mode string) error

// ListRules 返回会话当前规则。
func (a *App) ListRules(sessionID string) ([]RuleDTO, error)

// RemoveRule 删除一条会话级规则（撤销 always allow/deny）。
func (a *App) RemoveRule(sessionID, ruleID string) error

// ListSessions 返回会话列表。
func (a *App) ListSessions() ([]SessionInfoDTO, error)

// LoadSession 返回会话历史（已转换为前端 DTO）。
func (a *App) LoadSession(sessionID string) (SessionViewDTO, error)

// ResumeSession 为已存在的会话创建运行时并重放历史；重复调用返回已有运行时信息。
// 模型与预算解析（评审第 3 轮第 4 条）：
//  1. model := Loaded.Meta.Model（为空时用 cfg.Model）；
//  2. ContextWindow 优先用 cfg.ContextWindow（显式 HTTPS/本地模型覆盖），
//     否则用 llm.ModelInfoOf(model).ContextWindow；
//  3. 用上面的值 NewBudget 传给该会话的 Compactor；不同会话的模型/预算互不影响。
func (a *App) ResumeSession(sessionID string) (SessionViewDTO, error)

// CloseSession 取消并释放会话运行时（保留磁盘 transcript）。
func (a *App) CloseSession(sessionID string) error

// DeleteSession 关闭运行时并删除 transcript 文件。
func (a *App) DeleteSession(sessionID string) error

// GetHistory 返回会话内存历史的深拷贝。
func (a *App) GetHistory(sessionID string) ([]MessageDTO, error)

// GetConfig 返回当前配置（不包含 API Key 明文）。
func (a *App) GetConfig() ConfigDTO

// 绑定层错误哨兵。
var (
	ErrQueueFull        = errors.New("app: input queue full")
	ErrSessionClosed    = errors.New("app: session closed")
	ErrApprovalNotFound = errors.New("app: approval not found")
	ErrInvalidOption    = errors.New("app: invalid approval option")
)
```

生命周期与取消（评审第 11 条）：

- `a.ctx` 是 Wails 注入的启动 ctx，携带 `events` 值；所有事件发送都用它（`runtime.getEvents` 从 ctx 的 `events` 值取事件器，取不到直接 `log.Fatalf`，见 `vendor/github.com/wailsapp/wails/v2/pkg/runtime/runtime.go:47-61`）。
- 每轮 `turnCtx := context.WithCancel(a.ctx)`，`turnCancel` 存在 runtime 的 `mu` 下，轮次结束清空：**不是** `context.WithoutCancel`（已实测它不传播取消），也不是把 `turnCancel` 复用到整个会话。`CancelTurn` 只调当前 `turnCancel`。
- `Shutdown` 用进程内唯一的 `a.store` 统一 `Flush` → `Close`，不是每个 runtime 各关一次。

### 2.8 前端 DTO

W8 可直接按此实现绑定（评审缺失项 1）。所有时间字段都是 Unix 毫秒。
注意：以上是**绑定返回值**，会经 Wails typescriptify 生成 d.ts，所以 `Input` 用 `any`；
而 `agent.Event` 走 `EventsEmit`（不生成类型），它的 `Input`/`approval.input` 用 `json.RawMessage`
在运行时就是 JSON 对象，前端 `frontend/src/types/agent.ts` 应声明为 `Record<string, unknown>`。

```go
// RuleDTO 是一条权限规则的前端视图。
type RuleDTO struct {
	ID        string `json:"id"`
	Tool      string `json:"tool"`
	Pattern   string `json:"pattern,omitempty"`
	MatchMode string `json:"matchMode,omitempty"`
	Effect    string `json:"effect"`
	Source    string `json:"source"`
	Note      string `json:"note,omitempty"`
}

// SessionInfoDTO 是会话列表项。
type SessionInfoDTO struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	WorkDir   string `json:"workDir"`
	Messages  int    `json:"messages"`
	UpdatedAt int64  `json:"updatedAt"`
	Running   bool   `json:"running"`
}

// MessageDTO 是一条消息的前端视图。TimeMS 来自 llm.Message.Time（UnixMilli），
// 该字段随 JSONL 持久化，Replay 后仍然有值。
type MessageDTO struct {
	ID         string     `json:"id"`
	Role       string     `json:"role"`
	Blocks     []BlockDTO `json:"blocks"`
	StopReason string     `json:"stopReason,omitempty"`
	TimeMS     int64      `json:"timeMs"`
}

// BlockDTO 是一个内容块的前端视图。
// Input 用 any 而不用 json.RawMessage：Wails 的 typescriptify 把 []byte 映射成 number[]
// （vendor/.../typescriptify.go:140 把 uint8 映射为 number，:852-864 生成 num[]），
// 而实际线格式是 JSON 对象，会与生成的 d.ts 不一致（评审第 3 轮第 7 条）。
type BlockDTO struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	Thinking  string `json:"thinking,omitempty"`
	ToolUseID string `json:"toolUseId,omitempty"`
	ToolName  string `json:"toolName,omitempty"`
	Input     any    `json:"input,omitempty"`
	IsError   bool   `json:"isError,omitempty"`
}

// SessionViewDTO 是会话详情。
type SessionViewDTO struct {
	ID             string       `json:"id"`
	Title          string       `json:"title"`
	WorkDir        string       `json:"workDir"`
	Model          string       `json:"model"`
	PermissionMode string       `json:"permissionMode"`
	Messages       []MessageDTO `json:"messages"`
	Usage          UsageDTO     `json:"usage"`
	Running        bool         `json:"running"`
}

// UsageDTO 是 token 统计。
type UsageDTO struct {
	InputTokens              int `json:"inputTokens"`
	OutputTokens             int `json:"outputTokens"`
	CacheReadInputTokens     int `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int `json:"cacheCreationInputTokens"`
}

// ConfigDTO 是只读配置视图。
type ConfigDTO struct {	Provider       string `json:"provider"`
	Model          string `json:"model"`
	BaseURL        string `json:"baseUrl"`
	WorkDir        string `json:"workDir"`
	PermissionMode string `json:"permissionMode"`
	ContextWindow  int    `json:"contextWindow"`
	HasAPIKey      bool   `json:"hasApiKey"`
	InitError      string `json:"initError,omitempty"`
}

// ApprovalViewDTO 是一个待审批请求的前端视图。Input 同样是 any（理由见 BlockDTO）。
type ApprovalViewDTO struct {
	RequestID string              `json:"requestId"`
	SessionID string              `json:"sessionId"`
	ToolName  string              `json:"toolName"`
	Input     any                 `json:"input"`
	Reason    string              `json:"reason"`
	Options   []ApprovalOptionDTO `json:"options"`
}

// ApprovalOptionDTO 是一个审批选项。
type ApprovalOptionDTO struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}
```

---

## 3. 流式事件 schema 与前端消费

### 3.1 通道

单一 Wails 事件名：`agent:event`，载荷就是 §2.6 的 `Event` 序列化对象。前端：

```ts
import { EventsOn } from "../wailsjs/runtime/runtime";
import type { AgentEvent } from "./types/agent";

let lastSeq: Record<string, number> = {};
EventsOn("agent:event", (ev: AgentEvent) => {
  if (ev.sessionId !== currentSessionId) return;
  if (ev.seq <= (lastSeq[ev.sessionId] ?? 0)) return;   // 去重：seq 进程内全局，按会话记账
  lastSeq[ev.sessionId] = ev.seq;
  switch (ev.type) {
    case "text_delta":      appendText(ev.messageId!, ev.text!); break;
    case "thinking_delta":  appendThinking(ev.messageId!, ev.text!); break;
    case "tool_call_start": upsertTool({ id: ev.toolUseId!, name: ev.toolName!, state: "streaming" }); break;
    case "tool_result":     finishTool(ev.toolUseId!, ev.result!); break;
    case "permission_request": openApproval(ev.approval!); break;
    case "message_discard": removeMessage(ev.messageId!); break;   // 半截流作废
    case "turn_end":        markTurnDone(ev.stopReason!); break;
    default: break;
  }
});
```

约束：

1. **单通道 + `type` 判别**：跨类型顺序由后端 `seq` 保证。
2. **只有一个 Emitter**：`App` 持有单个 `Emitter`，互斥锁内分配 `seq` 并调用 `Sender`。`seq` 是**进程内全局**、从 1 开始、单调递增（评审第 15 条；旧稿按会话各建一个计数器是错的）。
3. **后端串行化依据**：Wails 的 `Events.Emit` 逐个调用 `Frontend.Notify`，并发调用之间没有排序保证（`vendor/github.com/wailsapp/wails/v2/internal/frontend/runtime/events.go:56-61`），`Notify` 每次直接 `ExecJS`（`.../desktop/darwin/frontend.go:385-396`）。
4. **单一发射协程**：`agent.EventFunc` 只从 turn goroutine 调用；工具进度经 `tools.go` 的内部 channel 汇入（§6.5）。绑定层拿到事件后只是「加锁 → 分配 seq → 调 Sender」，Sender 本身必须是快速非阻塞的。
5. **内容截断**：`Event` 只带增量文本；`tool_result` 只带预览（前 2KB），完整结果从历史接口读取。

### 3.2 事件表

| type | 关键字段 | 何时发出 | 必须实时 |
|---|---|---|---|
| `turn_queued` | sessionId, turnId, position | `StartTurn` 入队成功但前面还有轮次 | 是 |
| `turn_start` | sessionId, turnId | 入队获得执行权、落盘用户消息前 | 是 |
| `step_start` | step | 每次模型调用前（压缩之后） | 是 |
| `message_start` | messageId, step | 收到 `EventMessageStart` | 是 |
| `block_start` | messageId, blockIndex, toolUseId/toolName | 收到 `EventBlockStart`（文本/思考块） | 是 |
| `text_delta` | messageId, blockIndex, text | 每个 `EventTextDelta` | **是（逐字输出）** |
| `thinking_delta` | messageId, blockIndex, text | 每个 `EventThinkingDelta`（`Text` 非空时） | **是** |
| `tool_input_delta` | messageId, blockIndex, toolUseId, text(partialJson) | 每个 `EventToolInputDelta` | 是（参数流式展示） |
| `tool_call_start` | blockIndex, toolUseId, toolName | 收到 `EventToolUseStart` | 是 |
| `tool_call_ready` | toolUseId, toolName, input(完整) | 收到 `EventToolUseStop` 且 JSON 可解析 | 是 |
| `tool_progress` | toolUseId, text | 工具 `progress` 回调（经 channel 汇入） | 是（后端按 100ms 节流） |
| `permission_request` | approval{requestId,toolName,input,reason,options} | 判定为 ask 且已 `Broker.Prepare` | **是** |
| `permission_resolved` | approval.requestId, text(选项或 "canceled") | 收到应答或 ctx 取消（两条路径都发） | 是 |
| `tool_result` | toolUseId, result{preview,isError,truncated,bytes} | **每个工具结束即发**（不整批聚集） | 是 |
| `usage` | usage（input+output 已合并） | assistant 消息结束后一次 | 是 |
| `compact_start` | compact.trigger | `BeforeCall` 通过 progress 回调 Start | 是 |
| `compact_end` | compact{trigger,preTokens,postTokens,failed,error} | 压缩完成或失败后（必然到达） | 是 |
| `retry` | retry{attempt,max,delayMs,reason} | Provider 决定重试 | 是 |
| `message_stop` | messageId, stopReason | assistant 消息完成并落盘后 | 是 |
| `message_discard` | messageId, error | 已流式输出的 assistant 消息被作废（错误/取消） | 是 |
| `error` | error{kind,message,status} | 模型/存储错误 | 是 |
| `turn_end` | stopReason, usage, text(最终文本), step | 一轮结束（含排队轮次被取消；defer 保证，含 panic） | 是 |

事件顺序不变量（评审第 2 轮第 9 条已修正）：同一个消息内**每个块各自成序**，不同块之间可以交错；不存在「所有增量之后再来 tool_call_start」这条规则。

```text
[turn_queued]?                      // 只有排队时出现
turn_start
 step_start
 [compact_start → (compact_end | CompactFailed 对应的 compact_end{failed})]?
 message_start
   ┌ 文本块:   block_start → text_delta*
   ├ 思考块:   block_start → thinking_delta*
   └ 工具块:   tool_call_start → tool_input_delta* → tool_call_ready
   （三种块可以任意交错；同一块内相对顺序固定）
 message_stop
 usage
 (permission_request → permission_resolved)*
 tool_result*                       // 每个工具完成即发，顺序 = 完成顺序
 step_start | turn_end

异常路径：
 message_discard → error → turn_end
 tool_result(canceled)* → turn_end(canceled)
```

`tool_result` 一定在对应 `tool_call_start` 之后；`turn_end` 一定是本轮最后一个事件（由 `defer` 保证，取消与 panic 路径也发）。被取消的排队轮次是唯一例外：它只有 `turn_queued` 与 `turn_end{canceled}`，没有 `turn_start`（它从未执行）。

---

## 4. Provider 适配

### 4.1 Anthropic Messages SSE

请求：`POST {base}/v1/messages`，头 `x-api-key`、`anthropic-version: 2023-06-01`、`content-type: application/json`，体 `{model, max_tokens, system, messages, tools, stream:true, temperature?, thinking?}`。

请求前校验（评审缺失项 7）：`Thinking != nil && Enabled` 时要求 `BudgetTokens >= 1024`、`BudgetTokens < MaxTokens`、`Temperature == nil`，否则返回包装 `ErrInvalidRequest` 的 `*StreamError{Kind: invalid_request}`，不发网络请求。

消息编码：

- `RoleUser` 的块 → `user` 消息的 `content`（`tool_result` 编码为 `{"type":"tool_result","tool_use_id":..,"content":..,"is_error":..}`）。
- `RoleAssistant` 的块 → `assistant` 消息的 `content`（`tool_use` 编码为 `{"type":"tool_use","id":..,"name":..,"input":..}`）。

响应事件映射：

| SSE event | 数据字段 | → `llm.StreamEvent` |
|---|---|---|
| `message_start` | `message.id`, `message.usage` | `message_start{MessageID, Usage}` |
| `content_block_start` | `index`, `content_block.type/id/name` | `tool_use` → `tool_use_start{Index, Kind=tool_use, ToolUseID, ToolName}`；`thinking` → `block_start{Index, Kind=thinking}`；`text` → `block_start{Index, Kind=text}` |
| `content_block_delta` | `delta.type=text_delta` | `text_delta{Index, Kind=text, Text}` |
| 同上 | `delta.type=thinking_delta` | `thinking_delta{Index, Kind=thinking, Thinking}` |
| 同上 | `delta.type=signature_delta` | `signature{Index, Kind=thinking, Signature}`（**不**映射成 thinking_delta，评审第 28 条） |
| 同上 | `delta.type=input_json_delta` | `tool_input_delta{Index, Kind=tool_use, PartialJSON}` |
| `content_block_stop` | `index` | 若该 index 是工具块 → `tool_use_stop{Index, Kind=tool_use}`；否则忽略 |
| `message_delta` | `delta.stop_reason`, `usage.output_tokens` | `message_delta{StopReason, Usage}` |
| `message_stop` | — | 先为所有未关闭的工具块补 `tool_use_stop{Index, Kind=tool_use}`（防御），再 `message_stop` |
| `ping` | — | `ping`（心跳，不转发给前端） |
| `error` | `error.type/message` | `error{StreamError}` 后关闭流 |

### 4.2 OpenAI Chat Completions SSE

请求：`POST {base}/v1/chat/completions`，头 `Authorization: Bearer`，体 `{model, messages, tools, stream:true, stream_options:{include_usage:true}, max_tokens?, temperature?}`。

消息编码：

- system 作为首条 `{"role":"system","content":...}`。
- assistant 的 `tool_use` → `{"role":"assistant","content":..,"tool_calls":[{"id","type":"function","function":{"name","arguments":"<json string>"}}]}`。
- 每条 `tool_result` → 独立的 `{"role":"tool","tool_call_id":..,"content":..}`；按工具调用顺序展开成多条 tool 消息。
- thinking 块在 OpenAI 协议下不发送。

**块索引分配（评审第 2 条）**：OpenAI 的 `content` 与 `tool_calls[i].index` 是两个独立编号空间，不能直接透传。适配器维护 `nextBlock int`：

- 第一个正文 delta 到来时分配 `textIndex = nextBlock++`；
- 每个首次出现的 `tool_calls[index]` 分配 `toolIndex = nextBlock++`，记入 `map[toolCallIndex]blockIndex`；
- 同一响应内块索引唯一且单调递增，`text` 与 `tool_use` 永不共用一个 Index。

响应映射：

| 字段 | → `llm.StreamEvent` |
|---|---|
| 首块 `choices[0].delta.role` 或首个 chunk | `message_start{MessageID: id}` |
| `choices[0].delta.content` 非空（首次） | `block_start{Index:textIndex, Kind:text}`，随后 `text_delta{Index:textIndex, Kind:text, Text}` |
| `choices[0].delta.tool_calls[i]` 首次出现该 index | `tool_use_start{Index:toolIndex, Kind:tool_use, ToolUseID:id, ToolName:function.name}` |
| 同上 `function.arguments` 增量 | `tool_input_delta{Index:toolIndex, Kind:tool_use, PartialJSON}` |
| `choices[0].finish_reason` | 先为所有打开的工具块发 `tool_use_stop{Index, Kind=tool_use}`（按块索引升序），再暂存 `message_delta{StopReason}` |
| `usage`（include_usage 的末块） | 合并进随后的 `message_delta` 的 `Usage` |
| `data: [DONE]` | 若仍有打开的工具块（provider 未给 finish_reason）先补 `tool_use_stop`，再 `message_stop` |
| `data: {"error":{...}}` | `error{StreamError}` 后关闭流 |

`finish_reason` 映射：`stop`→`end_turn`，`tool_calls`→`tool_use`，`length`→`max_tokens`，`content_filter`→`refusal`，其他→空。

**`tool_use_stop` 保证（评审第 1 条）**：OpenAI 侧在 `finish_reason` 与 `[DONE]` 两个收尾点都检查未关闭的工具块并补发；agent 侧在 `message_stop` 再兜底关闭（§6.3）。两层保证下，任何 OpenAI 兼容端点都不会出现「工具调用永远停在未完成状态」。

### 4.3 统一约束

1. **原始增量直通**：映射层不做聚合，`partial_json` 原样转发；JSON 完整解析只在 agent 侧做一次。
2. **工具调用 ID**：Anthropic 用 `content_block.id`，OpenAI 用 `tool_calls[].id`；都原样写进 `ToolUseID`，不做重编号（zed.md:327 记录了跨轮重号的坑，这里以 provider 原值为准，同一轮内唯一即可）。
3. **空内容保底**：`ToolResultBlock` 的 `Text` 为空时填 `"<tool returned no output>"`，避免部分 provider 把空串当作无结果（zed.md:154）。
4. **usage 合并（评审第 24 条）**：Anthropic 的输入 token 只在 `message_start` 出现、输出 token 在 `message_delta` 出现；agent 用 `Usage.Merge` 合成单条消息的用量，用 `Usage.Sum` 累加多步与整轮（评审第 2 轮第 4 条）。`usage` 事件发出的是**整轮累计**值。
5. **不做模型 fallback**：本期没有「流中途换模型重试」。因此不存在「已有 delta 又清空重来」的路径；一旦产出 delta 就绝不重试（§4.4）。

### 4.4 重试边界、超时、取消与 goroutine 生命周期

| 项 | 规则 | 依据 |
|---|---|---|
| 重试位置 | 只在「请求未建立 / 未收到任何 delta」时重试；一旦产出事件就不再重试 | cc.md:506-519 的重试在请求层 |
| 可重试 | 网络错误、408、409、429、500、502、503、504、529 | cc.md:518 |
| 不可重试 | 400、401、403、404、413、422 | 同上 |
| 上限 | `MaxRetries` 默认 5 次（共 6 次尝试） | 桌面端取更短上限 |
| 退避 | `min(BaseBackoff * 2^(attempt-1), MaxBackoff)` + 0–25% 抖动 | cc.md:517 |
| `Retry-After` | 优先使用，按秒或 HTTP 日期解析，不超过 `MaxBackoff` | cc.md:517 |
| 通知 | 每次重试前产出 `EventRetry` | cc.md:519 |
| 首字节超时 | `FirstTokenTimeout` 默认 60s，从请求发出到第一个 SSE 事件 | cc.md:508 |
| 空闲超时 | `IdleTimeout` 默认 120s，两个 chunk 之间无数据即中止 | 同上 |
| ctx 取消 | 请求用 `http.NewRequestWithContext`；退避用 `select` 等 `ctx.Done()`；返回包装 `context.Canceled` 的错误 | AGENTS.md Go 规则 2/3 |

`sse.go` 的 goroutine 契约（评审第 5 条）：

```text
Stream 持有：body io.ReadCloser、events chan StreamEvent(cap 32)、errCh chan error(cap 1)、done chan struct{}、wg sync.WaitGroup
读取 goroutine（Stream 创建时启动，唯一一个）：
    defer wg.Done()
    for {
        ev := decodeNext()                 // 读 body
        select {
        case events <- ev:                 // 正常投递
        case <-done:  return               // 调用方已 Close，立刻退出，不会卡在满通道
        }
        if err { deliver error then return }
    }
Recv(ctx):
    select {
    case ev := <-events:            return ev, nil
    case err := <-errCh:            return StreamEvent{}, err
    case <-ctx.Done():              return StreamEvent{}, wrapCtx(ctx.Err())
    case <-idle.C:                  return StreamEvent{}, timeoutErr
    }
Close():
    once { close(done); body.Close(); wg.Wait() }   // 幂等，且不等任何未被消费的事件
```

- 读取侧**所有**发送都包在 `select { case events <- ev: case <-done: }` 里，通道满时也能被 Close 唤醒（评审第 5 条的孤儿 goroutine 与 Close 卡死问题）。
- `Recv` 接收 ctx：Stream 不保存调用方的 ctx，只在结构体里保存 `done` 与 body（满足 AGENTS.md:200「不要把 ctx 塞进结构体」）。
- 调用约定：`agent/stream.go` 必须 `defer stream.Close()`，且 `Recv` 返回任何错误后立即 Close。
- 正常结束（`[DONE]` / `message_stop`）：读取 goroutine 关闭 `events`，`Recv` 在通道关闭且 `errCh` 为空时返回 `io.EOF`。
- 异常结束：读取 goroutine **先**把错误写入容量 1 的 `errCh`，**再**关闭 `events`；`Recv` 在 `events` 关闭后先做一次非阻塞 `errCh` 检查，有错误返回错误，否则返回 `io.EOF`。这样不会因为 select 随机选中已关闭的 `events` 而吞掉真实错误。

---

## 5. 权限

### 5.1 五档语义

| 模式 | 只读工具 | Write/Edit | Bash | 其他写工具 | 说明 |
|---|---|---|---|---|---|
| `default` | allow | ask | ask | ask | 默认每步询问 |
| `acceptEdits` | allow | 全部目标路径在工作目录内 allow，否则 ask | ask | ask | 与 cc 的 `acceptEdits` 一致（cc.md:230） |
| `plan` | allow | deny | **deny** | deny | 计划模式禁止任何副作用；`NoApproval` 工具（如 TodoWrite）允许。本期没有只读命令分类器（`Bash.ReadOnly` 恒为 false），因此 Bash 一律 deny（评审第 8 条） |
| `bypassPermissions` | allow | allow | allow | allow | 只有显式 deny 规则与内置硬拒绝（尽力而为）能拦住（cc.md:226） |
| `dontAsk` | allow | deny | deny | deny | 判定与 default 相同，但**任何来源的 ask 最终都转 deny**（§5.2 第 9 步） |

### 5.2 判定优先级（deny 优先）

`Engine.Check` 严格按序执行，命中即返回（评审第 8 条修正：plan 在 ask 规则之前）：

1. `safety.go` 的内置硬拒绝。它只是**纵深防御**，不是安全边界（评审第 2 轮第 11 条）：只拦截最常见的字面形式，不试图解析 shell。实现方式：对命令做 `strings.ToLower` + 空白折叠后，按「命令词 + 参数前缀集合」的模式表匹配（至少覆盖 `rm -rf /`、`rm -fr /`、`rm -r -f /`、`mkfs`、`dd of=/dev/`）；`rm -rf /*`、变量拼接、脚本内命令、`bash -c '...'` 等都不保证被拦下。真正的边界是权限模式与用户审批。
2. `EffectDeny` 规则：任一匹配即 deny（`Tool="*"` 也参与）。
3. `req.NoApproval`（工具实现 `tool.AlwaysAllow` 且返回 true）→ allow。**permission 不按工具名判断**，因此 TodoWrite 的放行来自工具自己的声明（评审第 3 轮第 5 条）。
4. `ModePlan`：只读工具 allow；`req.NoApproval` 已在上一步 allow；其余 deny，`Reason="plan mode blocks mutations"`。
5. `EffectAsk` 规则：任一匹配即 ask。
6. `ModeAcceptEdits` 且 `req.WriteFiles` 且 `Paths` 全部位于 `WorkDir` 内 → allow。
7. `ModeBypassPermissions` → allow。
8. `EffectAllow` 规则：工具名匹配，且本次调用的可判定输入全部匹配 allow 规则 → allow；**任一 allow 规则无法判定（例如 Bash 复合命令遇到 prefix 规则）时不放行**，继续往下走。
9. 模式默认：`ModeDefault` / `ModeAcceptEdits` → 只读 allow，其余 ask；`ModePlan` / `ModeBypassPermissions` 已在上方返回。
10. `ModeDontAsk` 收口（评审第 3 轮第 6 条）：**只要最终结果是 ask（无论来自第 5 步的 ask 规则还是第 9 步的模式默认），一律改成 deny**，`Reason="dontAsk mode"`。因此 dontAsk 下 `Engine.Check` 永不返回 ask，agent 永远不会 `Prepare`/`emit permission_request`。

第 7 步「全部匹配才算 allow」与 zed 的 always_allow 语义一致（zed.md:218），deny 则是「任一匹配」（zed.md:216）；两者不对称是有意的。Pattern 为空时该规则匹配该工具的所有调用（整类放行），不再看输入（§5.6 用它实现「其他工具」的 always）。

**关于复合命令（评审第 9 条）**：旧稿声称「全部匹配能挡住 `ls && rm -rf /`」是错的——按前缀语义，整串仍以 `ls` 开头。现在的规则是：

- `MatchExact` 比较整串，复合命令也用整串比较，安全；
- `MatchPrefix` 仅当命令**不含任何 shell 元字符**（`& | ; < > $ ` \ ( ) { } * ? [ ] ~ # 换行`）时才参与判定，否则该规则视为「无法判定」，不产生 allow；
- 不实现 shell 词法器：无法判定就 fail-closed 走 ask/deny，而不是靠猜测放行。

### 5.3 规则来源与会话级存储

- 来源优先级不参与判定（deny 先于 allow），只用于展示与删除定位：`builtin` < `project` < `user` < `session`。
- `session` 源规则由 `Engine.AddRule` 写入内存，随会话生命周期存在；`app/sessionRuntime` 负责在 `CloseSession` / `Shutdown` 时丢弃。
- `SetPermissionMode` 额外写一条 `mode` 记录，恢复时可还原（§8.4）。
- 写盘策略：本期只做会话级（内存）。跨会话持久化需要新增规则来源与存储格式，列为后续项。

### 5.4 ask 的异步审批通道与 ctx 取消

```text
agent.tools.execute
  ├─ d := engine.Check(ctx, req)                    // 纯判定，不阻塞
  ├─ switch d.Effect
  │    allow → run
  │    deny  → tool_result{is_error:true, "permission denied: "+d.Reason}
  │    ask   → awaitApproval
  └─ awaitApproval:
       1. id := NewID("perm")
       2. view := permission.ApprovalView{
              RequestID: id, SessionID: req.SessionID,
              ToolName: req.ToolName, Input: req.Input,
              Reason: d.Reason, Options: 四个标准选项（无安全 always 时只给 once）}
       3. ch, done := broker.Prepare(view)         // 先登记（容量 1，保存完整视图）
       4. emit(permission_request{approval: view})
       5. select {
            case resp, ok := <-ch:
                if !ok {
                    emit(permission_resolved{id, text:"canceled"})
                    return deny("approval canceled")
                }
                emit(permission_resolved{id, text: resp.Option})   // 正常应答也发，text=选中的选项
            case <-ctx.Done():
                done()                              // 幂等清理，不会阻塞在锁上
                emit(permission_resolved{id, text:"canceled"})
                return deny("canceled while awaiting approval")
          }
       6. done()                                   // 幂等清理
       7. always 选项 → engine.AddRule(agent 推导的规则)（§5.6）
       8. 返回 allow / deny
```

**无死锁证明（评审第 4 条）**：`pending[id].ch` 容量固定为 1；`Deliver` 在持锁期间最多做一次 `e.ch <- resp`（entry 已从 map 摘除且 `closed` 已置位，因此只可能发送一次），发送不会阻塞；清理函数与 `Deliver` 都在同一把锁内检查 `closed` 后关闭通道，关闭只发生一次；等待方 `select` 不持锁。因此「ctx 取消去清理」与「前端同时投递」不会互锁。

约束：

- **先 `Prepare` 再 emit**：避免用户秒回时 `Deliver` 找不到登记（codex.md:202、zcode.md:132）。
- **ctx 取消 = 拒绝**：返回的 `tool_result` 是 `is_error`，这一轮随后以 `StopCanceled` 结束。不存在「超时自动允许」。
- **并发审批**：同一批并发工具里只要有一个需要 ask，该批就降级为串行（§6.5），避免同时弹出多个审批框。

### 5.5 路径与工具分类：不在 permission 里重写（评审第 26 条）

- 工具通过 `tool.PathAware` 提供 `AffectedPaths`；`agent` 在权限检查前调用它，把 **已解析符号链接的绝对路径** 填进 `permission.Request.Paths`，并置 `WriteFiles = !ReadOnly && len(Paths) > 0`。
- `permission` 只做三件事：把 `Paths` 转成相对 `WorkDir` 的斜杠路径、用 `tool.MatchPath` 匹配 glob 规则、按 `MatchMode` 比较其余模式。它不解析 `file_path` 字段，也不认识具体工具名（acceptEdits 判定用 `WriteFiles`，`NoApproval` 来自工具的 `tool.AlwaysAllow` 声明）。
- 文件 glob 规则的基准（评审第 2 轮第 12 条）：`rel, err := filepath.Rel(req.WorkDir, p)`，再用 `filepath.ToSlash(rel)` 去匹配 `Pattern`。**不能**把绝对 `Paths` 直接喂给 `MatchPath(相对 pattern, 绝对 path)`，那样永远不命中。
- glob 规则的「全部匹配」语义：**每一个** `req.Paths` 都必须匹配规则模式才算命中；`req.Paths` 为空时 glob 规则不生效（fail-closed）。
- `Request.WorkDir` 由 agent 填 `filepath.EvalSymlinks` 之后的绝对路径，`Paths` 也已是解析后的绝对路径，两者可直接比较。
- 工具改名或字段改名只影响它自己的 `AffectedPaths`，不会让权限层静默失效。
- glob 统一用 `tool.MatchPath`；`filepath.Match` 不支持 `**`（实测见 §12），不能用于路径规则或 Glob 工具。

### 5.6 always allow / always deny 的规则推导（评审第 21 条）

前端只传 `(requestID, option)`，`Pattern` 与 `MatchMode` 由 **agent** 推导，避免前端构造任意规则。
`MatchMode` 必填、没有「按工具名猜默认值」的路径（评审第 2 轮第 5 条），所以这里逐类写死：

| 工具类型（由 agent 判定） | allow_always 生成的规则 |
|---|---|
| Bash | `MatchExact`，Pattern = 完整命令字符串；复合命令也安全（整串相等才放行） |
| 文件工具（实现了 `PathAware`） | `MatchGlob`，Pattern = `AffectedPaths` 相对 `WorkDir` 的路径；目标在工作目录外时拒绝生成 always 规则，降级为 once |
| 其他工具（无路径、非 Bash） | `MatchJSON` + `Pattern=""`（整类放行）。空 Pattern 才是「整类」语义；**不能**把工具名当 Pattern（输入 JSON 里通常没有工具名，会静默失效；反过来还可能误命中，评审第 2 轮第 7 条） |

- 绝不为 Bash 生成 `Pattern=""` 的「整类放行」规则。
- `deny_always` 同样按上表推导，但任何工具都允许 `Pattern=""`（拒绝整类工具是安全方向）。
- 生成规则前校验：`Tool != ""`、`MatchMode` 非空且与上表一致、`Effect` 与选项一致、`Source=SourceSession`。
- agent 判定「Bash / 文件工具 / 其他」用的是自己手里的 `tool.Tool` 实例（`PathAware` 或工具名常量），这是 agent 的职责，不是 permission 的。

---

## 6. Agent 循环

### 6.1 总体流程

```text
Run(ctx, in, emit)
  ├─ runMu.TryLock(); defer Unlock()                  // 失败 → ErrTurnRunning
  ├─ ctx, cancel := context.WithCancel(ctx)
  ├─ persistCtx := detached(ctx)                      // context.WithTimeout(context.WithoutCancel(ctx), 5s)
  ├─ stateMu: running=true, turnCancel=cancel
  ├─ res := Result{}; roundUsage := llm.Usage{}       // Run 用命名返回值 res，供 defer 改写
  ├─ defer func() {                                   // 唯一的收尾 defer（评审第 2 轮第 2 条）
  │     if r := recover(); r != nil {
  │         emit(error{kind:"panic", message:...})
  │         res = Result{StopReason: StopError, Err: ...}
  │     }
  │     stateMu: running=false, turnCancel=nil
  │     e.append(persistCtx, RecordTurnEnd{...})      // 记录写入者（评审第 3 轮第 3 条）
  │     e.flush(persistCtx)
  │     if !turnEndSent { emit(turn_end{res.StopReason, roundUsage, text}) }   // 本轮最后一个事件
  │     cancel()
  │ }()
  ├─ builder.BeginTurn()                              // 只失效 git 动态层
  ├─ emit(turn_start); e.append(persistCtx, user 消息) // 同步写，失败 → 返回 error
  ├─ step := 0
  └─ loop
       step++
       ├─ if MaxTurns>0 && step>MaxTurns → turn_end{max_turns}; return
       ├─ emit(step_start)
       ├─ system, err := builder.System(ctx, tools.Specs())   // 每步重算（静态层走缓存）
       ├─ budget := compactor.Budget()                 // 唯一来源，不自行 NewBudget
       ├─ 预算检查：max(EstimateMessages(history), lastUsage.Total()) > budget.BlockingLimit()
       │     → turn_end{blocking_limit}; return
       ├─ r := compactor.BeforeCall(ctx, BeforeCallInput{history, lastAssistantAt, lastUsage}, progress)
       │     progress: Start → emit(compact_start)；Done/Failed → emit(compact_end)
       │     若 r.Compacted：
       │         e.append(persistCtx, RecordCompact{Info, Model: cfg.Model})
       │         for m in r.Info.KeptMessages: e.append(persistCtx, message 记录)
       │         history = r.Messages; e.flush(persistCtx)
       ├─ req := prompt.Build(system, history, tools.Specs())  // req.MaxTokens = budget.MaxOutputTokens
       ├─ stream := provider.Stream(ctx, req)
       ├─ 累积 assistant 消息（§6.3）；逐条 emit 增量；本步结束必须 stream.Close()
       ├─ 失败 → 若已有 delta 则 message_discard；emit(error) + turn_end{error}; return
       ├─ msg.Usage = 本步 merge 后的 usage
       ├─ lastUsage = lastUsage.Sum(msg.Usage); roundUsage = roundUsage.Sum(msg.Usage)
       ├─ lastAssistantAt = msg.Time = Now()
       ├─ e.append(persistCtx, assistant 消息)
       ├─ e.append(persistCtx, RecordUsage{msg.Usage})          // 冷恢复重算会话用量
       ├─ emit(message_stop{msg.ID, stopReason})
       ├─ emit(usage{roundUsage})                               // 独立事件，紧跟 message_stop
       ├─ 无 tool_use → emit(turn_end completed); return
       ├─ precheck：对每个 call 求 Check（纯判定）→ allow/ask/deny
       ├─ group := tool.Group(calls, registry, allowed, MaxToolConcurrency)
       ├─ 执行批次（§6.5）：每个工具完成立即 emit(tool_result)
       ├─ 组装一条 user 消息（全部 tool_result，顺序与 toolCalls 一致）并落盘
       └─ continue
```

`turn_endSent` 是局部标志：任何提前 `emit(turn_end)` 的路径都要先置位，收尾 defer 只负责补发一次。**不允许注册两个各自发 turn_end 的 defer**（旧稿的 LIFO 顺序会让 turn_end 早于 error，或发两次；评审第 2 轮第 2 条）。

`persistCtx` 用于所有 transcript 写入：它脱离取消并带 5s 超时。用户取消时 `ctx` 已 Done，但补写的 `tool_result` 仍必须落盘，否则重启后配对缺失（评审第 3 轮第 7 条）。已实测 `context.WithoutCancel` 保留 value 且 `Err()==nil`（§12.1）。

关键点：

1. **不是递归**：`continue` 只是给 `history` 追加消息（cc.md:77）。
2. **落盘时机**：用户消息在请求前**同步**写入（`Store.Append` 返回错误即中止，评审第 12 条）；assistant 消息与工具结果产生后立即写入。
3. **tool_result 必须连续放在一条 user 消息里**：不能与普通 user 文本交错，否则部分 API 报 400（cc.md:167-174）。
4. **Provider 的 `stop_reason` 只写进消息元数据，不参与判定**（zed.md:99）。
5. **`message_stop` 在工具执行之前发出**：assistant 消息已完整、已落盘；工具结果随后以 `tool_result` 事件逐个发出（评审第 13 条）。
6. **记录写入者**（评审第 3 轮第 3/10 条）：`user 消息`、`assistant 消息`、`RecordUsage`（每步一条）、`RecordCompact`（压缩时一条）、`RecordTurnEnd`（每轮一条）全部由 agent 在 `persistCtx` 下写入；Compactor 与 Store 都不主动写这些记录。`Restore` 把 `Loaded.Usage` 赋给 `lastUsage`、`Loaded.LastAssistantAt` 赋给 `lastAssistantAt`，冷恢复后的第一次 `BeforeCall` 就能用磁盘上的用量与时间触发压缩。
7. **系统提示**：每个 step 调一次 `builder.System(ctx, tools.Specs())`；工具集每步重新取快照（用户中途加规则/换工具立即生效），静态层命中缓存，git 动态层每 turn 刷新一次。
8. **模型请求失败的边界**（§11.3 已登记的偏离）：provider 内部已按 §4.4 重试；重试耗尽后本轮以 `error + turn_end{error}` 结束，**不**把请求失败包装成 tool_result 回灌。原因：此时没有可执行的工具调用，回灌只会让模型在完全没有新信息的情况下重复同一请求。

### 6.2 轮次上限

- `Config.MaxTurns` 约束**模型调用轮数**，不是工具调用次数（cc.md:79-87）。
- `0` 表示不限；`app` 默认 100（`ANC_MAX_TURNS` 可改）。参考实现中 codex/zed/zcode 的主循环没有轮次上限（codex.md:75、zed.md:113、zcode.md:53-57），这里按 `AGENTS.md` 保留该开关并在 UI 展示。
- 超限时补发 `turn_end{max_turns}`；历史里已有全部工具结果，可以直接开始下一轮。

### 6.3 流式累积

`stream.go` 用 `map[blockKey]*accumulator`，`blockKey{Index int, Kind llm.BlockType}`（评审第 2 轮第 1 条）：

- `block_start` → 按 `Kind` 建立文本/思考块（`Kind` 必填）。
- `text_delta` → 追加 `Text` 并 emit；`Kind` 必须是 `text`，为空时先按 `Index` 查已打开的块，查不到就丢弃并记录 error（**不再**默认成文本）。
- `thinking_delta` → 追加 `Thinking` 并 emit；`Kind` 必须是 `thinking`。
- `signature` → 只写入 accumulator 的 `Signature`，**不**转发给前端（评审第 28 条）。
- `tool_use_start` → 按 `Kind=tool_use` 建立工具块；若 `ToolName` 为空则保留已有值（OpenAI 只在首块给 name）。
- `tool_input_delta` → 按 `Kind=tool_use` 追加 `PartialJSON` 并 emit；`Kind` 为空时先按 `Index` 查已打开的工具块，查不到就丢弃并记录 error（这是上一轮「参数写进幽灵块」的直接修复）。
- `tool_use_stop` → 按 `Kind=tool_use` 关闭该块：`json.Unmarshal(partial)`；失败则保留 `Input=nil` 并记录解析错误；emit `tool_call_ready`。
- `message_stop` → **兜底**：所有仍处于打开状态的工具块按块索引升序补做一次关闭与 `tool_call_ready`（评审第 1 条的第二层保证）。
- 流结束时按块索引升序拼成一条 `llm.Message{Role: RoleAssistant}`；`MessageID` 取 `message_start` 的值，缺失时用 `NewID("msg")`；`Time = Now()`。
- **usage**：单条消息内用 `Usage.Merge` 合并 `message_start` 与 `message_delta`，写入 `msg.Usage`；整轮与整会话用 `Usage.Sum` 累加（评审第 2 轮第 4 条）。`usage` 事件发整轮累计值。

被放弃的 assistant 消息（流已产出 delta 但随后失败/取消）不落盘，并 emit `message_discard{messageId}` 让前端删除半截内容（评审第 20 条）。

### 6.4 错误边界

| 错误 | 处理 |
|---|---|
| 模型请求失败 | `EventError` + `turn_end{error}`；历史停在最后一条完整消息 |
| 模型返回 `max_tokens` | 本轮正常结束（`completed`），assistant 文本已落盘；不自动续写 |
| 模型流中断（已有 delta） | `message_discard` + `error` + `turn_end{error}` |
| 工具不存在 | `tool_result{is_error:"unknown tool: X"}` |
| 工具参数 JSON 非法 | `tool_result{is_error:"invalid input json: ..."}` |
| `PathAware.AffectedPaths` 失败 | `tool_result{is_error:"invalid path: ..."}`，不执行工具 |
| `Tool.Run` 返回 error | `tool_result{is_error: err.Error()}`（`fmt.Errorf("tool %s: %w", name, err)`） |
| `Tool.Run` panic | `recover()` → `tool_result{is_error:"tool panicked: ..."}` |
| **turn goroutine panic** | `Run` 的 `defer recover()`：emit `error` + `turn_end{error}`，返回 `Result{StopError}`，**不**让进程退出（评审第 14 条） |
| ctx 取消 | 若已有 delta 先 `message_discard`；立即停止等待未完成的工具；为全部调用补 `"canceled by user"` 结果并 emit；`turn_end{canceled}` |
| 落盘失败 | `Run` 返回 error（不做静默降级） |

`tools.go` 的 `finalizeResults` 按 `calls` 长度建 slice，空槽补 `tool.ErrorResult("canceled by user")`，保证「一个 `tool_use` 恰好一个 `tool_result`」。

### 6.5 并发、分组与事件汇入

- 上限 `MaxToolConcurrency`，默认 10（cc.md:161）。
- 分组依据 `Tool.ConcurrencySafe(input)`（cc.md:162-163），不是 `ReadOnly`。
- **只有权限预检为 allow 的调用**才能进入并发批；ask/deny 单独成批串行。
- 执行模型（评审第 7、26 条同时满足）：

```text
turn goroutine（本步全部调用，跨批次共享同一份 results）：
  allCalls   := 本步模型的全部 tool_use（tool.Call 的 Index 是全局下标 0..len(allCalls)-1）
  results    := make([]tool.Result, len(allCalls))   // **按全局长度分配**
  doneFlags  := make([]bool, len(allCalls))
  remaining  := len(allCalls)
  progressCh := make(chan progressMsg, 64)          // 满了就丢弃，不阻塞工具
  doneCh     := make(chan toolDone, len(allCalls))  // 容量 = 调用数，迟到写入不会阻塞

  start := func(c Call) {
      go func() {
          defer func() {
              if r := recover(); r != nil {
                  doneCh <- toolDone{c.Index, tool.ErrorResult("tool panicked: %v", r)}
              }
          }()
          p := func(pr tool.Progress) {
              select { case progressCh <- progressMsg{c.Index, pr}: default: }   // 非阻塞
          }
          doneCh <- toolDone{c.Index, tool.Call(ctx, t, c.Input, p)}  // **用 c.Index 回填**
      }()
  }

  // drainOne 处理一个就绪事件，返回 true 表示收到 ctx 取消。
  drainOne := func() bool {
      select {
      case <-ctx.Done():      return true
      case pm := <-progressCh: emit(tool_progress)                       // 唯一发射者
      case d := <-doneCh:     results[d.idx] = d.res; doneFlags[d.idx] = true; remaining--; emit(tool_result)
      }
      return false
  }

  for _, batch := range batches {
      for _, c := range batch.Calls {
          start(c)                                  // c.Index 是全局下标，跨批次不复位
          if !batch.Concurrent {                    // 串行批：等这个调用结束再启动下一个
              for !doneFlags[c.Index] {
                  if drainOne() { goto canceled }
              }
          }
      }
  }
  for remaining > 0 { if drainOne() { goto canceled } }
  goto done

canceled:
  finalizeResults(results, "canceled by user")      // 按全局下标补空槽
  persist + emit 缺失的 tool_result
done:
```

- **下标契约（评审第 3 轮第 1 条）**：`results` 只按 `len(allCalls)` 分配一次，所有批次共用；`start`/回填/等待全部使用 `tool.Call.Index`，**不得**用 `range batch.Calls` 的批次内下标 `i`。否则第二批会写进第一批的槽位，`tool_result` 会配到错误的 `tool_use_id`。
- `tools.go` 的 `finalizeResults` 也以全局下标为准，长度 `len(allCalls)`。

- 并发工具只写各自的 `results[i]`，无共享写；`emit` 只在 turn goroutine 调用，满足 §2.6 的 `EventFunc` 契约。
- 取消时**立即**收尾返回，不等 ctx 不敏感的阻塞工具；卡住的 goroutine 之后如果返回，其 `doneCh`（容量 = 调用数）写入不会阻塞，结果被丢弃（评审第 27 条）。

### 6.6 取消、排队与 App 关闭

队列 goroutine（每个 sessionRuntime 一个，Startup/ResumeSession 时启动）：

```text
for {
    select { case <-rt.quit: return; case <-rt.wake: }
    for {
        rt.mu.Lock()
        if rt.closed || len(rt.pending) == 0 { rt.mu.Unlock(); break }
        it := rt.pending[0]; rt.pending = rt.pending[1:]; rt.running = true
        turnCtx, tc := context.WithCancel(a.ctx)
        rt.turnCancel = tc
        rt.mu.Unlock()

        emit(turn_start{it.TurnID}); engine.Run(turnCtx, Input{it.TurnID, it.Prompt}, emit)

        rt.mu.Lock(); rt.turnCancel = nil; rt.running = false; rt.mu.Unlock()
        tc()
    }
}
```

StartTurn（在 mu 下）：

```text
if rt.closed → ErrSessionClosed
if len(rt.pending) >= 64 → ErrQueueFull
it := inputItem{TurnID, Prompt, Behavior}
if behavior == "interrupt" {
    if rt.turnCancel != nil { rt.turnCancel() }
    rt.pending = append([]inputItem{it}, rt.pending...)
} else {
    rt.pending = append(rt.pending, it)
}
pos := len(rt.pending)                         // 1 基，1 = 下一个执行
notify := rt.running || pos > 1
→ 解锁后 signal wake；notify 时 emit turn_queued{turnID, position: pos}
```

取消与关闭：

```text
Engine.Cancel() → stateMu.RLock 取 turnCancel → cancel() → Run 内 ctx.Done 分支收尾
App.CancelTurn(sid):
    rt.mu.Lock()
      if rt.turnCancel != nil { rt.turnCancel() }          // 只取消当前轮
      queued := rt.pending; rt.pending = nil
    rt.mu.Unlock()
    for q in queued: emitter.Emit(turn_end{q.TurnID, canceled})   // 每个已返回的 turnID 都有终态
App.CloseSession(sid):
    rt.mu.Lock(); rt.closed = true; if rt.turnCancel != nil { rt.turnCancel() }; close(rt.quit); rt.mu.Unlock()
    broker.CancelSession(sid); engine.Close(); 从 a.sessions 删除
App.Shutdown(ctx):
    for each rt: CloseSession 的步骤（turnCancel + close(quit) + engine.Close）
    a.store.Flush(独立 3s 超时 ctx) → a.store.Close() → 清空 a.sessions
```

- `turnCancel` 每轮在 `mu` 下重建与清空；一次 `CancelTurn` 只影响当前轮，下一轮从 `a.ctx` 重新派生（评审第 2 轮第 3 条）。
- 关窗时 Wails 取消 `a.ctx`，所有派生轮次自动停止；`CloseSession` 负责单个会话。
- 队列 goroutine 在 `quit` 关闭后退出；`pending` 与 `closed` 只在 `mu` 下读写。
- 不使用 `context.WithoutCancel` 作为轮次 ctx（它不传播取消）；它只用于 transcript 写入的 `persistCtx`（§6.1）。

---

## 7. 上下文

### 7.1 系统提示组装

`Builder.System(ctx, toolSpecs)` 按固定顺序拼接（`strings.Builder`）：

```text
1. 身份与行为约定（固定文案，中文）
2. <env>
     cwd: /abs/path
     os: darwin/arm64
     date: 2026-10-23
     model: <model>
3. <project-instructions>
     从 cwd 逐级向上查找 AGENTS.md，取最近的；再找 CLAUDE.md，取最近的；
     两者都注入（带文件路径标注），各自截断到 MaxInstructionBytes（默认 32KB）
4. <git>
     branch / main branch / git status（截断 2000 字符）/ 最近 5 条提交
     git 不可用或超时（默认 2s）时整段省略，不报错
5. <tools>
     每行 "- Name: Description"；完整 schema 通过 API 的 tools 字段下发
6. Config.SystemExtra
```

缓存（评审第 23 条）：

- 静态层（第 1、2、3、5、6 段）以 `(cwd, 指令文件 mtime+size, 工具名列表, 日期)` 为键缓存；文件变化或日期翻转才重算。
- 动态层（第 4 段 git）每次 `System` 重新获取，但同一 turn 内复用：`agent.Run` 在每轮开始时调用 `Builder.BeginTurn()`，它只失效动态层，**不**清空静态缓存。
- `Invalidate()` 只在切换工作目录/模型时调用。

依据：cc 的 `getSystemContext` 是 git 分支/主分支/status/最近 5 条提交、status 超 2000 字符截断（cc.md:384-394）；Zed 的系统提示包含项目根、规则文件、工具清单与日期（zed.md:381-383）。

### 7.2 token 估算

```go
// 单字符成本：ASCII 记 0.25 token，非 ASCII 记 1 token，向上取整；再加块开销。
func EstimateString(s string) int
func EstimateMessages(msgs []llm.Message) int   // 每条消息 +4，每块 +6
```

估算用于触发压缩；provider 返回的真实 `Usage` 优先用于 warning 判定与展示。

### 7.3 阈值（含小窗口钳制）

| 名称 | 公式 |
|---|---|
| 摘要预留 | `reset = min(ReservedForSummary, max(2048, ContextWindow/4))` |
| 有效窗口 | `effective = max(4096, ContextWindow - min(MaxOutputTokens, reset))` |
| 自动压缩阈值 | `auto = max(effective/2, effective - AutoBuffer)` |
| 警告阈值 | `max(0, auto - 20000)` |
| 阻塞线 | `max(auto, effective - BlockingBuffer)` |
| 启用摘要压缩 | `ContextWindow >= 32000` |

依据与修正：常量来自 cc.md:319-337；评审第 19 条指出的负阈值问题由上述钳制解决。以 32k 窗口、8k 输出为例：`reset=8192`、`effective=24576`、`auto=12288`、`warning=0`、`blocking=21576`，全部为正。8k 窗口：`effective=6144`、`auto=3072`、`blocking=3144`，且 `AutoCompactEnabled()==false`，只做 microCompact，不再出现「摘要请求自身超窗」的必然失败。

### 7.4 microCompact

触发（满足任一，评审第 18 条补齐数据来源）：

1. `LastAssistantAt` 非零且距 `Now()` 超过 `MicroIdle`（默认 60 分钟）——认为服务端 prompt cache 已失效（cc.md:342）。冷恢复时 `LastAssistantAt` 来自 `session.Loaded`。
2. `EstimateMessages(msgs) >= 0.9*AutoCompactThreshold` 且 `<= AutoCompactThreshold-2000`——先做廉价减负（zcode.md:179）。

动作：

- 只处理工具结果块，保留最近 `KeepRecentToolResults`（默认 5）组，其余 `Text` 替换为 `"[old tool result cleared]"`。
- 工具结果所属的 `tool_use` 及其 assistant 消息不动，保证配对结构完整。
- 节省不足 256 token 时整体放弃（zcode.md:179）。
- 返回新切片，不修改入参。

### 7.5 autoCompact

触发（评审第 18 条）：`EstimateMessages(msgs) >= AutoCompactThreshold`，或 `LastUsage.Total() >= AutoCompactThreshold`（冷恢复的第一次调用就能用磁盘上最后一条 usage 判定）。

动作：

1. `BeforeCall` 一旦决定压缩，**先**回调 `progress(CompactPhaseStart)` → agent emit `compact_start`；并用 `defer` 保证函数返回前必回调 `CompactPhaseDone` 或 `CompactPhaseFailed`（评审第 2 轮缺失项 2）。agent 在 Failed 时 emit `compact_end{failed:true,error}`，前端一定能结束「压缩中」状态。
2. 用 `Provider.Stream` 发一次无工具、`MaxTokens=8000` 的请求，system 为压缩指令，消息为完整历史 + 固定摘要提示；摘要提示为五段式 `Goal / State / Context / Next / Pitfalls`（zed.md:356）。
3. 摘要请求返回 `context_overflow`：从最旧消息开始按「一整组（assistant + 其 tool_result）」删除后重试一次（codex.md:265）。
4. 摘要为空 → `consecutiveFailures++`，回调 Failed；连续 3 次失败后返回 `ErrCompactionDisabled`（cc.md:335、zcode.md:184）。
5. 成功 → `consecutiveFailures=0`，回调 `progress(CompactPhaseDone)`。

保留策略（`keepTail`）：

```text
从最后一条消息向前累计：
  - 至少保留 KeepMinMessages（默认 4）
  - 累计 token 至少 KeepMinTokens（默认 10000）
  - 最多 KeepMaxTokens（默认 40000）
  - 起点落在「只含 tool_result 的 user 消息」时再前移一条 assistant 消息
  - 最后一条用户输入永远保留
```

新历史 = `[user: "The conversation was compacted. Summary:\n"+summary] + 保留尾部`。压缩写入 `session.Compact{Trigger,PreTokens,PostTokens,Summary,Model}`，保留尾部随后作为普通 `message` 记录追加。

与参考实现的差异：cc 完整压缩不保留最近消息（cc.md:371-374），Zed 保留压缩点之前的用户消息（zed.md:360-374）。本设计保留最近完整上下文，便于恢复后继续未完成的工具链。

### 7.6 未纳入本期

- `reactiveCompact`（413 后压缩重试）：只保留 `ContextOverflow` 分类。
- `sessionMemoryCompact`：记忆模块未实现。
- cc 的 token 预算续跑（90% / 3 次 / 500 token，cc.md:115-127）。

---

## 8. 会话

### 8.1 存储位置、文件格式与安全

```text
<dataDir>/sessions/<sessionID>.jsonl     // 0600，目录 0700
```

**会话 ID 校验（评审第 16 条）**：`session.ValidateID` 只接受 `^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`。`FileStore` 在拼路径前校验，并额外用 `filepath.Rel(dir, path)` 确认结果不以 `..` 开头；任何非法 ID 返回 `ErrInvalidID`。`App.StartTurn` 先校验 `sessionID`，会话不存在时调用 `CreateSession` 写入 `session_meta`（标题取输入前 40 个字符），不存在「未校验就拼路径」的入口。

首行固定为 `session_meta`，由 `CreateSession` 写入：

```json
{"id":"rec_1","type":"session_meta","sessionId":"s_01","time":"2026-10-23T10:00:00Z","meta":{"workDir":"/repo","title":"重构登录","model":"claude-sonnet-4-5"}}
```

一条消息：

```json
{"id":"rec_7","type":"message","sessionId":"s_01","time":"2026-10-23T10:00:05Z","message":{"id":"msg_3","role":"assistant","time":"2026-10-23T10:00:05Z","blocks":[{"type":"text","text":"我先看一下文件。"},{"type":"tool_use","toolUseId":"toolu_1","toolName":"Read","input":{"file_path":"main.go"}}],"stopReason":"tool_use"}}
```

工具结果（一条 user 消息装全部结果）：

```json
{"id":"rec_8","type":"message","sessionId":"s_01","time":"2026-10-23T10:00:06Z","message":{"id":"msg_4","role":"user","time":"2026-10-23T10:00:06Z","blocks":[{"type":"tool_result","toolUseId":"toolu_1","text":"1\tpackage main\n..."}]}}
```

压缩边界：

```json
{"id":"rec_20","type":"compact","sessionId":"s_01","time":"2026-10-23T10:30:00Z","compact":{"trigger":"auto","preTokens":168000,"postTokens":21000,"summary":"...","model":"claude-sonnet-4-5"}}
```

轮次结束：

```json
{"id":"rec_21","type":"turn_end","sessionId":"s_01","time":"2026-10-23T10:30:02Z","turnEnd":{"turnId":"turn_9","stopReason":"completed","turns":4}}
```

写入语义（评审第 12 条）：

- `FileStore` 的唯一实例由 `App` 持有（`a.store`），所有会话与所有绑定调用共用，不存在「每个绑定调用各 NewFileStore」的多实例写同一文件问题（评审第 2 轮第 19 条）。
- `FileStore` 内部维护 `map[sessionID]*fileWriter`，每个文件一把锁 + 一个句柄；`Append` 在对应锁内一次 `Write` 写入「JSON + \n」，失败立即返回错误；不存在「只进内存队列、turn 末才落盘」的假写入。
- `Flush` 对全部打开文件做 `fsync`。**调用者是 agent**（turn 结束与压缩后，§6.1）；`App.Shutdown` 再做最后一次；W5 只提供实现，不主动调度。
- `Close` 关闭全部句柄并清空 map，只由 `App.Shutdown` 调用。
- 崩溃可能丢失的是「最后一次 `Flush` 之后的写」，不是整个 turn；用户消息在请求前已 `Write` 到内核。
- transcript 单文件读取上限 50MB（cc.md:442），超限只读尾部：先丢弃到第一个 `\n` 之后再开始 `Decode`；找不到换行说明尾部不是一条完整记录，返回 `ErrInvalidTranscript`（评审第 2 轮第 16 条）。

`session_meta` 的字段映射（评审第 2 轮第 16 条）：`Meta.ID → Record.SessionID`、`Meta.CreatedAt → Record.Time`、`Meta.Title/WorkDir/Model → Record.Meta["title"/"workDir"/"model"]`。`Record.Meta` 是唯一落盘形式，typed `Meta` 只是 `CreateSession` 的入参，两者由 `CreateSession` 单向转换，`Load` 时不会重建 typed `Meta`。

### 8.2 压缩边界语义

- `compact` 记录是**分界点**：它之前的所有消息在恢复时都不再载入。
- `Compact.Summary` 是完整摘要文本，恢复时变成历史第一条 user 消息。
- 边界之后的保留消息作为普通 `message` 记录继续追加，因此「摘要 + 尾部」自洽，不依赖边界之前的任何内容（codex.md:324）。
- 追加式文件不删除旧行，保留审计能力；恢复时按位置截断。

### 8.3 恢复重放

`Replay(recs)`：

1. 扫描 `compact` 记录，取**最后一条**作为边界；没有边界则从头开始。
2. `out[0] = user 消息(摘要)`；随后按序追加边界之后的 `message` 记录里的 `llm.Message`。
3. 按 §8.4 重建 tool_use / tool_result 配对。
4. 丢弃 `Blocks` 为空的 assistant 消息。
5. 最后一条 assistant 仍有未配对 `tool_use` → `Loaded.Interrupted=true`。
6. `usage` 记录用 `Usage.Sum` 累加进 `Loaded.Usage`；读取最后一条 `mode` 记录到 `Loaded.Mode`；`LastAssistantAt` 取最后一条 assistant 消息的 `Message.Time`（缺失时回退到该 `Record.Time`）。

`agent.Engine.Restore` 调用 `Replay` 后：若 `Interrupted`，在历史末尾追加 `"Continue from where you left off."`（cc.md:438），并把 `Loaded.Messages` 设为初始历史；若 `Loaded.Mode` 非空且与当前模式不同，由 app 决定是否套用（默认套用，保持会话一致）。

### 8.4 配对重建算法（评审第 25 条）

逐条扫描消息数组，对每个含 `tool_use` 的 assistant 消息做如下处理：

```text
ids := assistant 消息里 tool_use 的 ID 顺序列表
next := 下一个元素
若 next 是 user 消息且包含 tool_result：
    existing := map[ToolUseID]ContentBlock
    others   := 非 tool_result 的块（保持原顺序）
    blocks   := []
    for id in ids:                       // 严格按 tool_use 顺序重建
        if existing[id] 存在: blocks += existing[id]
        else:                 blocks += ToolResultBlock(id, true, "[interrupted before resume]")
    blocks += others                      // 文本等非结果块放到最后
    用 blocks 替换 next 的 Blocks
否则：
    在 assistant 之后插入一条新 user 消息，Blocks = ids 对应的合成错误结果
```

- 插入位置固定在对应 assistant 消息之后，不会跨越后续 assistant 消息。
- 多条 assistant 各自处理，互不影响。
- 已经存在但顺序错乱的结果会被重排为 `tool_use` 顺序（满足 §6.1 第 3 点的 API 约束）。

---

## 9. 内置工具与测试

### 9.1 内置工具清单

所有构造函数都返回 `tool.Tool`。路径经 `builtin.Resolve(root, p)` 归一化：

```text
1. root 在构造时做一次 filepath.EvalSymlinks，得到 rootReal；
2. 目标路径若是相对路径则 join(rootReal, p)，再 Clean；
3. 找到最深的已存在祖先，对它做 EvalSymlinks，再把不存在的尾部拼回；
4. rel := filepath.Rel(rootReal, resolved)；拒绝条件必须是：
     rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
   不能写成 strings.HasPrefix(rel, "..")：合法文件名 "..foo" 会被误杀
   （实测 Rel("/a/b","/a/b/..foo")=="..foo" 且 HasPrefix("..foo","..")==true）。
```

这样 root 内的软链接不能读写到 root 外（评审第 16 条）。

| 构造函数 | 输入 schema（字段） | ReadOnly | ConcurrencySafe | 权限默认 | 上限与行为 |
|---|---|---|---|---|---|
| `builtin.NewRead(root string) tool.Tool` | `file_path`(必填)、`offset`(≥0)、`limit`(1–2000)、`show_line_numbers`(bool) | 是 | 是 | allow | 文件 >256KB 拒绝；输出 >25000 token 截断；行号 `%6d\t`（cc.md:476-487）；实现 `PathAware` |
| `builtin.NewWrite(root string) tool.Tool` | `file_path`(必填)、`content`(必填) | 否 | 否 | ask（acceptEdits 目录内 allow） | 建目录；临时文件 + `os.Rename` 原子替换；实现 `PathAware` |
| `builtin.NewEdit(root string) tool.Tool` | `file_path`、`old_string`、`new_string`、`replace_all`(bool) | 否 | 否 | 同 Write | 非 replace_all 时 `old_string` 必须唯一命中；实现 `PathAware` |
| `builtin.NewGlob(root string) tool.Tool` | `pattern`(必填)、`path`(默认 root)、`max_results`(默认 200) | 是 | 是 | allow | `WalkDir` + `tool.MatchPath`（支持 `**`）；跳过 `.git`、`node_modules`；按 mtime 倒序 |
| `builtin.NewGrep(root string) tool.Tool` | `pattern`(必填，RE2)、`path`、`include`(glob)、`max_results`(默认 200)、`context_lines`(0–5) | 是 | 是 | allow | 跳过二进制（前 512 字节含 NUL）；输出 `path:line:text` |
| `builtin.NewBash(root string) tool.Tool` | `command`(必填)、`timeout_ms`(默认 120000，上限 600000)、`description` | 否 | 否 | ask | 解释器/工作目录/环境见下方；stdout/stderr 合并；>30000 字符截断（cc.md:489-497）；退出码非 0 → `IsError` |
| `builtin.NewTodoWrite() tool.Tool` | `todos[]{content,status(pending\|in_progress\|completed),activeForm}` | 否 | 否 | allow（工具实现 `tool.AlwaysAllow`，见 §5.2 第 3 步） | 内存态；返回渲染后的清单；不写文件系统 |

**Bash 的解释器、工作目录、环境与进程组（评审第 17 条 + 评审第 2 轮第 11 条）**：

| 项 | 契约 |
|---|---|
| Unix 解释器 | `/bin/bash -lc <command>`（`-l` 加载登录环境，`-c` 执行字符串）；`bash.go` 用 `exec.Command`，**不用** `exec.CommandContext` 的默认取消 |
| Windows 解释器 | `%COMSPEC%`（通常 `C:\Windows\System32\cmd.exe`）`/d /s /c <command>`；由 `proc_windows.go` 提供，路径分隔符按平台处理。本期不提供 PowerShell 工具 |
| 工作目录 | 一律 `cmd.Dir = root`（`builtin.Resolve` 后的会话工作目录） |
| 环境 | `os.Environ()` 过滤后保留 `PATH`、`HOME`/`USERPROFILE`、`LANG`、`TERM`、`TMPDIR`/`TEMP`；不注入 API Key 等应用配置；`PWD` 由 `cmd.Dir` 决定 |
| stdin | `nil`：交互式命令立即 EOF，不会挂住 |
| 取消 | `Setpgid: true`，`ctx.Done()` 时 `syscall.Kill(-cmd.Process.Pid, SIGKILL)`（整组）；Windows 用 `CREATE_NEW_PROCESS_GROUP` + `taskkill /T /F /PID` |
| 等待 | `cmd.Wait()` 在独立 goroutine，结果汇入 `waitDone`；`timeout_ms` 由 `context.WithTimeout` 实现 |

```go
// Unix（proc_unix.go）：
cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
go func() { select { case <-ctx.Done(): syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); case <-waitDone: } }()
// Windows（proc_windows.go）：SysProcAttr.CreationFlags = CREATE_NEW_PROCESS_GROUP
// 取消时 exec.Command("taskkill","/T","/F","/PID", strconv.Itoa(cmd.Process.Pid)).Run()
```

依据：`exec.CommandContext` 默认的 `Cancel` 只调 `cmd.Process.Kill()`（`$(go env GOROOT)/src/os/exec/exec.go:484-494`，本次已读），只杀 shell 本身，派生的子进程会成为孤儿。

### 9.2 工具结果如何产生

```text
tool_use(id="toolu_1", name="Read", input={"file_path":"a.go"})
   ↓ agent.tools.go
0. registry.Lookup("Read")           → 未找到 → Result{IsError:true, "unknown tool: Read"}
1. AffectedPaths（若实现 PathAware）  → 失败   → Result{IsError:true, "invalid path: ..."}
   NoApproval := t.(tool.AlwaysAllow).AlwaysAllow()（未实现则为 false）
2. json.Valid(input)                 → 非法   → Result{IsError:true, "invalid input json: ..."}
3. engine.Check(req{Paths, WriteFiles, NoApproval}) → deny → Result{IsError:true, "permission denied: "+reason}
                                     → ask    → §5.4（取消 → IsError "canceled while awaiting approval"）
4. tool.Call(ctx, t, input, progress)→ Run 内 error / panic / ctx 取消 → IsError Result
5. 成功                              → Result{Content, Display}
   ↓
llm.ToolResultBlock("toolu_1", res.IsError, res.Content)
```

`tool.Call` 的 `recover` 只捕获工具实现的 panic，并在结果里带上工具名与 panic 值（错误字符串小写、无结尾标点）。

### 9.3 测试策略（全部离线）

| 包 | 测试文件 | 覆盖点 | 手段 |
|---|---|---|---|
| llm | `llm_test.go` | 两种适配器的完整映射；**OpenAI 在 finish_reason 与 [DONE] 两处补 `tool_use_stop`**；**块索引唯一性（文本 + 工具不共用 Index）**；503 后成功；413 不重试；`Retry-After`；首字节超时；空闲超时；ctx 取消；**Close 后无残留 goroutine**（写入通道被填满时 Close 仍立即返回） | `httptest.NewServer` + 手写 SSE 帧；`runtime.NumGoroutine` 前后对比 |
| tool | `tool_test.go` / `group_test.go` / `match_test.go` | 未知工具、非法 JSON、error、panic、ctx 取消都返回 `IsError`；分批顺序与上限；`MatchPath` 的 `**`/`*`/`?`/字符类/非法模式 | 自建 fake `Tool` |
| builtin | `builtin_test.go` | Read 截断与行号；Write 原子性；Edit 唯一匹配；Glob 的 `**` 递归；Grep 跳过二进制；Bash 超时后**子进程组不残留**；TodoWrite；路径越界与软链接逃逸拒绝 | `t.TempDir()`；Bash 跑 `echo`/`false`/`sleep` 与 `sh -c 'sleep 30'` |
| permission | `permission_test.go` | 5 模式 × (read/write/bash/todo) 矩阵；plan 下 ask 规则不能放行写工具；**NoApproval 在任何模式下 allow，且不依赖工具名**；**dontAsk 下 ask 规则与应用默认都收口为 deny、永不返回 ask**；deny 优先于 bypass；allow 的「全部匹配」；acceptEdits 目录内外（Paths 注入）；**复合命令不匹配 prefix 规则**；**MatchMode 为空被拒绝**；**绝对 Paths 先转相对再匹配**；Broker 的 `Prepare(view)`/Deliver 竞态、**ctx 取消与 Deliver 并发不死锁**、重复 Deliver、Pending 返回完整视图、按会话 CancelSession | 表驱动，纯内存；并发用例配 `-race` 与超时断言 |
| session | `session_test.go` | `CreateSession` 写首行（Meta→Record 映射，Replay 重建 `Loaded.Meta`）；Append 同步错误传播；边界截断；摘要注入；**配对重建顺序**；中断标记；并发 Append 无交错；非法 ID 拒绝；**50MB 尾部从首个换行开始解析**；**usage 记录 Sum 后等于各步之和** | `t.TempDir()` + 并发 goroutine |
| context | `context_test.go` | 阈值公式与 Status（**8k/32k/200k 三档**）；microCompact 只动旧结果、节省不足放弃、`LastAssistantAt` 触发；autoCompact 保留边界、失败熔断；**progress 在摘要前 Start、失败时 Failed**；system 含 AGENTS.md 与 git 段、`BeginTurn` 不清静态缓存；`ReadFile`/`RunGit` 注入后不触盘不联网 | fake Provider；`Now`/`ReadFile`/`RunGit` 注入 |
| agent | `agent_test.go` / `tools_test.go` / `approval_test.go` | 单轮完成、多轮续跑、max_turns；**Run 期间并发 History 无竞争**；Cancel 不被 runMu 阻塞；取消补结果且立即返回；**取消后 tool_result 仍用 persistCtx 落盘**；**跨批次 results 按全局 Index 回填（两批调用不串槽）**；并发批真并行（最大并发计数）；**进度事件实时转发且 emit 只来自 turn goroutine**；未知工具；权限拒绝；ask 四路径（正常应答与取消都发 permission_resolved）；**panic 转 StopError 且 turn_end 是最后一个事件**；**多步 usage 用 Sum 累加并写入 RecordUsage**；**Restore 后 lastUsage 非零** | `llmtest.Provider` 脚本化响应 + `MemStore` + 记录 emit 调用 goroutine 的探针。**agent 包的 Event.Seq 恒为 0，不断言 seq** |
| app | `app_test.go` | DTO 转换（Input 为 any）；**Emitter 全局 seq 与并发串行化（仅此处断言 seq）**；**NewWithDeps 注入 fake Provider 跑完整一轮**；Shutdown 取消 Run 并 Close Store；StartTurn 的 queue/interrupt 与 `turn_queued.position`；排队项被取消时收到 `turn_end{canceled}`；RespondPermission 的会话归属与选项校验；GetPendingApprovals 返回完整视图 | `NewWithDeps` + fake Provider + fake Sender，不启动 Wails |

`llmtest.Provider` 契约（W1 先落地，W6/W7 依赖）：

```go
// Package llmtest 提供脚本化的 llm.Provider，供离线测试使用。
package llmtest

// Step 是一次 Stream 调用的脚本：依次产出 Events，最后以 Err 结束（nil 表示 io.EOF）。
type Step struct {
	Events []llm.StreamEvent
	Err    error
	Delay  time.Duration
}

// Provider 是按队列产出 Step 的假实现。
type Provider struct{ /* ... */ }

// New 创建一个依次返回 steps 的 Provider；队列耗尽后返回空流。
func New(steps ...Step) *Provider

// Queue 追加脚本。
func (p *Provider) Queue(steps ...Step)

// Name 返回 "llmtest"。
func (p *Provider) Name() string

// Stream 实现 llm.Provider。
func (p *Provider) Stream(ctx context.Context, req llm.Request) (llm.Stream, error)

// Calls 返回已收到的请求快照（深拷贝），供断言 system 提示、消息顺序与工具规格。
func (p *Provider) Calls() []llm.Request
```

验收命令（实现阶段执行）：`gofmt -l .`、`go vet ./...`、`go test -race ./...`。

本轮实际执行（评审第 2 轮缺失项 8）：`go vet ./...` 返回 0；`go test ./...` 输出 `? b-desktop [no test files]` 与 `ok b-desktop/internal/app (cached)`。仓库 `find internal -type f` 只有 `internal/app/app.go` 与 `internal/app/app_test.go`，llm/tool/permission/session/context/agent 六个包尚不存在，因此上表中的包级测试**尚无可测对象**，本轮未运行 `go test -race ./...`。

---

## 10. 明确不做

| 项 | 本期状态 | 说明 |
|---|---|---|
| SubAgent / Agent Teams | 不做 | 无 `internal/subagent`、无 `Agent` 工具；`Config` 与 `EventType` 不预留子 Agent 字段。**这是对 `AGENTS.md` 约束 7 的显式偏离**，已在 §11.3 登记 |
| MCP | 不做 | 无客户端、无传输；`Registry` 不做动态注册 |
| Skills | 不做 | 系统提示不列 skill 清单 |
| Hooks | 不做 | 工具流水线为「校验 → 权限 → 执行」，不含 `PreToolUse`/`PostToolUse` |
| 定时任务 | 不做 | 无 cron |
| IM 适配 | 不做 | 无 `internal/im` |
| Computer Use | 不做 | 无截图与键鼠工具 |
| 记忆（MEMORY.md） | 不做 | 系统提示不注入记忆 |
| 图片/多模态 | 不做 | `ContentBlock` 只有文本类块 |
| token 预算续跑 | 不做 | 只有压缩，没有「花完预算再续」 |
| 沙箱（Seatbelt/Landlock） | 不做 | 权限只做模式与规则；Bash 用进程组 + 超时兜底 |
| 跨会话规则持久化 | 不做 | always allow/deny 只存会话内存 |
| reactive compact | 不做 | 只保留错误分类，不自动补救 |
| 模型 fallback / 流中途换模型 | 不做 | 见 §4.3 第 5 点 |
| 工具结果去重（Read 缓存） | 不做 | 每次调用都真实读取 |
| 流式工具提前执行 | 不做 | 工具在流结束后统一执行 |

---

## 11. 实施顺序、风险与偏离

### 11.1 顺序

```text
第 0 步（1 人）：
  0a. 冻结契约 —— 本文 §2 的签名先落成带 doc 注释的空实现骨架；
  0b. 同步修正 AGENTS.md（评审第 2 轮缺失项 4）：把「每个工具都要定义 checkPermissions
      与结果到 API block 的映射」改为「权限集中在 internal/permission，结果映射集中在
      internal/agent，工具只声明 ReadOnly/ConcurrencySafe/PathAware」。两份文档不得长期矛盾。
第 1 批（7 路并行）：W1 llm / W2 tool / W3 builtin / W4 permission / W5 session / W6 context / W7 agent
第 2 批（1 路）：W8 app 装配与绑定
第 3 批：端到端冒烟（httptest + llmtest，不联网）
```

编译依赖（评审第 2 轮第 14 条）：W3→W2，W4→W2（`tool.MatchPath`），**W5→W1（`session` 引用 `llm.Message`）**，W6→W1+W2，W7→全部，W8→全部。由于第 0 步已把骨架落地，所有流都能同时编译；不存在「W5 无内部依赖」的说法。

### 11.2 风险

| 风险 | 影响 | 缓解 |
|---|---|---|
| 无 JSON Schema 校验库 | 模型给的参数可能缺字段 | 每个工具在 `Run` 开头 `DecodeInput` + 显式必填校验；错误变成 `tool_result` |
| token 估算偏差 | 压缩触发过早或过晚 | 真实 `Usage` 覆盖估算；估算只作触发信号 |
| Wails `EventsEmit` 并发无序 | 前端事件乱序 | 单个 `Emitter` 全局互斥 + 全局 `seq`（`events.go:56-61`、`darwin/frontend.go:385-396`） |
| Wails 关闭后继续发事件 | 向已销毁窗口投递 | 轮次 ctx 由 `WithCancel(a.ctx)` 派生；`Shutdown` 取消全部轮次 |
| Bash 无沙箱 | 可执行任意命令 | 默认 ask；内置硬拒绝；进程组终止；文档明确不含沙箱 |
| 长会话 transcript 过大 | 恢复变慢 | 压缩边界截断；50MB 读取上限 |
| 工具 goroutine 卡在 ctx 不敏感的调用 | 取消后仍占用资源 | 取消时立即返回不等它；done 通道有缓冲，迟到结果被丢弃 |
| 本地模型窗口未知 | 被当成 200k，永不压缩，首次请求就 400 | 必须设置 `ANC_CONTEXT_WINDOW`；`GetConfig` 在 ContextWindow 取默认值且模型未知时给出提示 |
| 内置硬拒绝可被绕过 | 误以为有安全边界 | 文档明确它是纵深防御；真正的边界是权限模式与人工审批 |
| 会话 Store 多实例 | 同文件多句柄、锁不互斥、fd 泄漏 | 进程内唯一 `a.store`，runtime 只引用 |
| `internal/context` 与标准库同名 | 误导入 | 全仓统一别名 `sysctx` |

### 11.3 与参考实现 / AGENTS.md 的偏离（必须知情）

| 项 | 参考实现 / AGENTS.md | 本设计 | 原因 |
|---|---|---|---|
| 工具级 `checkPermissions` | `AGENTS.md:83` 要求每个工具定义 `checkPermissions`；cc 在 `Tool.ts:500-503` 有该方法 | **上移到 `permission.Engine`**，工具只提供 `ReadOnly`/`ConcurrencySafe`/`AffectedPaths`/`AlwaysAllow` | 五档模式是全局策略，放在工具里会重复实现；路径安全通过 `PathAware` 保留在工具侧。这是对 AGENTS.md 的显式偏离，**第 0b 步必须同步修改 AGENTS.md**（评审第 2 轮缺失项 4） |
| SubAgent 独立上下文（AGENTS.md 约束 7） | `AGENTS.md:51` 要求 SubAgent 有独立上下文、后台运行、取消与恢复 | **本期不实现**：`Config` 不预留字段，`Event` 不预留子 Agent 事件，`internal/subagent` 不存在 | 按本轮任务的第 10 条「明确不做」执行。后续加回时需要修改已冻结的 `Config` 与 `EventType`，这被登记为已知代价（评审第 3 轮缺失项 1） |
| 模型请求失败回灌（AGENTS.md 约束 4） | `AGENTS.md:48` 要求模型请求失败也转成结构化结果回灌，由模型决定下一步 | provider 内部按 §4.4 有界重试；耗尽后本轮 `error + turn_end{error}` 结束，**不**回灌成 user 消息 | 失败时没有新的 tool_result 可回灌，把同一请求再喂给同一 provider 只会无界重试；用户可见错误后自行决定重发（评审第 3 轮缺失项 2） |
| 结果到 API block 的映射 | `AGENTS.md:83` 要求工具定义映射 | **集中在 agent**：`Result → llm.ToolResultBlock` | 单一实现，避免 7 个工具各写一份 |
| 并发分组依据 | cc 用 `isConcurrencySafe`；codex 用单布尔 + 读写锁；zed 全并发 | `ConcurrencySafe` + 权限预检 | 保留「只读并行、写串行」，同时避免审批风暴 |
| 主循环轮次上限 | cc 有 `maxTurns`；codex/zed/zcode 无 | 可配置，app 默认 100 | 满足 `AGENTS.md` 的「受 maxTurns 约束」，桌面端防失控 |
| 压缩后保留 | cc 完整压缩不保留；zcode 保留最近 1 轮；zed 保留旧用户消息 | 摘要 + 最近 4 条 / 10k–40k token | 恢复后可继续未完成的工具链 |
| microCompact 触发 | cc 仅时间触发 | 时间或接近阈值 | 兼顾缓存失效与真实容量 |
| 权限审批位置 | zed 在工具内部；cc/codex 在工具执行前统一判定 | 统一前置 + 预检 | 集中判定便于测试与展示 |
| 持久化 | cc/codex 追加 JSONL；zed 全量快照 | 追加 JSONL | 崩溃可恢复、写入成本低 |
| tool 结果去重、流式提前执行 | zed/codex 有 | 不做 | 本期范围外 |

---

## 12. 评审修订记录

### 12.1 实测依据

本次实际执行并在修订中引用的验证：

```bash
$ cd /tmp/ancverify && go run main.go
Match("src/**","src/a/b.go")=false err=<nil>
Match("**/*.go","a.go")=false err=<nil>
Match("src/*","src/a.go")=true err=<nil>
WithCancel: root err = context canceled derived err = context canceled
WithoutCancel: root err = context canceled derived err = <nil>
value survives WithCancel: events
```

```bash
$ grep -n "func CommandContext" -A 18 "$(go env GOROOT)/src/os/exec/exec.go"
484:func CommandContext(ctx context.Context, name string, arg ...string) *Cmd {
...
490:	cmd.Cancel = func() error {
491:		return cmd.Process.Kill()
492:	}
```

结论：`filepath.Match` 的 `**` 既不递归也不跨段；`WithoutCancel` 不传播取消但 `WithCancel` 传播且保留 value；`exec.CommandContext` 默认只 Kill 目标进程本身。

第 2 轮补充实测：

```bash
$ cd /tmp/ancverify && go run v2.go
second registered runs first
first registered runs last
Rel= "..foo" err=<nil> HasPrefix(..)=true
escape Rel= "../x"
```

```bash
$ cd /tmp/ancverify && go run v3.go
byte-truncated valid=false runes="这是一段很长的中文标题用于\xe6"
rune-truncated valid=true runes="这是一段很长的中文标题用于测试截断行为"

$ cd /Users/baike3/CodeRepo/b-desktop && go vet ./...
# 输出为空，退出码 0

$ go test ./...
?   	b-desktop	[no test files]
ok  	b-desktop/internal/app	(cached)
```

结论：`defer` 是 LIFO（先注册后执行），所以 turn_end 不能拆成两个 defer；`filepath.Rel` 的合法结果可以是 `..foo`，判界必须比 `".."` 与 `".."+Separator`；按字节截断中文会产生非法 UTF-8，标题必须按 `[]rune` 截断；当前仓库只有 `internal/app` 一个包，`go vet ./...` 通过、`go test ./...` 只跑到 app。

第 3 轮补充实测：

```bash
$ cd /tmp/ancverify && go run v4.go
WithoutCancel err = <nil> value = events
```

结论：`context.WithoutCancel` 不传播取消但保留 value，正适合做「用户取消后仍要完成 transcript 写入」的 `persistCtx`。

### 12.2 第 1 轮逐条处置

| # | 评审意见 | 处置 | 位置 |
|---|---|---|---|
| 1 | OpenAI 缺 `tool_use_stop` | 适配器在 finish_reason 与 `[DONE]` 两处补发；agent 在 `message_stop` 再兜底关闭 | §4.2、§6.3 |
| 2 | OpenAI 索引空间冲突 | 适配器自分配唯一单调块索引；`StreamEvent` 增加 `Kind`；agent 用 `(Index, Kind)` 复合键 | §2.1、§4.2、§6.3 |
| 3 | Compactor 无法实时发事件 | `BeforeCall` 增加 `CompactProgressFunc`；agent 在回调里 emit `compact_start` | §2.5、§7.5 |
| 4 | Broker 死锁 | 通道容量固定 1；Deliver 持锁非阻塞单次发送；清理与投递用 `closed` 保证只关一次；等待方不持锁 | §2.3、§5.4 |
| 5 | SSE 孤儿 goroutine / Close 卡死 / ctx 进结构体 | 读取侧所有发送都 `select done`；`Recv(ctx)`；`Close` 先 close(done) 再等待；强制 `defer Close` | §2.1、§4.4 |
| 6 | Engine 并发契约不成立 | `runMu.TryLock` + `stateMu` 分离；`Cancel` 不经过 runMu；`History` 返回深拷贝 | §2.6 |
| 7 | EventFunc 并发契约矛盾 | 明确「只从 turn goroutine 调用」；工具进度经 channel 汇入 | §2.6、§6.5 |
| 8 | plan 模式语义矛盾 | plan 分支移到 ask 规则之前；Bash 在 plan 下恒 deny（无分类器） | §5.1、§5.2 |
| 9 | 复合命令的前缀匹配论证错误 | 删除错误论证；`MatchExact` 用于整串，`MatchPrefix` 仅对无元字符命令生效，否则 fail-closed | §5.2 |
| 10 | `**` 无法用 `filepath.Match` | 新增 `tool.MatchPath` 递归匹配；Glob 与路径规则统一使用 | §2.2、§5.5、§9.1 |
| 11 | `WithoutCancel` 取消失效 | 改为 `context.WithCancel(a.ctx)`；补充 `Shutdown` 清理契约 | §2.7、§6.6 |
| 12 | Append 缓冲语义与落盘承诺矛盾 | `Append` 改为同步写入并返回错误；Flush 只做 fsync | §2.4、§8.1 |
| 13 | 事件顺序与实现矛盾 | `message_stop` 在工具执行前；`tool_result` 完成即发；重写不变量 | §3.2、§6.1、§6.5 |
| 14 | panic 未恢复 | `Run` 增加 `defer recover` → `StopError` + `turn_end`；工具 goroutine 也各自 recover | §6.4、§6.5 |
| 15 | seq 全局性与 Emitter 不可测 | 单个全局 `Emitter`；新增可注入 `Sender`；`NewWithSender` | §2.7、§3.1、§9.3 |
| 16 | sessionID 穿越 / 软链接逃逸 | `ValidateID` + Rel 校验；`Resolve` 做 EvalSymlinks 后判界 | §2.4、§8.1、§9.1 |
| 17 | Bash 只杀 shell | Unix `Setpgid` + `kill(-pgid)`；Windows `taskkill /T` | §9.1 |
| 18 | 压缩触发拿不到 usage / 时间 | `BeforeCallInput` 携带 `LastUsage`；`Loaded` 携带 `LastAssistantAt` | §2.4、§2.5、§8.3 |
| 19 | 小窗口负阈值 | `NewBudget` 全程钳制；`ContextWindow < 32000` 关闭摘要压缩 | §2.5、§7.3 |
| 20 | 半截流无法丢弃 | 新增 `message_discard` 事件；放弃的消息不落盘 | §2.6、§3.2、§6.3 |
| 21 | always allow 规则不可控 | `RespondPermission(sessionID, requestID, option)` 不收 Pattern；由 agent 按工具类型推导并显式指定 MatchMode | §5.6、§2.7 |
| 22 | 同会话第二条输入报错 | `StartTurn` 增加 behavior：`queue`（默认）/ `interrupt`，并新增 `turn_queued` 事件 | §2.7、§3.2 |
| 23 | 系统提示缓存自相矛盾 | 静态层/动态层分离；`BeginTurn` 只失效动态层 | §2.5、§7.1 |
| 24 | usage 丢输入 token | 区分 `Usage.Merge`（单消息分段合并）与 `Usage.Sum`（多步累加）；`usage` 事件发整轮累计 | §2.1、§4.3、§6.3 |
| 25 | Replay 孤儿结果插入位置未定义 | 新增「按 tool_use 顺序整条重建 user 消息」算法 | §8.4 |
| 26 | permission 重复路径逻辑 | 新增 `tool.PathAware`；agent 填充 `Request.Paths` 与 `WriteFiles`；permission 只做相对化与 MatchPath | §2.3、§5.5 |
| 27 | 取消后事件不闭合 | 取消时立即 finalize 并返回；`doneCh` 有缓冲，迟到结果丢弃 | §6.5 |
| 28 | OpenAI name 累积与 signature 转发 | name 为空保留旧值；新增 `EventSignature`，agent 只存不转发 | §4.1、§6.3 |
| 29 | W8 前端 DTO 缺失 | 新增 §2.8 完整 DTO 定义 | §2.8 |
| 30 | ResumeSession / 会话释放缺失 | 补齐 `ResumeSession`、`CloseSession`、`DeleteSession`；`Shutdown` 统一清理 | §2.7 |
| 31 | Store 缺会话创建 / meta 写入 / mode 读取 | 增加 `CreateSession`、`Delete`、`ErrInvalidTranscript`；`Replay` 读取 mode | §2.4、§8.1、§8.3 |
| 32 | Tool 契约缺 `checkPermissions` 与 block 映射 | 在 §11.3 显式记录为偏离及替代方案 | §11.3 |
| 33 | App.Shutdown 无契约 | 定义取消轮次 → Flush → Close → 清表，且幂等 | §2.7、§6.6 |
| 34 | 配置与默认值缺失 | §2.7 新增 `Config`/`LoadConfig` 与 `ANC_*` 变量表（含缺省值与缺失行为）；无密钥允许启动，`StartTurn` 返回错误 | §2.7 |
| 35 | Thinking 校验与未知模型能力 | 请求前校验 budget/temperature；未知模型 `SupportsTools=true` | §2.1、§4.1 |
| 36 | 缺作废/回滚语义 | 同第 20 条；并明确本期不做模型 fallback | §4.3、§6.3 |
| 37 | 装配冒烟与直连 EventsEmit 冲突 | 同第 15 条：`Sender` 注入 | §2.7、§9.3 |

### 12.3 第 1 轮核对结论

第 1 轮 21 条阻断、7 条应修、9 条缺失**全部成立**，共 37 条，没有需要反驳的条目。其中第 10 条（`filepath.Match` 不支持 `**`）、第 11 条（`WithoutCancel` 不传播取消）、第 17 条（`CommandContext` 只杀自身）已在本次实测复核，输出见 §12.1。

### 12.4 第 2 轮逐条处置

| # | 评审意见 | 处置 | 位置 |
|---|---|---|---|
| B1 | 复合键 (Index, Kind) 与 Kind 为空的工具事件冲突，参数丢失 | `Kind` 改为块级事件必填；`tool_input_delta`/`tool_use_stop` 在 §4.1/§4.2 全部标注 `Kind=tool_use`；agent 对空 Kind 先按 Index 查已打开块，查不到就丢弃并记 error | §2.1、§4.1、§4.2、§6.3 |
| B2 | 两个 defer 的 LIFO 顺序让 turn_end 早于 error 或重复 | 合并为单一收尾 defer：先 recover（emit error）再 `if !turnEndSent` emit turn_end；明确禁止两个各自发 turn_end 的 defer | §6.1 |
| B3 | sessionRuntime 的 cancel 生命周期未定义；inputs 关闭未定义 | 每轮从 `a.ctx` 派生 `turnCtx`，`turnCancel` 在 mu 下重建/清空，不保存 ctx；`inputs` 永不关闭，用 `quit` 退出，`closed` 标志让 StartTurn 返回 `ErrSessionClosed` | §2.7、§6.6 |
| B4 | `Usage.Add` 的覆盖语义与轮次/会话累加冲突 | 拆成 `Merge`（单消息分段覆盖）与 `Sum`（多步累加）；单消息用 Merge，整轮与会话用 Sum | §2.1、§4.3、§6.3、§8.3 |
| B5 | MatchMode 默认值需要工具分类，与 permission 不认识工具名矛盾 | `MatchMode` 改为必填，删除「按工具默认」；agent 在生成 always 规则时显式写入，permission 只按 MatchMode 执行 | §2.3、§5.6 |
| B6 | agent.Config 无 Budget，阻塞线与压缩器阈值会漂移 | Config 删除 MaxOutputTokens，新增 `Compactor.Budget()` 作为唯一预算来源；循环里 `budget := compactor.Budget()` | §2.5、§2.6、§6.1 |
| S1 | §5.6「其他工具」用工具名当 MatchJSON 的 Pattern，永不命中且可能误命中 | 改为 `MatchJSON + Pattern=""`（整类放行）；明确 Bash 禁止空 Pattern | §5.6 |
| S2 | 排队轮次被取消时无终止事件 | `CancelTurn` 对每个丢弃的排队项 emit `turn_end{canceled}`；新增 `turn_queued` 事件 | §2.7、§3.2、§6.6 |
| S3 | §3.2 不变量把 tool_input_delta 放在 tool_call_start 之前 | 改为按块成序：文本块 / 思考块 / 工具块各自 `start → delta* → ready`，块之间可交错 | §3.2 |
| S4 | §6.5 伪代码 progressCh 声明两次 | 删除重复声明 | §6.5 |
| S5 | Bash 的 shell、cwd、环境与 Windows 解释器丢失 | 新增契约表：Unix `/bin/bash -lc`、Windows `%COMSPEC% /d /s /c`、`cmd.Dir=root`、环境白名单、stdin=nil | §9.1 |
| S6 | 硬拒绝被写成不可绕过的安全边界 | 降级为「纵深防御」：归一化 + 有限模式表，明确 `rm -rf /*`、变量拼接、脚本内命令不保证拦截 | §5.1、§5.2 |
| S7 | 文件 glob 用相对 Pattern 匹配绝对 Paths | 明确先 `filepath.Rel(WorkDir, p)` 再 `ToSlash`；绝对路径不能直接喂 `MatchPath` | §5.5 |
| S8 | content_block_start 的 text/thinking 映射没有 EventType | 新增 `EventBlockStart`（带 Kind），Anthropic 与 OpenAI 都改用它 | §2.1、§4.1、§4.2 |
| S9 | Resolve 的「以 .. 开头」误杀 `..foo` | 改为 `rel == ".." \|\| HasPrefix(rel, ".."+Separator)`，并附实测 | §9.1 |
| S10 | MessageDTO.TimeMS 无数据来源 | `llm.Message` 增加 `Time`，写入 JSONL，Replay 回填；DTO 从它取 UnixMilli | §2.1、§8.1、§2.8 |
| S11 | lastUsage 更新时机晚一步 | 每条 assistant 消息完成后立即 `lastUsage = lastUsage.Sum(msg.Usage)`，下一步 BeforeCall 就用新值 | §2.6、§6.1 |
| S12 | ListSessions/LoadSession 与每会话 FileStore 的关系未定义 | App 持有进程内唯一 `a.store`；FileStore 内部按 session 维护句柄；runtime 只引用不 Close | §2.4、§2.7、§8.1 |
| S13 | RespondPermission 无会话归属/选项校验，无法撤销规则 | 签名改为 `(sessionID, requestID, option)`；校验 option 与归属；新增 `RemoveRule`、`GetPendingApprovals` | §2.7 |
| S14 | §11.1 编译顺序与依赖表矛盾 | 明确 W5→W1，并说明所有流都基于第 0 步骨架编译 | §11.1 |
| S15 | agent 测试断言 seq，但 seq 由 app 分配 | agent 测试改为只断言顺序；seq 只在 app 测试中断言 | §9.3 |
| S16 | Meta/Record.Meta 重复、50MB 尾部不从换行开始、40 字节切中文 | 给出 Meta→Record 映射表；尾部从首个 `\n` 后解析；标题按 `[]rune` 截断 40 字符 | §8.1、§2.7 |
| M1 | 本地模型窗口无法覆盖 | 新增 `ANC_CONTEXT_WINDOW` 与 `Config.ContextWindow`，并说明未知模型的 200k 默认值风险 | §2.7 |
| M2 | 压缩失败没有进度终态 | 新增 `CompactPhaseFailed` 与 `CompactView.Failed/Error`；BeforeCall 用 defer 保证 Start 后必有 Done/Failed | §2.5、§7.5、§2.6 |
| M3 | 前端无法枚举待审批 | 新增 `GetPendingApprovals(sessionID)` 与 `Broker.Pending(sessionID)` | §2.3、§2.7 |
| M4 | AGENTS.md 偏离未闭合 | 第 0b 步强制同步修改 AGENTS.md；§11.3 标注 | §11.1、§11.3 |
| M5 | App 级 Store 所有权缺失 | 同 S12：进程内唯一 `a.store` | §2.7 |
| M6 | 排队轮次状态事件缺失 | 新增 `turn_queued`（含 position）与排队取消的 `turn_end` | §2.6、§3.2 |
| M7 | Message 缺时间戳 | 同 S10 | §2.1 |
| M8 | 本轮未执行实现级检查 | §9.3 记录本轮实际执行的 `go vet ./...`（0）与 `go test ./...`（仅 app）；说明其余包尚不存在 | §9.3、§12.1 |

### 12.5 第 2 轮核对结论

第 2 轮 6 条阻断、16 条应修、8 条缺失**全部成立**，共 30 条，没有需要反驳的条目。其中「defer 是 LIFO」「`Rel` 对 `..foo` 返回 `..foo`」「按字节截断中文产生非法 UTF-8」已在本次实测复核，输出见 §12.1。

### 12.6 第 3 轮（终审）逐条处置

| # | 评审意见 | 处置 | 位置 |
|---|---|---|---|
| F1 | §6.5 伪代码用批次内下标分配 results，跨批次串槽 | `results` 按**全局** `len(allCalls)` 分配一次；`start`/回填/等待全部使用 `tool.Call.Index`；明确禁止 `range batch.Calls` 的 `i`；`finalizeResults` 同用全局下标 | §6.5、§2.2 |
| F2 | GetPendingApprovals 没有数据来源 | `Broker.Prepare(view ApprovalView)` 保存完整视图（requestId/sessionId/toolName/input/reason/options）；`Pending(sessionID) []ApprovalView`；App 转 `ApprovalViewDTO` | §2.3、§2.7 |
| F3 | usage / turn_end 没有写入者，Restore 不回填 lastUsage | agent 写 `RecordUsage`（每步）、`RecordTurnEnd`（每轮）；`Restore` 把 `Loaded.Usage`→`lastUsage`、`Loaded.LastAssistantAt`→`lastAssistantAt` | §6.1、§2.6、§8.3 |
| F4 | 恢复取不到 session_meta，模型/预算不确定 | `Loaded.Meta` 由首行重建；`ResumeSession` 定义 model 解析顺序与 per-session Budget | §2.4、§8.1、§2.7 |
| F5 | TodoWrite 默认权限自相矛盾且权限层硬编码工具名 | 新增 `tool.AlwaysAllow` 与 `Request.NoApproval`；§5.2 第 3 步按标记放行，plan 分支不再写工具名 | §2.2、§2.3、§5.2、§9.1 |
| F6 | dontAsk 只收口第 8 步的 ask，ask 规则仍弹窗 | 第 10 步改为「最终结果为 ask（任一来源）一律转 deny」；dontAsk 永不返回 ask | §5.1、§5.2 |
| F7 | DTO 的 json.RawMessage 在 TS 里成为 number[] | `BlockDTO.Input`、`ApprovalViewDTO.Input` 改为 `any`；附 Wails typescriptify 依据 | §2.8 |
| F8 | 取消后落盘使用已取消的 ctx | 新增 `persistCtx := WithTimeout(WithoutCancel(ctx), 5s)`，所有 transcript 写入与 Flush 使用它 | §6.1、§6.4 |
| F9 | 归属表缺 GetPendingApprovals/RemoveRule、0b、前端类型文件 | 更新 bindings_permission.go 职责；归属表新增 `frontend/src/types/agent.ts`、`AGENTS.md`、本文档三行 | §1.8、§1.9 |
| N1 | SubAgent 没有任何落点 | §10 与 §11.3 显式登记为对 AGENTS.md 约束 7 的偏离，并写明后续加回需改 Config/EventType 的代价 | §10、§11.3 |
| N2 | 模型请求失败未回灌，且未登记偏离 | §6.1 第 8 条说明边界；§11.3 新增偏离行（重试耗尽后结束本轮） | §6.1、§11.3 |
| N3 | 正常应答后不发 permission_resolved | §5.4 在应答与取消两条分支都发，`text` 为选项或 `canceled` | §5.4、§3.2 |
| N4 | usage 事件发出方式冲突 | 统一为 `message_stop` 后紧跟一条独立 `usage` 事件；`message_stop` 不再携带 usage | §3.2、§6.1 |
| N5 | 循环里没有 system prompt 调用点 | 每 step `builder.System(ctx, tools.Specs())`，turn 开始 `BeginTurn()`；工具集每步刷新 | §6.1、§7.1 |
| N6 | Flush 调用者未定义 | 明确 agent 在 turn 结束与压缩后调用；App.Shutdown 最后再调一次；W5/W8 不调度 | §8.1、§6.1 |
| N7 | 队列语义不可编码 | 改为「mu 保护的 pending 切片 + wake」，给出队列 goroutine、StartTurn、CancelTurn 三段伪代码与 position 定义 | §2.7、§6.6 |
| N8 | App 缺 Provider 注入缝 | 新增 `ProviderFactory` 与 `NewWithDeps(sender, cfg, factory)` | §2.7、§9.3 |
| N9 | Config.Validate 缺必填清单与默认值 | 明确必填项、数值范围，以及 Now/NewID 的默认实现 | §2.6 |
| N10 | 压缩边界由谁写、CompactInfo 缺 Model | `CompactInfo.Model`；agent 写 `RecordCompact` 与保留消息并 Flush；Compactor 不持有 Store | §2.5、§6.1 |
| N11 | context 测试注入点不存在 | `Options` 增加 `ReadFile` 与 `RunGit` | §2.5、§9.3 |
| N12 | `permission.Response.Rule` 是死字段 | 删除该字段；agent 按 option 自行推导规则 | §2.3、§5.6 |

### 12.7 第 3 轮核对结论

第 3 轮 8 条问题、12 条缺失**全部成立**，共 20 条，没有需要反驳的条目。F7 的 Wails 类型映射依据随本轮一起读取：`vendor/github.com/wailsapp/wails/v2/internal/typescriptify/typescriptify.go:140` 把 `uint8` 映射为 `number`，`:852-864` 的 `AddSimpleArrayField` 生成 `number[]`，而 `json.RawMessage` 是 `[]byte`。`context.WithoutCancel` 保留 value 且不传播取消已在 §12.1 实测。
