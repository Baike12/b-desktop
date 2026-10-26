package agent

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Baike12/b-desktop/internal/llm"
	"github.com/Baike12/b-desktop/internal/permission"
	"github.com/Baike12/b-desktop/internal/tool"
)

// ToolEvent 描述一次工具调用，供 PreToolUse / PostToolUse 钩子使用。
// Paths 是经 tool.PathAware 解析后的绝对路径；非文件类工具为空。
type ToolEvent struct {
	SessionID string
	ToolName  string
	Input     json.RawMessage
	Paths     []string
}

// Hooks 是 Agent 循环的生命周期钩子。所有方法都可选：nil 接口表示不启用。
// 实现不得回调 Engine，也不得阻塞超过工具本身所需的时长。
type Hooks interface {
	// SessionStart 在一轮开始、发出 turn_start 之后调用；返回错误只上报，不中断轮次。
	SessionStart(ctx context.Context, sessionID, workDir string) error
	// UserPromptSubmit 在用户消息写入历史之后调用，返回的文本追加到下一次系统提示末尾；
	// 返回空字符串表示不追加。
	UserPromptSubmit(ctx context.Context, sessionID, prompt string) (string, error)
	// PreToolUse 在权限引擎判定之后、工具执行之前调用。
	// deny 覆盖任何 allow；ask 只能把 allow 升级为 ask；allow 不改变既有判定。
	PreToolUse(ctx context.Context, ev ToolEvent) (permission.Effect, string, error)
	// PostToolUse 在单个工具结果产生之后、结果回灌历史之前调用。
	PostToolUse(ctx context.Context, ev ToolEvent, res tool.Result)
}

// ContextProvider 为本轮请求补充系统提示文本。返回空字符串表示不追加。
// 实现必须快速返回，且不得修改引擎状态。
type ContextProvider interface {
	// ExtraContext 返回追加到系统提示末尾的文本；prompt 是本轮用户输入。
	ExtraContext(ctx context.Context, sessionID, workDir, prompt string) string
}

// TurnInfo 是一轮正常完成后的摘要，供 TurnObserver 做轮末处理
// （例如记忆自动抽取）。
type TurnInfo struct {
	// SessionID 与 TurnID 标识产生这轮的会话与轮次。
	SessionID string
	TurnID    string
	// WorkDir 是会话工作目录。
	WorkDir string
	// Prompt 是本轮用户输入原文。
	Prompt string
	// FinalText 是助手最终的文本回复。
	FinalText string
	// ToolNames 是本轮调用过的工具名，去重保序。
	ToolNames []string
	// StopReason 是结束原因；观察者只在 StopCompleted 时被调用。
	StopReason StopReason
}

// TurnObserver 是 Hooks 的可选扩展能力：每轮以 StopCompleted 结束后
// 收到一次通知。通过类型断言从 Hooks 实现上获取；未实现则不启用。
// 实现必须立即返回（异步处理自行开 goroutine），不得阻塞循环，
// 也不得回调 Engine。
type TurnObserver interface {
	TurnComplete(info TurnInfo)
}

// RequestMetric 是一次模型调用（一次 Stream 请求）的观测记录。
type RequestMetric struct {
	SessionID  string
	Model      string
	Usage      llm.Usage
	DurationMS int64
	// StopReason 描述这次请求本身的结果：成功为 completed，失败为 error，取消为 canceled。
	StopReason StopReason
	Err        string
	At         time.Time
}

// MetricsSink 接收模型调用观测。实现必须非阻塞、并发安全，且不得 panic。
type MetricsSink interface {
	// Record 记录一次模型调用；成功与失败都会被调用。
	Record(RequestMetric)
}

// Extensions 是 Agent 的可选扩展集合，由装配层构造后整体注入。
// 零值表示不启用任何扩展。
type Extensions struct {
	Tools    []tool.Tool       // 追加到内置工具集；重名由 NewEngine 报错。
	Hooks    Hooks             // 生命周期钩子；nil 表示不启用。
	Context  []ContextProvider // 系统提示补充文本，按顺序追加。
	Metrics  MetricsSink       // 模型调用观测；nil 表示不记录。
	Subagent SubagentRunner    // 子 Agent 执行器，供 Agent 工具调用。
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

// SubagentRunner 执行子 Agent。实现拥有独立的上下文、工具池与权限范围，
// 事件经 emit 逐条回传父 Agent；Run 返回时子 Agent 已结束（后台模式返回值可先于完成）。
type SubagentRunner interface {
	Run(ctx context.Context, req SubagentRequest, emit EventSink) (SubagentResult, error)
}
