// Package acp 实现 Agent Client Protocol（ACP）的核心类型与 Agent 后端抽象。
//
// ACP 用换行分隔的 JSON-RPC 2.0 连接编辑器（客户端）与外部 Agent 进程。
// 本包沉淀四层能力：
//
//   - types.go：ACP v1 协议类型（会话更新、工具调用、权限请求等）；
//   - session.go：Backend / Session 抽象与注册表，内置或外部 Agent 一视同仁；
//   - client.go：通用外部 ACP 进程客户端（接入 codex 等外部 Agent 走此路径）；
//   - claude/：内置 Claude Code 后端，直接驱动 claude CLI 的 stream-json 协议，
//     行为对齐 zed 的 claude-agent-acp 适配器，无需 Node 插件进程。
//
// 个别结构体带有「b-desktop 扩展」注释的字段：它们超出 ACP v1 规范，
// 用于把宿主需要的信息（子 Agent 路由、流式工具参数、模型状态）穿过同一
// 更新通道；外部后端不发送这些字段，解析时也总是容忍未知字段。
package acp

import (
	"encoding/json"
	"errors"
)

// ProtocolVersion 是本实现支持的 ACP 协议版本，initialize 请求与响应都携带。
const ProtocolVersion = 1

// ToolKind 是工具调用的展示类别。
type ToolKind string

// 工具类别常量。delete 与 move 目前没有内置后端使用，保留以对齐协议。
const (
	ToolRead       ToolKind = "read"
	ToolEdit       ToolKind = "edit"
	ToolDelete     ToolKind = "delete"
	ToolMove       ToolKind = "move"
	ToolSearch     ToolKind = "search"
	ToolExecute    ToolKind = "execute"
	ToolThink      ToolKind = "think"
	ToolFetch      ToolKind = "fetch"
	ToolSwitchMode ToolKind = "switch_mode"
	ToolOther      ToolKind = "other"
)

// ToolCallStatus 是工具调用的生命周期状态。
type ToolCallStatus string

// 工具状态常量。
const (
	StatusPending    ToolCallStatus = "pending"
	StatusInProgress ToolCallStatus = "in_progress"
	StatusCompleted  ToolCallStatus = "completed"
	StatusFailed     ToolCallStatus = "failed"
)

// ContentBlock 是会话内容块，Type 决定哪些字段有效：
//   - text：Text
//   - image：Data（base64）、MimeType
//   - resource_link：URI、Name、可选 Title/Description/MimeType
//   - resource：Resource
type ContentBlock struct {
	Type string `json:"type"`
	// Text 是 text 块的正文。
	Text string `json:"text,omitempty"`
	// Data 是 image 块的裸 base64 数据。
	Data string `json:"data,omitempty"`
	// MimeType 是 image / resource 块的媒体类型。
	MimeType string `json:"mimeType,omitempty"`
	// URI 是 resource_link / resource 块的地址。
	URI string `json:"uri,omitempty"`
	// Name 是 resource_link 块的显示名。
	Name string `json:"name,omitempty"`
	// Title 与 Description 是 resource_link 块的可选说明。
	Title       string           `json:"title,omitempty"`
	Description string           `json:"description,omitempty"`
	Resource    *ResourceContent `json:"resource,omitempty"`
}

// ResourceContent 是 resource 块携带的资源内容：Text 与 Blob 二选一。
type ResourceContent struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

// ToolCallLocation 是工具触及的文件位置；Line 从 0 计。
type ToolCallLocation struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
}

// ToolDiff 是 diff 内容：OldText 为 nil 表示新建文件。
type ToolDiff struct {
	Path    string  `json:"path"`
	OldText *string `json:"oldText,omitempty"`
	NewText string  `json:"newText"`
}

// ToolCallContent 是工具调用的附属内容，Type 决定哪些字段有效：
//   - content：Content
//   - diff：Diff
//   - terminal：TerminalID
type ToolCallContent struct {
	Type       string        `json:"type"`
	Content    *ContentBlock `json:"content,omitempty"`
	Diff       *ToolDiff     `json:"diff,omitempty"`
	TerminalID string        `json:"terminalId,omitempty"`
}

