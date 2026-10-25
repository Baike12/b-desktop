package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// Anthropic 是 Anthropic Messages 协议的 Provider 实现，并发安全。
type Anthropic struct {
	cfg providerConfig
}

// NewAnthropic 创建 Anthropic Messages Provider。
// opts.APIKey 为空时 Stream 返回包装 ErrMissingAPIKey 的错误。
func NewAnthropic(opts HTTPOptions) *Anthropic {
	auth := map[string]string{"anthropic-version": "2023-06-01"}
	if opts.APIKey != "" {
		auth["x-api-key"] = opts.APIKey
	}
	return &Anthropic{
		cfg: newProviderConfig(
			"anthropic",
			defaultAnthropicBaseURL,
			"/v1/messages",
			opts,
			auth,
			func() (sseDecoder, error) {
				return &anthropicDecoder{provider: "anthropic", openTools: make(map[int]bool)}, nil
			},
			func(status int, _ http.Header, body []byte) *StreamError {
				return classifyHTTPError("anthropic", status, body)
			},
		),
	}
}

// Name 返回 "anthropic"。
func (a *Anthropic) Name() string { return "anthropic" }

// Stream 发起一次 Messages 流式请求。
// 请求参数不满足供应商约束时在发请求前返回包装 ErrInvalidRequest 的 *StreamError。
func (a *Anthropic) Stream(ctx context.Context, req Request) (Stream, error) {
	if err := validateAnthropicRequest(req); err != nil {
		return nil, err
	}
	payload, err := encodeAnthropicRequest(req)
	if err != nil {
		return nil, invalidRequest("anthropic", err.Error())
	}
	return a.cfg.stream(ctx, payload)
}

// validateAnthropicRequest 校验扩展思考约束。
func validateAnthropicRequest(req Request) error {
	if req.Thinking == nil || !req.Thinking.Enabled {
		return nil
	}
	if req.Thinking.BudgetTokens < 1024 {
		return invalidRequest("anthropic", fmt.Sprintf("thinking budget %d must be at least 1024", req.Thinking.BudgetTokens))
	}
	if req.MaxTokens > 0 && req.Thinking.BudgetTokens >= req.MaxTokens {
		return invalidRequest("anthropic", fmt.Sprintf("thinking budget %d must be less than max_tokens %d", req.Thinking.BudgetTokens, req.MaxTokens))
	}
	if req.Temperature != nil {
		return invalidRequest("anthropic", "temperature cannot be used with extended thinking")
	}
	return nil
}

