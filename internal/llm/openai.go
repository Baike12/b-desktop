package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

// OpenAI 是 OpenAI Chat Completions 协议的 Provider 实现，并发安全。
type OpenAI struct {
	cfg providerConfig
}

// NewOpenAI 创建 OpenAI Chat Completions Provider。
// opts.APIKey 为空时 Stream 返回包装 ErrMissingAPIKey 的错误。
func NewOpenAI(opts HTTPOptions) *OpenAI {
	auth := map[string]string{}
	if opts.APIKey != "" {
		auth["Authorization"] = "Bearer " + opts.APIKey
	}
	return &OpenAI{
		cfg: newProviderConfig(
			"openai",
			defaultOpenAIBaseURL,
			"/v1/chat/completions",
			opts,
			auth,
			func() (sseDecoder, error) {
				return &openAIDecoder{
					provider: "openai",
					tools:    make(map[int]*openAIToolState),
				}, nil
			},
			func(status int, _ http.Header, body []byte) *StreamError {
				return classifyHTTPError("openai", status, body)
			},
		),
	}
}

// Name 返回 "openai"。
func (o *OpenAI) Name() string { return "openai" }

// Stream 发起一次 Chat Completions 流式请求。
func (o *OpenAI) Stream(ctx context.Context, req Request) (Stream, error) {
	payload, err := encodeOpenAIRequest(req)
	if err != nil {
		return nil, invalidRequest("openai", err.Error())
	}
	return o.cfg.stream(ctx, payload)
}

// openAIRequest 是 Chat Completions 的请求体。
type openAIRequest struct {
	Model         string            `json:"model"`
	Messages      []openAIMessage   `json:"messages"`
	Tools         []openAITool      `json:"tools,omitempty"`
	Stream        bool              `json:"stream"`
	StreamOptions *openAIStreamOpts `json:"stream_options,omitempty"`
	MaxTokens     int               `json:"max_tokens,omitempty"`
	Temperature   *float64          `json:"temperature,omitempty"`
	Stop          []string          `json:"stop,omitempty"`
}

type openAIStreamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openAIToolCall struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAITool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

// encodeOpenAIRequest 把统一 Request 编码成 Chat Completions 的请求体。
func encodeOpenAIRequest(req Request) ([]byte, error) {
	out := openAIRequest{
		Model:         req.Model,
		Stream:        true,
		StreamOptions: &openAIStreamOpts{IncludeUsage: true},
		MaxTokens:     req.MaxTokens,
		Temperature:   req.Temperature,
		Stop:          req.StopSequences,
	}
	if req.System != "" {
		out.Messages = append(out.Messages, openAIMessage{Role: "system", Content: req.System})
	}
	if len(req.Tools) > 0 {
		out.Tools = make([]openAITool, 0, len(req.Tools))
		for _, t := range req.Tools {
			var tool openAITool
			tool.Type = "function"
			tool.Function.Name = t.Name
			tool.Function.Description = t.Description
			tool.Function.Parameters = normalizeSchema(t.InputSchema)
			out.Tools = append(out.Tools, tool)
		}
	}

	for _, m := range req.Messages {
		switch m.Role {
		case RoleAssistant:
			msg := openAIMessage{Role: "assistant"}
			var text strings.Builder
			for _, b := range m.Blocks {
				switch b.Type {
				case BlockText:
					if b.Text != "" {
						if text.Len() > 0 {
							text.WriteByte('\n')
						}
						text.WriteString(b.Text)
					}
				case BlockToolUse:
					args := strings.TrimSpace(string(b.Input))
					if args == "" || args == "null" {
						args = "{}"
					}
					msg.ToolCalls = append(msg.ToolCalls, openAIToolCall{
						ID:   b.ToolUseID,
						Type: "function",
						Function: openAIFunction{
							Name:      b.ToolName,
							Arguments: args,
						},
					})
				}
				// thinking 块在 OpenAI 协议下不发送。
			}
			msg.Content = text.String()
			if msg.Content == "" && len(msg.ToolCalls) == 0 {
				continue
			}
			out.Messages = append(out.Messages, msg)
		case RoleUser:
			var text strings.Builder
			flushText := func() {
				if text.Len() > 0 {
					out.Messages = append(out.Messages, openAIMessage{Role: "user", Content: text.String()})
					text.Reset()
				}
			}
			for _, b := range m.Blocks {
				switch b.Type {
				case BlockText:
					if b.Text != "" {
						if text.Len() > 0 {
							text.WriteByte('\n')
						}
						text.WriteString(b.Text)
					}
				case BlockToolResult:
					// 工具结果按块顺序展开成独立的 tool 消息。
					flushText()
					content := b.Text
					if content == "" {
						content = "<tool returned no output>"
					}
					out.Messages = append(out.Messages, openAIMessage{
						Role:       "tool",
						Content:    content,
						ToolCallID: b.ToolUseID,
					})
				}
			}
			flushText()
		}
	}
	return json.Marshal(out)
}