// ToolCall 是一次工具调用的完整快照。
type ToolCall struct {
	ToolCallID string `json:"toolCallId"`
	Title      string `json:"title,omitempty"`
	// ToolName 是 b-desktop 扩展：原始工具名（如 Read/Edit/mcp__srv__tool），
	// 外部 ACP agent 不发送，宿主按 Kind 回退。
	ToolName  string             `json:"toolName,omitempty"`
	Kind      ToolKind           `json:"kind,omitempty"`
	Status    ToolCallStatus     `json:"status,omitempty"`
	Content   []ToolCallContent  `json:"content,omitempty"`
	Locations []ToolCallLocation `json:"locations,omitempty"`
	RawInput  json.RawMessage    `json:"rawInput,omitempty"`
	RawOutput json.RawMessage    `json:"rawOutput,omitempty"`
	// InputDelta 是 b-desktop 扩展：工具参数 JSON 的流式增量，宿主拼接后展示。
	InputDelta string `json:"inputDelta,omitempty"`
}

// ToolCallUpdate 是对既有工具调用的增量更新；未出现的字段保持不变。
type ToolCallUpdate struct {
	ToolCallID string             `json:"toolCallId"`
	Title      string             `json:"title,omitempty"`
	ToolName   string             `json:"toolName,omitempty"`
	Kind       ToolKind           `json:"kind,omitempty"`
	Status     ToolCallStatus     `json:"status,omitempty"`
	Content    []ToolCallContent  `json:"content,omitempty"`
	Locations  []ToolCallLocation `json:"locations,omitempty"`
	RawInput   json.RawMessage    `json:"rawInput,omitempty"`
	RawOutput  json.RawMessage    `json:"rawOutput,omitempty"`
	InputDelta string             `json:"inputDelta,omitempty"`
}

// PlanEntry 是计划清单里的一条。Priority 取 high|medium|low，
// Status 取 pending|in_progress|completed。
type PlanEntry struct {
	Content  string `json:"content"`
	Priority string `json:"priority,omitempty"`
	Status   string `json:"status"`
}

// Plan 是计划清单的完整快照，整体替换语义。
type Plan struct {
	Entries []PlanEntry `json:"entries"`
}

// AvailableCommand 是 Agent 支持的一条斜杠命令。
type AvailableCommand struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Hint 是参数占位提示（如 "[reconnect|enable]"）。
	Hint string `json:"hint,omitempty"`
}

// SessionMode 是会话的一种权限模式。
type SessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SessionModes 是会话的模式状态。
type SessionModes struct {
	Current   string        `json:"currentModeId"`
	Available []SessionMode `json:"availableModes"`
}

// PermissionOptionKind 是权限选项的分类。
type PermissionOptionKind string

// 权限选项分类常量，与 ACP v1 一致。
const (
	AllowOnce    PermissionOptionKind = "allow_once"
	AllowAlways  PermissionOptionKind = "allow_always"
	RejectOnce   PermissionOptionKind = "reject_once"
	RejectAlways PermissionOptionKind = "reject_always"
)

// PermissionOption 是呈现给用户的一个权限选项。
type PermissionOption struct {
	OptionID string               `json:"optionId"`
	Name     string               `json:"name"`
	Kind     PermissionOptionKind `json:"kind"`
}

// PermissionRequest 是 Agent 发起的权限审批请求。
type PermissionRequest struct {
	// ToolCall 是待审批调用的快照（可能早于流式 tool_call 到达）。
	ToolCall ToolCallUpdate `json:"toolCall"`
	// ToolName 是 b-desktop 扩展：CLI 报告的原始工具名，供宿主审批弹窗展示。
	ToolName string `json:"toolName,omitempty"`
	// Reason 是 CLI 给出的判定原因，可为空。
	Reason string `json:"reason,omitempty"`
	// Options 是可选的处理方式。
	Options []PermissionOption `json:"options"`
}

// PermissionOutcome 是宿主对权限请求的答复。
type PermissionOutcome struct {
	// Cancelled 为 true 表示宿主取消了请求（例如用户停止本轮）。
	Cancelled bool
	// OptionID 是被选中的选项；Cancelled 时为空。
	OptionID string
}

// UsageSummary 是一轮提示的累计用量。
type UsageSummary struct {
	InputTokens  int     `json:"inputTokens"`
	OutputTokens int     `json:"outputTokens"`
	CachedRead   int     `json:"cachedRead,omitempty"`
	CachedWrite  int     `json:"cachedWrite,omitempty"`
	CostUSD      float64 `json:"costUsd,omitempty"`
}

// StopReason 是一轮提示的结束原因，与 ACP v1 一致。
type StopReason string

