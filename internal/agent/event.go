// Package agent 定义 Agent 运行时推给前端的事件契约。
// 本文件只冻结事件类型与载荷；循环、工具编排与持久化由后续文件补齐。
package agent

import (
	"encoding/json"

	"github.com/Baike12/b-desktop/internal/llm"
	"github.com/Baike12/b-desktop/internal/permission"
)

// EventType 是推给前端的事件名。
type EventType string

// 事件类型常量。turn_end 一定是本轮最后一个事件；被取消的排队轮次只有
// turn_queued 与 turn_end，没有 turn_start。
const (
	EventTurnQueued         EventType = "turn_queued"         // EventTurnQueued 表示轮次已入队但尚未开始。
	EventTurnStart          EventType = "turn_start"          // EventTurnStart 表示轮次开始。
	EventStepStart          EventType = "step_start"          // EventStepStart 表示第 Step 次模型调用开始。
	EventMessageStart       EventType = "message_start"       // EventMessageStart 表示一条 assistant 消息开始。
	EventBlockStart         EventType = "block_start"         // EventBlockStart 表示消息内的文本或思考块开始。
	EventTextDelta          EventType = "text_delta"          // EventTextDelta 是文本增量。
	EventThinkingDelta      EventType = "thinking_delta"      // EventThinkingDelta 是思考文本增量。
	EventToolInputDelta     EventType = "tool_input_delta"    // EventToolInputDelta 是工具参数 JSON 增量。
	EventToolCallStart      EventType = "tool_call_start"     // EventToolCallStart 表示工具调用块开始。
	EventToolCallReady      EventType = "tool_call_ready"     // EventToolCallReady 表示工具参数已完整，准备执行。
	EventToolProgress       EventType = "tool_progress"       // EventToolProgress 是工具执行进度。
	EventPermissionRequest  EventType = "permission_request"  // EventPermissionRequest 表示需要用户审批。
	EventPermissionResolved EventType = "permission_resolved" // EventPermissionResolved 表示审批已有结果。
	EventToolResult         EventType = "tool_result"         // EventToolResult 表示工具执行结束。
	EventUsage              EventType = "usage"               // EventUsage 是一次模型调用的用量。
	EventCompactStart       EventType = "compact_start"       // EventCompactStart 表示压缩开始。
	EventCompactEnd         EventType = "compact_end"         // EventCompactEnd 表示压缩结束，含失败。
	EventRetry              EventType = "retry"               // EventRetry 表示模型调用即将重试。
	EventMessageStop        EventType = "message_stop"        // EventMessageStop 表示一条 assistant 消息结束。
	EventMessageDiscard     EventType = "message_discard"     // EventMessageDiscard 表示丢弃未完成的 assistant 消息。
	EventTaskUpdate         EventType = "task_update"         // EventTaskUpdate 表示一个后台任务状态变化。
	EventTodoUpdate         EventType = "todo_update"         // EventTodoUpdate 表示会话待办清单整体变化。
	EventError              EventType = "error"               // EventError 表示可展示的错误。
	EventTurnEnd            EventType = "turn_end"            // EventTurnEnd 表示轮次结束，一定是本轮最后一个事件。
)

// StopReason 是一轮的结束原因。
type StopReason string

// 轮次结束原因常量。
const (
	StopCompleted     StopReason = "completed"      // StopCompleted 表示模型不再发起工具调用，正常结束。
	StopMaxTurns      StopReason = "max_turns"      // StopMaxTurns 表示达到模型调用次数上限。
	StopCanceled      StopReason = "canceled"       // StopCanceled 表示调用方取消。
	StopError         StopReason = "error"          // StopError 表示发生不可恢复错误（含 panic）。
	StopBlockingLimit StopReason = "blocking_limit" // StopBlockingLimit 表示上下文已无法压缩到可用范围。
)

// Event 是推给前端的统一事件，字段按 Type 解释。
// Seq 由 app.Emitter 在发送前分配；agent 内部构造的事件 Seq 恒为 0。
type Event struct {
	Type       EventType                `json:"type"`
	SessionID  string                   `json:"sessionId"`
	TurnID     string                   `json:"turnId"`
	Step       int                      `json:"step,omitempty"`
	Position   int                      `json:"position,omitempty"`
	Seq        int64                    `json:"seq"`
	TimeMS     int64                    `json:"timeMs"`
	MessageID  string                   `json:"messageId,omitempty"`
	BlockIndex int                      `json:"blockIndex,omitempty"`
	Text       string                   `json:"text,omitempty"`
	ToolUseID  string                   `json:"toolUseId,omitempty"`
	ToolName   string                   `json:"toolName,omitempty"`
	Input      json.RawMessage          `json:"input,omitempty"`
	Result     *ToolResultView          `json:"result,omitempty"`
	Approval   *permission.ApprovalView `json:"approval,omitempty"`
	Usage      *llm.Usage               `json:"usage,omitempty"`
	// ContextTokens 是最近一次模型调用占用的上下文规模，供前端展示“当前上下文”。
	ContextTokens int            `json:"contextTokens,omitempty"`
	Compact       *CompactView   `json:"compact,omitempty"`
	Retry         *llm.RetryInfo `json:"retry,omitempty"`
	Error         *ErrorView     `json:"error,omitempty"`
	StopReason    StopReason     `json:"stopReason,omitempty"`
	Task          *TaskView      `json:"task,omitempty"`
	// Todos 是 todo_update 事件的完整清单快照；其他事件恒为 nil。
	Todos *TodoStateView `json:"todos,omitempty"`
}

// TodoStateView 是待办清单的前端视图，条目按提交顺序排列。
type TodoStateView struct {
	Items []TodoItemView `json:"items"`
}

// TodoItemView 是一条待办。
type TodoItemView struct {
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"activeForm,omitempty"`
}

// TaskView 是一次后台任务状态变化的前端视图。
// Kind 为 local_bash 时 Command/ExitCode/OutputFile 有效，
// 为 local_agent 时 AgentType/Result 有效。
type TaskView struct {
	TaskID      string `json:"taskId"`
	Kind        string `json:"kind"`
	Description string `json:"description,omitempty"`
	Command     string `json:"command,omitempty"`
	AgentType   string `json:"agentType,omitempty"`
	Status      string `json:"status"`
	ExitCode    int    `json:"exitCode,omitempty"`
	Error       string `json:"error,omitempty"`
	Result      string `json:"result,omitempty"`
	OutputFile  string `json:"outputFile,omitempty"`
	// Summary 是完成通知的摘要文本（与 <task-notification> 的 summary 一致）。
	Summary string `json:"summary,omitempty"`
}

// ToolResultView 是工具结果的前端视图，不包含完整内容（完整内容在历史里）。
type ToolResultView struct {
	Preview   string `json:"preview"`
	IsError   bool   `json:"isError"`
	Truncated bool   `json:"truncated"`
	Bytes     int    `json:"bytes"`
	// Images 是结果附带的图片（与 transcript 一致），前端渲染缩略图。
	Images []llm.Image `json:"images,omitempty"`
}

// CompactView 是一次压缩的前端视图。Failed 为 true 时 Error 说明原因，
// 前端据此结束「压缩中」状态。
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
