// Package llm 定义模型传输层共享的请求、消息、内容块与流式事件类型。
// 本文件只冻结跨包契约；Provider 的 HTTP 实现与 SSE 解析由后续文件补齐。
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Role 是消息角色。工具结果作为 RoleUser 消息回灌，与 Anthropic/OpenAI 的线格式一致。
type Role string

// 消息角色常量。
const (
	RoleUser      Role = "user"      // RoleUser 表示用户消息，工具结果也用它回灌。
	RoleAssistant Role = "assistant" // RoleAssistant 表示模型产出的消息。
)

// BlockType 是内容块的判别字段。
type BlockType string

// 内容块类型常量。
const (
	BlockText       BlockType = "text"        // BlockText 是纯文本块。
	BlockThinking   BlockType = "thinking"    // BlockThinking 是思考块，可带签名。
	BlockToolUse    BlockType = "tool_use"    // BlockToolUse 是模型发起的工具调用。
	BlockToolResult BlockType = "tool_result" // BlockToolResult 是工具结果。
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

// StopReason 是模型停止原因，仅用于观测，不参与循环控制。
type StopReason string

// 停止原因常量。
const (
	StopEndTurn   StopReason = "end_turn"   // StopEndTurn 表示模型自然结束。
	StopToolUse   StopReason = "tool_use"   // StopToolUse 表示模型请求调用工具。
	StopMaxTokens StopReason = "max_tokens" // StopMaxTokens 表示输出被长度上限截断。
	StopRefusal   StopReason = "refusal"    // StopRefusal 表示模型拒答。
	StopAborted   StopReason = "aborted"    // StopAborted 表示流被取消。
)

// Usage 是一次模型调用的 token 统计。同一消息内分段上报的用量用 Merge，
// 多步或多会话累加用 Sum，两者语义不同，调用方不得混用。
type Usage struct {
	InputTokens              int `json:"inputTokens"`
	OutputTokens             int `json:"outputTokens"`
	CacheReadInputTokens     int `json:"cacheReadInputTokens,omitempty"`
	CacheCreationInputTokens int `json:"cacheCreationInputTokens,omitempty"`
}

// ContextTokens 返回这次调用占用的上下文规模估算：
// 未命中缓存的输入、缓存读取、缓存写入与输出之和。
func (u Usage) ContextTokens() int {
	return u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens + u.OutputTokens
}

// Message 是一条对话消息。一条 assistant 消息可包含多个块（文本、思考与多个 tool_use）。
// Time 由 agent 在消息产生时写入并随 JSONL 持久化；Replay 会把它填回。
type Message struct {
	ID         string         `json:"id"`
	Role       Role           `json:"role"`
	Blocks     []ContentBlock `json:"blocks"`
	Model      string         `json:"model,omitempty"`
	StopReason StopReason     `json:"stopReason,omitempty"`
	Usage      *Usage         `json:"usage,omitempty"`
	Time       time.Time      `json:"time,omitempty"`
}

// ToolSpec 是暴露给模型的工具规格。
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Thinking 是扩展思考配置，nil 表示不启用。
// Anthropic 要求 BudgetTokens 至少 1024、小于本次请求的 MaxTokens，且与 Temperature 互斥。
type Thinking struct {
	Enabled      bool `json:"enabled"`
	BudgetTokens int  `json:"budgetTokens,omitempty"`
}

// MinThinkingBudget 是扩展思考预算的协议下限：Anthropic 拒绝小于 1024 的 budget_tokens。
const MinThinkingBudget = 1024

// DefaultThinkingBudget 是未显式配置时使用的扩展思考预算（token）。
// 选 8192 是为了在常见的 32k 上下文模型上留出足够的正文输出空间。
const DefaultThinkingBudget = 8192

// NormalizeThinking 按协议约束归一化思考预算，返回 nil 表示本次请求不启用扩展思考。
// maxTokens 是本次请求的 max_tokens；Anthropic 要求 budget_tokens 严格小于它。
// 预算非法或无法满足约束时返回 nil，调用方据此不发送 thinking 字段，
// 而不是让整个请求失败。
func NormalizeThinking(enabled bool, budget int, maxTokens int) *Thinking {
	if !enabled {
		return nil
	}
	if budget <= 0 {
		budget = DefaultThinkingBudget
	}
	if budget < MinThinkingBudget {
		budget = MinThinkingBudget
	}
	// budget_tokens 必须严格小于 max_tokens；不满足时放弃扩展思考。
	if maxTokens > 0 && budget >= maxTokens {
		return nil
	}
	return &Thinking{Enabled: true, BudgetTokens: budget}
}

// Request 是一次模型调用。
// Messages 必须满足「每个 assistant 的 tool_use 都有对应的 tool_result」。
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

// ModelInfo 是模型的静态能力与容量。未知模型的默认值由 ModelInfoOf 提供。
type ModelInfo struct {
	ID            string
	ContextWindow int
	MaxOutput     int
	SupportsTools bool
}

// EventType 是流事件的判别字段。
type EventType string

// 流事件类型常量。块级事件（block_start、text_delta、thinking_delta、signature、
// tool_use_start、tool_input_delta、tool_use_stop）必须携带 Kind。
const (
	EventMessageStart   EventType = "message_start"    // EventMessageStart 表示一条 assistant 消息开始。
	EventBlockStart     EventType = "block_start"      // EventBlockStart 表示文本或思考块开始。
	EventTextDelta      EventType = "text_delta"       // EventTextDelta 是文本增量。
	EventThinkingDelta  EventType = "thinking_delta"   // EventThinkingDelta 是思考文本增量。
	EventSignature      EventType = "signature"        // EventSignature 是思考签名增量。
	EventToolUseStart   EventType = "tool_use_start"   // EventToolUseStart 表示工具调用块开始。
	EventToolInputDelta EventType = "tool_input_delta" // EventToolInputDelta 是工具参数 JSON 增量。
	EventToolUseStop    EventType = "tool_use_stop"    // EventToolUseStop 表示工具调用块结束。
	EventMessageDelta   EventType = "message_delta"    // EventMessageDelta 携带停止原因与输出用量。
	EventMessageStop    EventType = "message_stop"     // EventMessageStop 表示消息结束。
	EventPing           EventType = "ping"             // EventPing 是心跳，不转发给前端。
	EventRetry          EventType = "retry"            // EventRetry 表示即将重试。
	EventError          EventType = "error"            // EventError 表示流失败。
)

// StreamEvent 是 Provider 产出的最小事件单位，字段按 Type 解释。
//
// Index 是 assistant 消息内的块序号，由适配器在块创建时分配：同一消息内唯一且单调递增，
// 不同类型不得复用同一个 Index。
//
// Kind 对块级事件必填；agent 以 (Index, Kind) 为累加器键。Kind 为空的块级事件属于
// 适配器缺陷：agent 先按 Index 查已打开的块，查不到就丢弃并记录错误。
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

// 错误分类常量。
const (
	ErrKindAuth            ErrorKind = "auth"             // ErrKindAuth 表示鉴权失败。
	ErrKindRateLimit       ErrorKind = "rate_limit"       // ErrKindRateLimit 表示触发限流。
	ErrKindOverloaded      ErrorKind = "overloaded"       // ErrKindOverloaded 表示服务过载。
	ErrKindInvalidRequest  ErrorKind = "invalid_request"  // ErrKindInvalidRequest 表示请求参数非法。
	ErrKindContextOverflow ErrorKind = "context_overflow" // ErrKindContextOverflow 表示上下文超窗。
	ErrKindNetwork         ErrorKind = "network"          // ErrKindNetwork 表示网络错误。
	ErrKindServer          ErrorKind = "server"           // ErrKindServer 表示服务端错误。
	ErrKindCanceled        ErrorKind = "canceled"         // ErrKindCanceled 表示调用方取消。
	ErrKindUnknown         ErrorKind = "unknown"          // ErrKindUnknown 表示未分类错误。
)

// StreamError 是流失败的唯一载体，由 llm 包的错误实现补齐 Error 与 Unwrap 方法。
type StreamError struct {
	Kind      ErrorKind `json:"kind"`
	Message   string    `json:"message"`
	Status    int       `json:"status,omitempty"`
	Retryable bool      `json:"retryable"`
	Provider  string    `json:"provider,omitempty"`
}

// Provider 是一个模型供应商适配器。实现必须是并发安全的。
type Provider interface {
	// Name 返回供应商标识，例如 "anthropic"、"openai"。
	Name() string
	// Stream 发起一次流式请求。返回错误表示请求未能建立（此时已完成重试）；
	// 建立后的失败通过 Stream.Recv 返回，不会同时在两个地方报错。
	Stream(ctx context.Context, req Request) (Stream, error)
}

// Stream 是一次进行中的流。调用方必须 defer Close，并在 Recv 返回错误后立即 Close。
type Stream interface {
	// Recv 返回下一个事件。流正常结束返回 io.EOF；失败返回 *StreamError；
	// ctx 取消返回包装了 context.Canceled 的错误。只允许一个 goroutine 调用。
	Recv(ctx context.Context) (StreamEvent, error)
	// Close 释放连接并等待读取 goroutine 退出。可重复调用，且不得因事件通道写满而阻塞。
	Close() error
}

// ErrMissingAPIKey 表示未配置密钥。
var ErrMissingAPIKey = errors.New("llm: missing api key")

// ErrInvalidRequest 表示请求参数不满足供应商约束，例如思考预算非法。
var ErrInvalidRequest = errors.New("llm: invalid request")