// anthropicRequest 是 Messages API 的请求体。
type anthropicRequest struct {
	Model         string             `json:"model"`
	MaxTokens     int                `json:"max_tokens"`
	System        string             `json:"system,omitempty"`
	Messages      []anthropicMessage `json:"messages"`
	Tools         []anthropicTool    `json:"tools,omitempty"`
	Stream        bool               `json:"stream"`
	Temperature   *float64           `json:"temperature,omitempty"`
	Thinking      *anthropicThinking `json:"thinking,omitempty"`
	StopSequences []string           `json:"stop_sequences,omitempty"`
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

type anthropicBlock struct {
	Type      string                `json:"type"`
	Text      string                `json:"text,omitempty"`
	Thinking  string                `json:"thinking,omitempty"`
	Signature string                `json:"signature,omitempty"`
	ID        string                `json:"id,omitempty"`
	Name      string                `json:"name,omitempty"`
	Input     json.RawMessage       `json:"input,omitempty"`
	ToolUseID string                `json:"tool_use_id,omitempty"`
	Content   json.RawMessage       `json:"content,omitempty"`
	IsError   bool                  `json:"is_error,omitempty"`
	Source    *anthropicImageSource `json:"source,omitempty"`
}

// anthropicImageSource 是 Anthropic 图片块的 base64 来源描述。
type anthropicImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// anthropicImageBlock 把统一图片字段编码成 Anthropic 的 image 块。
// 空数据的图片直接丢弃：发出去只会换来 "image cannot be empty" 的 400。
func anthropicImageBlock(mediaType, data string) (anthropicBlock, bool) {
	if mediaType == "" || data == "" {
		return anthropicBlock{}, false
	}
	return anthropicBlock{
		Type: "image",
		Source: &anthropicImageSource{
			Type:      "base64",
			MediaType: mediaType,
			Data:      data,
		},
	}, true
}

// anthropicToolResultContent 编码 tool_result 的 content 字段：
// 没有附属图片时保持字符串形式（与旧 transcript 兼容），有图片时
// Anthropic 要求 content 是 text/image 块数组。
func anthropicToolResultContent(text string, images []Image) json.RawMessage {
	if len(images) == 0 {
		raw, err := json.Marshal(text)
		if err != nil {
			return json.RawMessage(`"<tool returned no output>"`)
		}
		return raw
	}
	parts := make([]anthropicBlock, 0, len(images)+1)
	if text != "" {
		parts = append(parts, anthropicBlock{Type: "text", Text: text})
	}
	for _, img := range images {
		if block, ok := anthropicImageBlock(img.MediaType, img.Data); ok {
			parts = append(parts, block)
		}
	}
	if len(parts) == 0 {
		parts = append(parts, anthropicBlock{Type: "text", Text: "<tool returned no output>"})
	}
	raw, err := json.Marshal(parts)
	if err != nil {
		return json.RawMessage(`"<tool returned no output>"`)
	}
	return raw
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

// encodeAnthropicRequest 把统一 Request 编码成 Messages API 的请求体。
func encodeAnthropicRequest(req Request) ([]byte, error) {
	out := anthropicRequest{
		Model:     req.Model,
		MaxTokens: req.MaxTokens,
		System:    req.System,
		Stream:    true,
		Temperature: func() *float64 {
			if req.Thinking != nil && req.Thinking.Enabled {
				return nil
			}
			return req.Temperature
		}(),
		StopSequences: req.StopSequences,
	}
	if req.Thinking != nil && req.Thinking.Enabled {
		out.Thinking = &anthropicThinking{Type: "enabled", BudgetTokens: req.Thinking.BudgetTokens}
	}
	if len(req.Tools) > 0 {
		out.Tools = make([]anthropicTool, 0, len(req.Tools))
		for _, t := range req.Tools {
			out.Tools = append(out.Tools, anthropicTool{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: normalizeSchema(t.InputSchema),
			})
		}
	}

	for _, m := range req.Messages {
		role := string(m.Role)
		if role != string(RoleUser) && role != string(RoleAssistant) {
			continue
		}
		blocks := make([]anthropicBlock, 0, len(m.Blocks))
		for _, b := range m.Blocks {
			switch b.Type {
			case BlockText:
				if b.Text == "" {
					continue
				}
				blocks = append(blocks, anthropicBlock{Type: "text", Text: b.Text})
			case BlockThinking:
				if b.Thinking == "" {
					continue
				}
				blocks = append(blocks, anthropicBlock{
					Type:      "thinking",
					Thinking:  b.Thinking,
					Signature: b.Signature,
				})
			case BlockToolUse:
				blocks = append(blocks, anthropicBlock{
					Type:  "tool_use",
					ID:    b.ToolUseID,
					Name:  b.ToolName,
					Input: normalizeInput(b.Input),
				})
			case BlockToolResult:
				content := b.Text
				if content == "" && len(b.Images) == 0 {
					content = "<tool returned no output>"
				}
				blocks = append(blocks, anthropicBlock{
					Type:      "tool_result",
					ToolUseID: b.ToolUseID,
					Content:   anthropicToolResultContent(content, b.Images),
					IsError:   b.IsError,
				})
			case BlockImage:
				if block, ok := anthropicImageBlock(b.MediaType, b.Data); ok {
					blocks = append(blocks, block)
				}
			}
		}
		if len(blocks) == 0 {
			continue
		}
		out.Messages = append(out.Messages, anthropicMessage{Role: role, Content: blocks})
	}
	return json.Marshal(out)
}

// normalizeInput 保证 tool_use 的 input 是合法 JSON 对象文本。
func normalizeInput(raw json.RawMessage) json.RawMessage {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(trimmed)
}

// normalizeSchema 保证 input_schema 是合法 JSON 对象文本。
func normalizeSchema(raw json.RawMessage) json.RawMessage {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return json.RawMessage(trimmed)
}

// anthropicUsage 是 Messages API 的 usage 结构。
type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// toUsage 转换成统一 Usage。
func (u *anthropicUsage) toUsage() *Usage {
	if u == nil {
		return nil
	}
	return &Usage{
		InputTokens:              u.InputTokens,
		OutputTokens:             u.OutputTokens,
		CacheReadInputTokens:     u.CacheReadInputTokens,
		CacheCreationInputTokens: u.CacheCreationInputTokens,
	}
}

// anthropicDecoder 把 Messages SSE 帧映射成 StreamEvent。
type anthropicDecoder struct {
	provider  string
	openTools map[int]bool
	stopped   bool
}

// anthropicEnvelope 是 Messages SSE 的事件体。
type anthropicEnvelope struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		ID    string          `json:"id"`
		Usage *anthropicUsage `json:"usage"`
	} `json:"message"`
	ContentBlock *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *anthropicUsage `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Decode 实现 sseDecoder。
func (d *anthropicDecoder) Decode(frame sseFrame) ([]StreamEvent, bool, error) {
	var env anthropicEnvelope
	if err := json.Unmarshal(frame.Data, &env); err != nil {
		return nil, false, &StreamError{
			Kind:     ErrKindUnknown,
			Message:  "invalid anthropic event: " + err.Error(),
			Provider: d.provider,
		}
	}
	kind := env.Type
	if kind == "" {
		kind = frame.Event
	}

	switch kind {
	case "message_start":
		ev := StreamEvent{Type: EventMessageStart}
		if env.Message != nil {
			ev.MessageID = env.Message.ID
			ev.Usage = env.Message.Usage.toUsage()
		}
		return []StreamEvent{ev}, false, nil

	case "content_block_start":
		if env.ContentBlock == nil {
			return nil, false, nil
		}
		switch env.ContentBlock.Type {
		case "tool_use":
			d.openTools[env.Index] = true
			return []StreamEvent{{
				Type:      EventToolUseStart,
				Index:     env.Index,
				Kind:      BlockToolUse,
				ToolUseID: env.ContentBlock.ID,
				ToolName:  env.ContentBlock.Name,
			}}, false, nil
		case "thinking":
			return []StreamEvent{{
				Type:  EventBlockStart,
				Index: env.Index,
				Kind:  BlockThinking,
			}}, false, nil
		case "text":
			return []StreamEvent{{
				Type:  EventBlockStart,
				Index: env.Index,
				Kind:  BlockText,
			}}, false, nil
		}
		return nil, false, nil

	case "content_block_delta":
		if env.Delta == nil {
			return nil, false, nil
		}
		switch env.Delta.Type {
		case "text_delta":
			if env.Delta.Text == "" {
				return nil, false, nil
			}
			return []StreamEvent{{
				Type:  EventTextDelta,
				Index: env.Index,
				Kind:  BlockText,
				Text:  env.Delta.Text,
			}}, false, nil
		case "thinking_delta":
			if env.Delta.Thinking == "" {
				return nil, false, nil
			}
			return []StreamEvent{{
				Type:     EventThinkingDelta,
				Index:    env.Index,
				Kind:     BlockThinking,
				Thinking: env.Delta.Thinking,
			}}, false, nil
		case "signature_delta":
			if env.Delta.Signature == "" {
				return nil, false, nil
			}
			return []StreamEvent{{
				Type:      EventSignature,
				Index:     env.Index,
				Kind:      BlockThinking,
				Signature: env.Delta.Signature,
			}}, false, nil
		case "input_json_delta":
			if env.Delta.PartialJSON == "" {
				return nil, false, nil
			}
			return []StreamEvent{{
				Type:        EventToolInputDelta,
				Index:       env.Index,
				Kind:        BlockToolUse,
				PartialJSON: env.Delta.PartialJSON,
			}}, false, nil
		}
		return nil, false, nil

	case "content_block_stop":
		if !d.openTools[env.Index] {
			return nil, false, nil
		}
		delete(d.openTools, env.Index)
		return []StreamEvent{{
			Type:  EventToolUseStop,
			Index: env.Index,
			Kind:  BlockToolUse,
		}}, false, nil

	case "message_delta":
		ev := StreamEvent{Type: EventMessageDelta}
		if env.Delta != nil {
			ev.StopReason = mapAnthropicStopReason(env.Delta.StopReason)
		}
		ev.Usage = env.Usage.toUsage()
		return []StreamEvent{ev}, false, nil

	case "message_stop":
		events := d.closeOpenTools()
		d.stopped = true
		events = append(events, StreamEvent{Type: EventMessageStop})
		return events, true, nil

	case "ping":
		return []StreamEvent{{Type: EventPing}}, false, nil

	case "error":
		msg := "anthropic stream error"
		errType := ""
		if env.Error != nil {
			if env.Error.Message != "" {
				msg = env.Error.Message
			}
			errType = env.Error.Type
		}
		return nil, false, &StreamError{
			Kind:      anthropicErrorKind(errType, msg),
			Message:   msg,
			Retryable: false,
			Provider:  d.provider,
		}
	}
	return nil, false, nil
}

// Finish 在连接 EOF 时补齐未关闭的工具块与 message_stop。
func (d *anthropicDecoder) Finish() ([]StreamEvent, error) {
	if d.stopped {
		return nil, nil
	}
	d.stopped = true
	events := d.closeOpenTools()
	events = append(events, StreamEvent{Type: EventMessageStop})
	return events, nil
}

// closeOpenTools 按块索引升序为未关闭的工具块补发 tool_use_stop。
func (d *anthropicDecoder) closeOpenTools() []StreamEvent {
	if len(d.openTools) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(d.openTools))
	for idx := range d.openTools {
		indexes = append(indexes, idx)
	}
	sort.Ints(indexes)
	events := make([]StreamEvent, 0, len(indexes))
	for _, idx := range indexes {
		delete(d.openTools, idx)
		events = append(events, StreamEvent{
			Type:  EventToolUseStop,
			Index: idx,
			Kind:  BlockToolUse,
		})
	}
	return events
}

// mapAnthropicStopReason 映射 Messages API 的 stop_reason。
func mapAnthropicStopReason(s string) StopReason {
	switch s {
	case "end_turn", "stop_sequence":
		return StopEndTurn
	case "tool_use":
		return StopToolUse
	case "max_tokens":
		return StopMaxTokens
	case "refusal":
		return StopRefusal
	default:
		return ""
	}
}

// anthropicErrorKind 分类流内错误。
func anthropicErrorKind(errType, msg string) ErrorKind {
	switch errType {
	case "overloaded_error":
		return ErrKindOverloaded
	case "rate_limit_error":
		return ErrKindRateLimit
	case "authentication_error", "permission_error":
		return ErrKindAuth
	case "invalid_request_error":
		if looksLikeContextOverflow(msg) {
			return ErrKindContextOverflow
		}
		return ErrKindInvalidRequest
	case "api_error":
		return ErrKindServer
	}
	if looksLikeContextOverflow(msg) {
		return ErrKindContextOverflow
	}
	return ErrKindUnknown
}
