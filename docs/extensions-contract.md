# 扩展接缝契约

本文冻结 `internal/agent` 的唯一扩展入口。Skills、Memory、Hooks、MCP、SubAgent、
用量追踪六个模块并行实现时，只写自己的目录和 `internal/app/app_<module>.go`，
不再改动 Agent 核心循环。

核心实现：`internal/agent/extension.go`（类型）、`internal/agent/engine.go`（注册）、
`internal/agent/loop.go`（调用点）、`internal/app/app_extension_hub.go`（组件单例）、
`internal/app/extensions.go`（装配入口）。

---

## 1. 接口签名（逐字冻结）

```go
// ToolEvent 描述一次工具调用，供 PreToolUse / PostToolUse 钩子使用。
type ToolEvent struct {
	SessionID string
	ToolName  string
	Input     json.RawMessage
	Paths     []string
}

// Hooks 是 Agent 循环的生命周期钩子。
type Hooks interface {
	// SessionStart 在一轮开始、发出 turn_start 之后调用；返回错误只上报，不中断轮次。
	SessionStart(ctx context.Context, sessionID, workDir string) error
	// UserPromptSubmit 在用户消息写入历史之后调用，返回的文本追加到本轮系统提示末尾。
	UserPromptSubmit(ctx context.Context, sessionID, prompt string) (string, error)
	// PreToolUse 在权限引擎判定之后、工具执行之前调用。
	PreToolUse(ctx context.Context, ev ToolEvent) (permission.Effect, string, error)
	// PostToolUse 在单个工具结果产生之后、结果回灌历史之前调用。
	PostToolUse(ctx context.Context, ev ToolEvent, res tool.Result)
}

// ContextProvider 为本轮请求补充系统提示文本；返回空字符串表示不追加。
type ContextProvider interface {
	ExtraContext(ctx context.Context, sessionID, workDir, prompt string) string
}

// RequestMetric 是一次模型调用（一次 Stream 请求）的观测记录。
type RequestMetric struct {
	SessionID  string
	Model      string
	Usage      llm.Usage
	DurationMS int64
	StopReason StopReason // 成功 completed，失败 error，取消 canceled
	Err        string
	At         time.Time
}

// MetricsSink 接收模型调用观测。
type MetricsSink interface {
	Record(RequestMetric)
}

// Extensions 是 Agent 的可选扩展集合，零值表示不启用任何扩展。
type Extensions struct {
	Tools    []tool.Tool
	Hooks    Hooks
	Context  []ContextProvider
	Metrics  MetricsSink
	Subagent SubagentRunner
}

// SubagentRequest 是一次子 Agent 运行请求。
type SubagentRequest struct {
	SessionID   string
	WorkDir     string
	Description string
	Prompt      string
	AgentType   string
	Background  bool
}

// SubagentResult 是一次子 Agent 运行结果。
type SubagentResult struct {
	ID    string
	Text  string
	Error string
	Usage llm.Usage
	Turns int
}

// SubagentRunner 执行子 Agent。
type SubagentRunner interface {
	Run(ctx context.Context, req SubagentRequest, emit EventSink) (SubagentResult, error)
}
```

### 1.1 核心调用点与语义

| 位置 | 时机 | 失败/异常处理 |
|---|---|---|
| `Engine.Run` → `SessionStart` | 发出 `turn_start` 之后、写用户消息之前 | 错误只发 `error` 事件（kind=`hook`），轮次继续 |
| 用户消息写入历史之后 → `UserPromptSubmit` | 每轮一次 | 错误只上报，视为不追加；返回文本存 `turn.promptExtra` |
| `prepare()` → 系统提示末尾 | 每次模型调用 | `promptExtra` + 每个 `ContextProvider.ExtraContext`，空文本跳过；panic 只上报 |
| `precheck()` → 权限判定之后、执行之前 | 每次工具调用 | 见下方合并规则；报错按原判定继续 |
| 单个结果产生后、回灌历史前 → `PostToolUse` | 每个 `tool_use` 一条结果（含拒绝、取消与错误结果） | 无返回值；panic 只上报 |
| `callModel()` | 每次模型调用 | 成功/失败/取消都调用 `MetricsSink.Record`；失败带 `Err`，取消 `StopReason=canceled` |

`PreToolUse` 的合并规则（不可放宽）：

1. `deny` 覆盖任何既有判定（包括 `allow` 和 `ask`）。
2. `ask` 只能把 `allow` 升级为 `ask`，不能把 `deny` 降级。
3. `allow` 不改变既有判定。
4. 钩子返回错误或 panic 时，保持原有判定继续执行。

### 1.2 工具注册

`agent.NewEngine` 会把 `Extensions.Tools` 逐个注册进 `Config.Tools`（与内置工具同一个注册表）。
工具名重复时 `NewEngine` 返回错误，引擎不会构建。扩展工具必须满足 `internal/tool` 的 `Tool` 契约
（`Name` / `Description` / `InputSchema` / `ReadOnly` / `ConcurrencySafe` / `Run`）。