// openAIToolState 是单个工具调用的流式状态。
type openAIToolState struct {
	blockIndex int
	id         string
	name       string
	closed     bool
}

// openAIDecoder 把 Chat Completions SSE 帧映射成 StreamEvent。
// OpenAI 的正文与 tool_calls 使用两套独立编号，因此块索引由本解码器分配。
type openAIDecoder struct {
	provider string

	nextBlock int
	// textOpen / thinkOpen 标记当前是否有正文块 / 思考块处于打开状态。
	// 打开状态下到达的增量并入该块；块被关闭后，下一次增量会分配新索引，
	// 从而让「工具调用之后又出现的正文」成为独立块，而不是被并回前一段。
	textOpen  bool
	text      int
	thinkOpen bool
	thinking  int

	tools      map[int]*openAIToolState // key 是 provider 的 tool_calls[].index
	messageSet bool
	finished   bool

	stopReason StopReason
	sawFinish  bool
	usage      *Usage
}

// closeContentBlocks 关闭当前打开的正文块与思考块。
//
// Chat Completions 把正文与工具调用放在同一条消息里，但兼容端点会在工具调用
// 之后继续下发正文（对应 Anthropic 的多个 content block）。不关闭块的话，
// 工具之后的正文会沿用同一个索引并被并入工具之前那段，工具调用之间的模型输出
// 就此丢失。工具块开始时调用它。
func (d *openAIDecoder) closeContentBlocks() {
	d.textOpen = false
	d.thinkOpen = false
}

