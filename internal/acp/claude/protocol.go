package claude

import "encoding/json"

// 以下类型是 claude CLI stream-json 双向协议的线格式（stdout 每行一帧，
// stdin 每行一帧），字段名与 CLI 输出保持一致，不得改动 JSON tag。

// controlRequestOut 是 SDK→CLI 的控制请求信封。
type controlRequestOut struct {
	Type      string         `json:"type"` // 固定 "control_request"
	RequestID string         `json:"request_id"`
	Request   controlPayload `json:"request"`
}

// controlPayload 是控制请求的具体载荷，Subtype 决定其余字段。
type controlPayload struct {
	Subtype string `json:"subtype"`
	// initialize
	Hooks               json.RawMessage `json:"hooks,omitempty"`
	ForwardSubagentText bool            `json:"forwardSubagentText,omitempty"`
	// set_permission_mode
	Mode string `json:"mode,omitempty"`
	// set_model
	Model *string `json:"model,omitempty"`
	// stop_task
	TaskID string `json:"task_id,omitempty"`
}

// controlRequestIn 是 CLI→SDK 的控制请求信封（can_use_tool 等）。
type controlRequestIn struct {
	Type      string          `json:"type"` // "control_request"
	RequestID string          `json:"request_id"`
	Request   json.RawMessage `json:"request"`
}

// Subtype 返回控制请求的子类型。
func (r controlRequestIn) Subtype() string {
	var probe struct {
		Subtype string `json:"subtype"`
	}
	_ = json.Unmarshal(r.Request, &probe)
	return probe.Subtype
}

// canUseTool 解析 can_use_tool 载荷。
func (r controlRequestIn) canUseTool() canUseToolRequest {
	var req canUseToolRequest
	_ = json.Unmarshal(r.Request, &req)
	return req
}

// canUseToolRequest 是 can_use_tool 控制请求载荷。
type canUseToolRequest struct {
	Subtype               string          `json:"subtype"` // 固定 "can_use_tool"
	ToolName              string          `json:"tool_name"`
	Input                 json.RawMessage `json:"input"`
	PermissionSuggestions json.RawMessage `json:"permission_suggestions,omitempty"`
	ToolUseID             string          `json:"tool_use_id,omitempty"`
	AgentID               string          `json:"agent_id,omitempty"`
	BlockedPath           string          `json:"blocked_path,omitempty"`
	DecisionReason        string          `json:"decision_reason,omitempty"`
	Title                 string          `json:"title,omitempty"`
	DisplayName           string          `json:"display_name,omitempty"`
	Description           string          `json:"description,omitempty"`
}

// permissionUpdate 是 CLI 权限建议（PermissionUpdate）的一条。
type permissionUpdate struct {
	Type        string           `json:"type"` // addRules|replaceRules|removeRules|setMode|addDirectories|removeDirectories
	Destination string           `json:"destination,omitempty"`
	Behavior    string           `json:"behavior,omitempty"` // allow|deny|ask
	Mode        string           `json:"mode,omitempty"`
	Rules       []permissionRule `json:"rules,omitempty"`
}

type permissionRule struct {
	ToolName    string `json:"toolName"`
	RuleContent string `json:"ruleContent,omitempty"`
}

// controlResponseOut 是 SDK→CLI 的控制响应信封。
type controlResponseOut struct {
	Type     string          `json:"type"` // 固定 "control_response"
	Response controlResponse `json:"response"`
}

type controlResponse struct {
	RequestID string          `json:"request_id"`
	Subtype   string          `json:"subtype"` // success | error
	Response  json.RawMessage `json:"response,omitempty"`
	Error     string          `json:"error,omitempty"`
}

// controlResponseIn 是 CLI→SDK 的控制响应帧。
type controlResponseIn struct {
	Type     string `json:"type"` // "control_response"
	Response struct {
		RequestID string          `json:"request_id"`
		Subtype   string          `json:"subtype"`
		Response  json.RawMessage `json:"response,omitempty"`
		Error     string          `json:"error,omitempty"`
	} `json:"response"`
}

// userMessageIn 是 SDK→CLI 的用户消息帧。
type userMessageIn struct {
	Type            string     `json:"type"` // 固定 "user"
	Message         apiMessage `json:"message"`
	ParentToolUseID *string    `json:"parent_tool_use_id"`
	SessionID       string     `json:"session_id,omitempty"`
}

// apiContentBlock 是 Anthropic API 内容块的线格式。
// Type 决定字段：text{text} / thinking{thinking,signature} / tool_use{id,name,input} /
// tool_result{tool_use_id,content,is_error} / image{source}。
type apiContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// thinking
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// tool_result
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	// image
	Source *apiImageSource `json:"source,omitempty"`
}

type apiImageSource struct {
	Type     string `json:"type"` // base64 | url
	MimeType string `json:"media_type,omitempty"`
	Data     string `json:"data,omitempty"`
	URL      string `json:"url,omitempty"`
}