---

## 2. App 绑定文件约定

每个模块在 `internal/app` 只新增一个绑定文件：`app_<module>.go`（例如 `app_skills.go`、
`app_memory.go`、`app_mcp.go`）。该文件：

- 只做参数校验、DTO 转换和编排；业务逻辑必须在模块自己的 `internal/<module>` 包里。
- DTO 与前端转换函数写在同一文件（`*DTO` / `*Input` / `to*DTO`），不放进 `dto.go`。
- 绑定方法挂在 `*App` 上，方法名导出且有中文文档注释；参数与返回值必须可 JSON 序列化。
- 组件实例通过 `a.extensionStore(key, init)` 获取，不在绑定文件里保存可变全局状态。
- 不得直接返回内部类型（`*skill.Loader`、`*mcp.Manager` 等），只返回 DTO。

`extensionStore` 语义（`internal/app/app_extension_hub.go`）：

```go
func (a *App) extensionStore(key string, init func() (any, error)) (any, error)
```

- 首次调用执行 `init` 并缓存结果；之后同 key 直接返回缓存。
- `init` 返回错误时不缓存，下一次调用会重试。
- `init` 在 `extMu` 持有期间执行：不得再调用 `extensionStore`（会死锁），也不得调用依赖 `a.mu` 的方法。
- 组件构造只做本地 IO；网络连接（MCP、IM）放在组件自身的惰性路径。

用户级数据目录统一用 `a.settingsDir()`，返回并创建 `~/.b-desktop`（0700）。

---

## 3. 后续模块实现的 component 方法

以下方法由各模块在 `internal/app/extensions.go` 中组装，签名冻结。
它们**当前尚不存在**，地基不引用这些包；模块落地后按下表实现。

```go
// Skills：加载 SKILL.md，按需把技能清单注入上下文，并提供 Skill 工具。
func (a *App) skillsComponent(workDir string) (*skill.Loader, error)
// *skill.Loader 实现 agent.ContextProvider，且有 Tools() []tool.Tool。

// Memory：MEMORY.md 索引 + 单条记忆文件，每轮召回相关记忆并提供记忆工具。
func (a *App) memoryComponent() (*memory.Store, error)
// *memory.Store 实现 agent.ContextProvider，且有 Tools() []tool.Tool。

// Hooks：读取用户/项目 hooks 配置并实现 agent.Hooks。
func (a *App) hooksComponent(workDir string) (agent.Hooks, error)

// MCP：MCP 客户端管理，暴露远端工具。
func (a *App) mcpComponent() (*mcp.Manager, error)
// *mcp.Manager 有 Tools() []tool.Tool 与 Close() error。

// SubAgent：子 Agent 与 Teams，实现 agent.SubagentRunner。
func (a *App) subagentComponent(workDir string) (*subagent.Manager, error)
// *subagent.Manager 实现 agent.SubagentRunner，且有 Tools() []tool.Tool 与 Close() error。

// 用量追踪：模型调用指标持久化。
func (a *App) telemetryComponent() (*telemetry.Store, error)
// *telemetry.Store 实现 agent.MetricsSink。
```

`buildExtensions(workDir string) (agent.Extensions, error)` 的组装规则：

1. 按需调用上面的 component 方法，用 `extensionStore` 复用单例；返回错误可包装后上抛，
   引擎不会以半装配状态启动。
2. `Tools` 汇集各组件 `Tools()` 的结果；重名由 `agent.NewEngine` 报错，模块内应避免与内置工具同名。
3. `Hooks`、`Context`、`Metrics`、`Subagent` 分别取对应组件；未启用时留零值。
4. `Close()` 由整合代理在 App 的 `Shutdown` 中统一调用，组件本身要可重复关闭。

---

## 4. 文件所有权规则

| 范围 | 归属 | 规则 |
|---|---|---|
| `internal/agent/extension.go` | 地基 | 只增不改；签名变更必须先改本文档 |
| `internal/agent/engine.go`、`loop.go` | 地基 | 模块不得修改；需要新接缝先提契约变更 |
| `internal/app/app.go`、`app_extension_hub.go`、`extensions.go` | 地基 / 整合代理 | 集成期只允许在 `buildExtensions` 内装配 |
| `internal/<module>/**` | 对应模块 | 模块独占，包含实现与测试 |
| `internal/app/app_<module>.go` | 对应模块 | 绑定薄层 + DTO，其他模块不得修改 |
| `docs/extensions-contract.md` | 地基 | 跨模块签名变更先改这里，再改代码 |

并行约定：

1. 每个模块只写自己名下的文件；需要其他模块新增导出能力时先改本文档。
2. 模块不得导入 `internal/app`，依赖方向只能是 `app → module → agent/llm/tool/...`。
3. 模块的 `Tools()` / `Close()` 必须并发安全、幂等；`ExtraContext` 必须快速返回（每轮模型调用前都会调用）。
4. 所有新增导出标识符写中文文档注释，提交前 `gofmt`。