// openAIChunk 是 Chat Completions 的一个流式块。
type openAIChunk struct {
	ID      string `json:"id"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role             string `json:"role"`
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// Decode 实现 sseDecoder。
func (d *openAIDecoder) Decode(frame sseFrame) ([]StreamEvent, bool, error) {
	data := strings.TrimSpace(string(frame.Data))
	if data == "" {
		return nil, false, nil
	}
	if data == "[DONE]" {
		events := d.finish()
		return events, true, nil
	}

	var chunk openAIChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return nil, false, &StreamError{
			Kind:     ErrKindUnknown,
			Message:  "invalid openai chunk: " + err.Error(),
			Provider: d.provider,
		}
	}
	if chunk.Error != nil {
		return nil, false, &StreamError{
			Kind:      openAIErrorKind(chunk.Error.Type, chunk.Error.Code, chunk.Error.Message),
			Message:   firstNonEmpty(chunk.Error.Message, chunk.Error.Code, chunk.Error.Type, "openai stream error"),
			Retryable: false,
			Provider:  d.provider,
		}
	}

	var events []StreamEvent
	if !d.messageSet {
		d.messageSet = true
		events = append(events, StreamEvent{Type: EventMessageStart, MessageID: chunk.ID})
	}
	if chunk.Usage != nil {
		d.usage = &Usage{
			InputTokens:  chunk.Usage.PromptTokens,
			OutputTokens: chunk.Usage.CompletionTokens,
		}
		if chunk.Usage.PromptTokensDetails != nil {
			d.usage.CacheReadInputTokens = chunk.Usage.PromptTokensDetails.CachedTokens
		}
	}

	for _, choice := range chunk.Choices {
		delta := choice.Delta

		if delta.Content != "" {
			if !d.textOpen {
				d.textOpen = true
				d.text = d.nextBlock
				d.nextBlock++
				events = append(events, StreamEvent{Type: EventBlockStart, Index: d.text, Kind: BlockText})
			}
			events = append(events, StreamEvent{Type: EventTextDelta, Index: d.text, Kind: BlockText, Text: delta.Content})
		}

		// 部分 OpenAI 兼容端点用 reasoning_content 表达思考过程。
		if delta.ReasoningContent != "" {
			if !d.thinkOpen {
				d.thinkOpen = true
				d.thinking = d.nextBlock
				d.nextBlock++
				events = append(events, StreamEvent{Type: EventBlockStart, Index: d.thinking, Kind: BlockThinking})
			}
			events = append(events, StreamEvent{
				Type:     EventThinkingDelta,
				Index:    d.thinking,
				Kind:     BlockThinking,
				Thinking: delta.ReasoningContent,
			})
		}

		for _, tc := range delta.ToolCalls {
			st, ok := d.tools[tc.Index]
			if !ok {
				// 工具调用开始意味着上一段正文已经结束：关闭正文块与思考块，
				// 工具之后到达的正文会分配新索引，成为独立内容块。
				d.closeContentBlocks()
				st = &openAIToolState{
					blockIndex: d.nextBlock,
					id:         tc.ID,
					name:       tc.Function.Name,
				}
				d.nextBlock++
				d.tools[tc.Index] = st
				events = append(events, StreamEvent{
					Type:      EventToolUseStart,
					Index:     st.blockIndex,
					Kind:      BlockToolUse,
					ToolUseID: st.id,
					ToolName:  st.name,
				})
			} else {
				if st.id == "" {
					st.id = tc.ID
				}
				if st.name == "" {
					st.name = tc.Function.Name
				}
			}
			if tc.Function.Arguments != "" {
				events = append(events, StreamEvent{
					Type:        EventToolInputDelta,
					Index:       st.blockIndex,
					Kind:        BlockToolUse,
					ToolUseID:   st.id,
					ToolName:    st.name,
					PartialJSON: tc.Function.Arguments,
				})
			}
		}

		if choice.FinishReason != "" {
			d.sawFinish = true
			d.stopReason = mapOpenAIStopReason(choice.FinishReason)
			events = append(events, d.closeOpenTools()...)
		}
	}
	return events, false, nil
}

// finish 产出收尾事件：关闭工具块、message_delta（含 usage）与 message_stop。
func (d *openAIDecoder) finish() []StreamEvent {
	if d.finished {
		return nil
	}
	d.finished = true
	events := d.closeOpenTools()
	if d.sawFinish || d.usage != nil {
		events = append(events, StreamEvent{
			Type:       EventMessageDelta,
			StopReason: d.stopReason,
			Usage:      d.usage,
		})
	}
	events = append(events, StreamEvent{Type: EventMessageStop})
	return events
}

// Finish 在连接 EOF 时补齐收尾事件。
func (d *openAIDecoder) Finish() ([]StreamEvent, error) {
	return d.finish(), nil
}

// closeOpenTools 按块索引升序补发尚未关闭的 tool_use_stop。
func (d *openAIDecoder) closeOpenTools() []StreamEvent {
	if len(d.tools) == 0 {
		return nil
	}
	var open []*openAIToolState
	for _, st := range d.tools {
		if !st.closed {
			open = append(open, st)
		}
	}
	if len(open) == 0 {
		return nil
	}
	sort.Slice(open, func(i, j int) bool { return open[i].blockIndex < open[j].blockIndex })
	events := make([]StreamEvent, 0, len(open))
	for _, st := range open {
		st.closed = true
		events = append(events, StreamEvent{
			Type:      EventToolUseStop,
			Index:     st.blockIndex,
			Kind:      BlockToolUse,
			ToolUseID: st.id,
			ToolName:  st.name,
		})
	}
	return events
}

// mapOpenAIStopReason 映射 Chat Completions 的 finish_reason。
func mapOpenAIStopReason(s string) StopReason {
	switch s {
	case "stop":
		return StopEndTurn
	case "tool_calls", "function_call":
		return StopToolUse
	case "length":
		return StopMaxTokens
	case "content_filter":
		return StopRefusal
	default:
		return ""
	}
}

// openAIErrorKind 分类流内错误。
func openAIErrorKind(errType, code, msg string) ErrorKind {
	haystack := strings.ToLower(errType + " " + code + " " + msg)
	switch {
	case strings.Contains(haystack, "context_length"), strings.Contains(haystack, "context length"),
		strings.Contains(haystack, "maximum context"), strings.Contains(haystack, "too many tokens"):
		return ErrKindContextOverflow
	case strings.Contains(haystack, "rate_limit"):
		return ErrKindRateLimit
	case strings.Contains(haystack, "overloaded"):
		return ErrKindOverloaded
	case strings.Contains(haystack, "invalid_api_key"), strings.Contains(haystack, "authentication"),
		strings.Contains(haystack, "unauthorized"), strings.Contains(haystack, "permission"):
		return ErrKindAuth
	case strings.Contains(haystack, "invalid_request"):
		return ErrKindInvalidRequest
	case strings.Contains(haystack, "server_error"), strings.Contains(haystack, "internal"):
		return ErrKindServer
	default:
		return ErrKindUnknown
	}
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// 编译期断言：两种协议都满足 Provider / 解码器契约。
var (
	_ Provider    = (*Anthropic)(nil)
	_ Provider    = (*OpenAI)(nil)
	_ Stream      = (*sseStream)(nil)
	_ Stream      = (*retryStream)(nil)
	_ sseDecoder  = (*anthropicDecoder)(nil)
	_ sseDecoder  = (*openAIDecoder)(nil)
	_ sseFinisher = (*anthropicDecoder)(nil)
	_ sseFinisher = (*openAIDecoder)(nil)
	_ error       = (*StreamError)(nil)
	_ error       = (*wrappedStreamError)(nil)
)