// 结束原因常量。
const (
	StopEndTurn     StopReason = "end_turn"
	StopMaxTokens   StopReason = "max_tokens"
	StopMaxTurnReqs StopReason = "max_turn_requests"
	StopRefusal     StopReason = "refusal"
	StopCancelled   StopReason = "cancelled"
)

// SessionStatus 是 b-desktop 扩展的会话状态快照：当前模型与模式。
// 外部 ACP 后端不发送该更新。
type SessionStatus struct {
	Model         string `json:"model,omitempty"`
	ContextWindow int    `json:"contextWindow,omitempty"`
	Mode          string `json:"mode,omitempty"`
}

// UpdateKind 是会话更新的判别字段，取值与 ACP v1 的 sessionUpdate 一致，
// 另加 b-desktop 扩展的 status_update。
type UpdateKind string

// 更新类型常量。
const (
	UpdateUserMessageChunk  UpdateKind = "user_message_chunk"
	UpdateAgentMessageChunk UpdateKind = "agent_message_chunk"
	UpdateAgentThoughtChunk UpdateKind = "agent_thought_chunk"
	UpdateToolCall          UpdateKind = "tool_call"
	UpdateToolCallUpdate    UpdateKind = "tool_call_update"
	UpdatePlan              UpdateKind = "plan"
	UpdateAvailableCommands UpdateKind = "available_commands_update"
	UpdateCurrentMode       UpdateKind = "current_mode_update"
	UpdateStatus            UpdateKind = "status_update"
)

// SessionUpdate 是一次会话更新。Kind 决定哪些字段有效：
//   - *_chunk：Content（MessageID 为消息分组键，可为空）
//   - tool_call：ToolCall
//   - tool_call_update：ToolCallUpdate
//   - plan：Plan
//   - available_commands_update：Commands
//   - current_mode_update：CurrentModeID
//   - status_update：Status
type SessionUpdate struct {
	Kind      UpdateKind `json:"kind"`
	SessionID string     `json:"sessionId,omitempty"`
	// AgentID 是 b-desktop 扩展：子 Agent 路由键。Claude 后端填
	// parent_tool_use_id，宿主据此把子 Agent 的输出归入对应工具线程；
	// 主线程更新恒为空。
	AgentID string `json:"agentId,omitempty"`

	Content        *ContentBlock      `json:"content,omitempty"`
	MessageID      string             `json:"messageId,omitempty"`
	ToolCall       *ToolCall          `json:"toolCall,omitempty"`
	ToolCallUpdate *ToolCallUpdate    `json:"toolCallUpdate,omitempty"`
	Plan           *Plan              `json:"plan,omitempty"`
	Commands       []AvailableCommand `json:"commands,omitempty"`
	CurrentModeID  string             `json:"currentModeId,omitempty"`
	Status         *SessionStatus     `json:"status,omitempty"`
}

// PromptResult 是一轮提示的结算结果。
type PromptResult struct {
	StopReason StopReason
	Usage      *UsageSummary
	// Error 是可展示的失败说明；非空时 StopReason 不可信，宿主按失败处理。
	Error string
	// Turns 是本轮的模型调用次数（Agent 报告，可能为 0）。
	Turns int
}

// 会话级错误哨兵。
var (
	// ErrResumeNotFound 表示 ResumeKey 指定的后端会话不存在；
	// 调用方应去掉 resume 键重开新会话。
	ErrResumeNotFound = errors.New("acp: agent session not found")
)

// MCPServer 是传给 Agent 的一个 MCP 服务器配置。
// Type 取 stdio|http|sse；stdio 用 Command/Args/Env，http 与 sse 用 URL/Headers。
type MCPServer struct {
	Name    string            `json:"name"`
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// SessionParams 是新建会话的参数。
type SessionParams struct {
	CWD string
	// MCPServers 是宿主希望 Agent 挂载的 MCP 服务器，可为空。
	MCPServers []MCPServer
	// PermissionMode 是初始权限模式；空表示用 Agent 默认值。
	PermissionMode string
	// ResumeKey 是上次会话的后端会话键（如 claude 会话 uuid）；
	// 后端支持恢复时应据此续接上下文，恢复失败报 ErrResumeNotFound 类错误。
	ResumeKey string
}

// AgentInfo 描述一个可选的 Agent。
type AgentInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// AgentAvailability 是注册表中一个 Agent 的可用性快照。
type AgentAvailability struct {
	AgentInfo
	// Available 为 false 时 Detail 说明不可用的原因。
	Available bool   `json:"available"`
	Detail    string `json:"detail,omitempty"`
}
