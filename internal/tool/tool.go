// Package tool 定义工具契约与共享结果类型。
// 本文件只冻结跨包契约；注册表与执行兜底由后续文件补齐。
package tool

import (
	"context"
	"encoding/json"
)

// Progress 是一次工具进度回报。
type Progress struct {
	Text      string `json:"text,omitempty"`
	Bytes     int64  `json:"bytes,omitempty"`
	Total     int64  `json:"total,omitempty"`
	ElapsedMS int64  `json:"elapsedMs,omitempty"`
}

// ProgressFunc 报告进度。实现必须非阻塞；ctx 取消后应尽快返回。
type ProgressFunc func(Progress)

// Image 是工具结果附带的一张 base64 图片（例如 Read 读取图片文件）。
// 它与 Content 一起作为 tool_result 的内容回灌给模型。
type Image struct {
	MediaType string `json:"mediaType"`
	Data      string `json:"data"`
}

// Result 是工具执行结果。Content 回灌给模型，Display 给 UI（为空则复用 Content）。
type Result struct {
	Content   string            `json:"content"`
	Display   string            `json:"display,omitempty"`
	IsError   bool              `json:"isError,omitempty"`
	Truncated bool              `json:"truncated,omitempty"`
	Meta      map[string]string `json:"meta,omitempty"`
	// Images 是结果附带的图片；Content 仍应写一句说明文字，方便不支持
	// 图片的端点降级为纯文本。
	Images []Image `json:"images,omitempty"`
}

// Tool 是一个可被模型调用的工具。实现必须是并发安全的：同一实例可能并行执行。
type Tool interface {
	// Name 返回工具名，必须全局唯一且与模型看到的一致。
	Name() string
	// Description 返回给模型看的用途说明。
	Description() string
	// InputSchema 返回 JSON Schema 文本。
	InputSchema() json.RawMessage
	// ReadOnly 报告该次调用是否只读：只读调用不会修改文件系统与进程状态。
	ReadOnly(input json.RawMessage) bool
	// ConcurrencySafe 报告该次调用能否与其他安全调用并行执行。
	ConcurrencySafe(input json.RawMessage) bool
	// Run 执行工具。返回 error 表示内部失败，由 tool.Call 转成错误结果；
	// ctx 取消时应尽快返回。
	Run(ctx context.Context, input json.RawMessage, progress ProgressFunc) (Result, error)
}

// TodoEntry 是待办清单里的一条目，由前端计划面板直接渲染。
type TodoEntry struct {
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"activeForm,omitempty"`
}

// TodoProvider 由维护「会话级待办清单」的工具实现（内置 TodoWrite）。
// agent 在该工具每次执行结束后读取完整清单并推给前端，前端据此渲染计划面板。
type TodoProvider interface {
	// TodoList 返回当前完整清单，按提交顺序排列。
	TodoList() []TodoEntry
}

// PathAware 由文件类工具实现，返回本次调用会触及的绝对路径（已解析符号链接）。
// agent 在权限预检前调用它，把结果填进 permission.Request.Paths，
// 避免在权限层重复解析文件路径字段。
type PathAware interface {
	// AffectedPaths 返回本次调用影响的绝对路径；输入不可解析时返回错误。
	AffectedPaths(input json.RawMessage) ([]string, error)
}

// AlwaysAllow 由「无文件、无进程副作用、策略上默认放行」的工具实现（例如 TodoWrite）。
// agent 据此设置 permission.Request.NoApproval，权限层不需要认识具体工具名。
type AlwaysAllow interface {
	// AlwaysAllow 报告该工具是否在任何权限模式下都默认放行。
	AlwaysAllow() bool
}

// Interactive 由「需要用户交互才能产出结果」的工具实现（例如 AskUserQuestion）。
// agent 对这类调用强制走审批通道：交互界面本身就是审批对话框，
// 用户提交的应答经审批通道回填进工具输入后再执行。
type Interactive interface {
	// Interactive 报告该工具是否必须与用户交互。
	Interactive() bool
}

// ContentLimiter 由需要自定义结果体积上限的工具实现（例如已经自行截断的
// Bash、以文件为界的 Read）。返回值含义：>0 覆盖 agent 层的默认兜底上限
// （字节）；<0 表示不限，由工具自行保证体积。未实现本接口的工具按 agent
// 默认上限做兜底截断，防止 MCP 或未来工具把一次超大输出直接灌进上下文。
type ContentLimiter interface {
	// ContentLimit 返回结果 Content 的字节上限。
	ContentLimit() int
}

// Spec 是工具的对外规格，由 agent 转换成 llm.ToolSpec。
type Spec struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// Call 是一次待执行的工具调用。
// Index 是它在整条模型回复中的全局位置（从 0 开始，跨批次不变）；
// 并发执行与结果回填都以它为准，不能使用批次内下标。
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