// apiMessage 是消息体（assistant 输出与 user 输入共用）。
type apiMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content,omitempty"`
	// 以下字段只出现在 CLI 输出的 assistant 消息里。
	ID         string    `json:"id,omitempty"`
	Model      string    `json:"model,omitempty"`
	StopReason string    `json:"stop_reason,omitempty"`
	Usage      *apiUsage `json:"usage,omitempty"`
}

// apiUsage 是 Anthropic API 用量。
type apiUsage struct {
	InputTokens              int `json:"input_tokens,omitempty"`
	OutputTokens             int `json:"output_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
}

// outEnvelope 是 stdout 帧的公共信封：先取 type 分流，再解析具体帧。
type outEnvelope struct {
	Type    string          `json:"type"`
	Subtype string          `json:"subtype,omitempty"`
	Raw     json.RawMessage `json:"-"`
}

// systemFrame 是 system 帧。Subtype: init / task_started / task_progress /
// task_notification / commands_changed / local_command_output / …
type systemFrame struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	// init 帧
	SessionID             string   `json:"session_id,omitempty"`
	Model                 string   `json:"model,omitempty"`
	PermissionMode        string   `json:"permissionMode,omitempty"`
	ClaudeCodeVersion     string   `json:"claude_code_version,omitempty"`
	SlashCommands         []string `json:"slash_commands,omitempty"`
	TerminalSlashCommands []string `json:"terminal_slash_commands,omitempty"`
	Capabilities          []string `json:"capabilities,omitempty"`
	Tools                 []string `json:"tools,omitempty"`
	// task_started / task_notification 帧
	TaskID      string `json:"task_id,omitempty"`
	Description string `json:"description,omitempty"`
	ToolUseID   string `json:"tool_use_id,omitempty"`
	TaskType    string `json:"task_type,omitempty"`
	Status      string `json:"status,omitempty"`
	OutputFile  string `json:"output_file,omitempty"`
	Summary     string `json:"summary,omitempty"`
	// commands_changed 帧
	Commands []string `json:"commands,omitempty"`
	// local_command_output 帧
	Content string `json:"content,omitempty"`
	// conversation_reset 帧
	NewConversationID string `json:"new_conversation_id,omitempty"`
}

// assistantFrame 是 assistant 消息帧。
type assistantFrame struct {
	Type            string     `json:"type"` // "assistant"
	UUID            string     `json:"uuid,omitempty"`
	SessionID       string     `json:"session_id,omitempty"`
	ParentToolUseID *string    `json:"parent_tool_use_id,omitempty"`
	Message         apiMessage `json:"message"`
	Error           *string    `json:"error,omitempty"`
}

// userFrame 是 user 消息帧：用户输入回放或工具结果回灌。
type userFrame struct {
	Type            string          `json:"type"` // "user"
	UUID            string          `json:"uuid,omitempty"`
	SessionID       string          `json:"session_id,omitempty"`
	ParentToolUseID *string         `json:"parent_tool_use_id,omitempty"`
	Message         apiMessage      `json:"message"`
	ToolUseResult   json.RawMessage `json:"tool_use_result,omitempty"`
}

// resultFrame 是 result 帧：一轮结束的结算消息。
type resultFrame struct {
	Type         string    `json:"type"` // "result"
	Subtype      string    `json:"subtype"`
	DurationMS   int64     `json:"duration_ms,omitempty"`
	IsError      bool      `json:"is_error,omitempty"`
	NumTurns     int       `json:"num_turns,omitempty"`
	StopReason   *string   `json:"stop_reason,omitempty"`
	TotalCostUSD float64   `json:"total_cost_usd,omitempty"`
	Usage        *apiUsage `json:"usage,omitempty"`
	Result       string    `json:"result,omitempty"`
	Errors       []string  `json:"errors,omitempty"`
	UUID         string    `json:"uuid,omitempty"`
	SessionID    string    `json:"session_id,omitempty"`
}

// streamEventFrame 是 stream_event 帧：Anthropic API 流事件的透传。
type streamEventFrame struct {
	Type            string         `json:"type"` // "stream_event"
	UUID            string         `json:"uuid,omitempty"`
	SessionID       string         `json:"session_id,omitempty"`
	ParentToolUseID *string        `json:"parent_tool_use_id,omitempty"`
	Event           apiStreamEvent `json:"event"`
}

// apiStreamEvent 是透传的 Anthropic 流事件。
type apiStreamEvent struct {
	Type  string `json:"type"` // message_start | content_block_start | content_block_delta | content_block_stop | message_delta | message_stop
	Index int    `json:"index,omitempty"`
	// message_start
	Message *apiMessage `json:"message,omitempty"`
	// content_block_start
	ContentBlock *apiContentBlock `json:"content_block,omitempty"`
	// content_block_delta
	Delta *apiStreamDelta `json:"delta,omitempty"`
}

type apiStreamDelta struct {
	Type string `json:"type"` // text_delta | thinking_delta | input_json_delta | signature_delta
	Text string `json:"text,omitempty"`
	// thinking_delta
	Thinking string `json:"thinking,omitempty"`
	// input_json_delta
	PartialJSON string `json:"partial_json,omitempty"`
}
