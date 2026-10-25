package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// anthropicTextStream 是覆盖 thinking、signature、text、usage 的最小流。
func anthropicTextStream() []string {
	return []string{
		sseEventText("message_start", `{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":10,"cache_read_input_tokens":3}}}`),
		sseEventText("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`),
		sseEventText("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"let me think"}}`),
		sseEventText("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-abc"}}`),
		sseEventText("content_block_stop", `{"type":"content_block_stop","index":0}`),
		sseEventText("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text"}}`),
		sseEventText("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hel"}}`),
		sseEventText("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"lo"}}`),
		sseEventText("content_block_stop", `{"type":"content_block_stop","index":1}`),
		sseEventText("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`),
		sseEventText("ping", `{"type":"ping"}`),
		sseEventText("message_stop", `{"type":"message_stop"}`),
	}
}

func TestAnthropicStreamTextThinkingUsage(t *testing.T) {
	srv, _ := newAnthropicTestServer(t, anthropicTextStream()...)
	p := NewAnthropic(fastOptions(srv.URL, "k"))

	st, err := p.Stream(context.Background(), Request{Model: "claude-sonnet-4", MaxTokens: 1024})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer st.Close()

	events, err := recvAll(t, st)
	if err != nil {
		t.Fatalf("recvAll: %v", err)
	}

	want := []EventType{
		EventMessageStart,
		EventBlockStart,
		EventThinkingDelta,
		EventSignature,
		EventBlockStart,
		EventTextDelta,
		EventTextDelta,
		EventMessageDelta,
		EventPing,
		EventMessageStop,
	}
	if got := eventTypes(events); !reflect.DeepEqual(got, want) {
		t.Fatalf("事件序列不一致:\n got %v\nwant %v", got, want)
	}

	if events[0].MessageID != "msg_1" {
		t.Fatalf("MessageID = %q", events[0].MessageID)
	}
	if events[0].Usage == nil || events[0].Usage.InputTokens != 10 || events[0].Usage.CacheReadInputTokens != 3 {
		t.Fatalf("message_start usage 不正确: %+v", events[0].Usage)
	}

	if events[1].Kind != BlockThinking || events[1].Index != 0 {
		t.Fatalf("思考块 block_start 不正确: %+v", events[1])
	}
	if events[2].Kind != BlockThinking || events[2].Thinking != "let me think" || events[2].Index != 0 {
		t.Fatalf("thinking_delta 不正确: %+v", events[2])
	}
	if events[3].Type != EventSignature || events[3].Kind != BlockThinking || events[3].Signature != "sig-abc" {
		t.Fatalf("signature 不正确: %+v", events[3])
	}
	if events[4].Kind != BlockText || events[4].Index != 1 {
		t.Fatalf("文本块 block_start 不正确: %+v", events[4])
	}
	if events[5].Kind != BlockText || events[5].Text != "Hel" || events[5].Index != 1 {
		t.Fatalf("text_delta 不正确: %+v", events[5])
	}

	md := events[7]
	if md.StopReason != StopEndTurn {
		t.Fatalf("stop reason = %q", md.StopReason)
	}
	if md.Usage == nil || md.Usage.OutputTokens != 7 {
		t.Fatalf("message_delta usage 不正确: %+v", md.Usage)
	}
}

func TestAnthropicToolUseDeltasAndStop(t *testing.T) {
	frames := []string{
		sseEventText("message_start", `{"type":"message_start","message":{"id":"msg_2"}}`),
		sseEventText("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"Read"}}`),
		sseEventText("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`),
		sseEventText("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"a.go\"}"}}`),
		sseEventText("content_block_stop", `{"type":"content_block_stop","index":0}`),
		sseEventText("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_2","name":"Bash"}}`),
		sseEventText("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{}"}}`),
		sseEventText("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":20}}`),
		sseEventText("message_stop", `{"type":"message_stop"}`),
	}
	srv, _ := newAnthropicTestServer(t, frames...)
	p := NewAnthropic(fastOptions(srv.URL, "k"))

	st, err := p.Stream(context.Background(), Request{Model: "m", MaxTokens: 256})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer st.Close()
	events, err := recvAll(t, st)
	if err != nil {
		t.Fatalf("recvAll: %v", err)
	}

	starts := 0
	for _, ev := range events {
		if ev.Type == EventToolUseStart {
			starts++
			if ev.Kind != BlockToolUse {
				t.Fatalf("tool_use_start 缺少 Kind: %+v", ev)
			}
		}
		if ev.Type == EventToolInputDelta && ev.Kind != BlockToolUse {
			t.Fatalf("tool_input_delta 缺少 Kind: %+v", ev)
		}
	}
	if starts != 2 {
		t.Fatalf("期望 2 个 tool_use_start，实际 %d", starts)
	}
	if _, ok := findEvent(events, EventBlockStart); ok {
		t.Fatal("工具块不应产生 block_start")
	}

	// 第二个工具块没有 content_block_stop，message_stop 必须兜底补发。
	stops := []StreamEvent{}
	for _, ev := range events {
		if ev.Type == EventToolUseStop {
			stops = append(stops, ev)
		}
	}
	if len(stops) != 2 {
		t.Fatalf("期望 2 个 tool_use_stop，实际 %d", len(stops))
	}
	if stops[0].Index != 0 || stops[1].Index != 1 {
		t.Fatalf("兜底 tool_use_stop 必须按索引升序: %+v", stops)
	}

	md, _ := findEvent(events, EventMessageDelta)
	if md.StopReason != StopToolUse {
		t.Fatalf("stop reason = %q", md.StopReason)
	}
}

func TestAnthropicStreamErrorFrame(t *testing.T) {
	frames := []string{
		sseEventText("message_start", `{"type":"message_start","message":{"id":"m"}}`),
		sseEventText("error", `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`),
	}
	srv, _ := newAnthropicTestServer(t, frames...)
	p := NewAnthropic(fastOptions(srv.URL, "k"))

	st, err := p.Stream(context.Background(), Request{Model: "m", MaxTokens: 16})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer st.Close()

	events, err := recvAll(t, st)
	if err == nil {
		t.Fatal("期望流内错误")
	}
	var se *StreamError
	if !errors.As(err, &se) {
		t.Fatalf("期望 *StreamError，得到 %T", err)
	}
	if se.Kind != ErrKindOverloaded || se.Provider != "anthropic" || se.Retryable {
		t.Fatalf("错误分类不正确: %+v", se)
	}
	if events[len(events)-1].Type != EventMessageStart {
		t.Fatalf("错误前的事件应已交付: %v", eventTypes(events))
	}
}

func TestAnthropicRequestEncoding(t *testing.T) {
	srv, bodyCh := newAnthropicTestServer(t, sseEventText("message_stop", `{"type":"message_stop"}`))
	p := NewAnthropic(fastOptions(srv.URL, "k"))

	temp := 0.2
	req := Request{
		Model:       "claude-sonnet-4",
		System:      "be brief",
		MaxTokens:   2048,
		Temperature: &temp,
		Tools: []ToolSpec{{
			Name:        "Read",
			Description: "read a file",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		}},
		Messages: []Message{
			{Role: RoleUser, Blocks: []ContentBlock{
				{Type: BlockToolResult, ToolUseID: "toolu_1", Text: "", IsError: true},
			}},
			{Role: RoleAssistant, Blocks: []ContentBlock{
				{Type: BlockToolUse, ToolUseID: "toolu_2", ToolName: "Read", Input: nil},
			}},
		},
		StopSequences: []string{"STOP"},
	}

	st, err := p.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	_ = st.Close()

	var body anthropicRequest
	if err := json.Unmarshal(<-bodyCh, &body); err != nil {
		t.Fatalf("请求体不是合法 JSON: %v", err)
	}
	if body.Model != "claude-sonnet-4" || body.MaxTokens != 2048 || !body.Stream {
		t.Fatalf("基础字段不正确: %+v", body)
	}
	if body.System != "be brief" || body.Temperature == nil || *body.Temperature != 0.2 {
		t.Fatalf("system/temperature 不正确: %+v", body)
	}
	if len(body.StopSequences) != 1 || body.StopSequences[0] != "STOP" {
		t.Fatalf("stop_sequences 不正确: %+v", body.StopSequences)
	}
	if len(body.Tools) != 1 || body.Tools[0].Name != "Read" || !strings.Contains(string(body.Tools[0].InputSchema), "path") {
		t.Fatalf("tools 编码不正确: %+v", body.Tools)
	}
	if len(body.Messages) != 2 {
		t.Fatalf("消息数不正确: %+v", body.Messages)
	}
	tr := body.Messages[0].Content[0]
	if body.Messages[0].Role != "user" || tr.Type != "tool_result" || tr.ToolUseID != "toolu_1" || !tr.IsError {
		t.Fatalf("tool_result 编码不正确: %+v", tr)
	}
	var placeholder string
	if err := json.Unmarshal(tr.Content, &placeholder); err != nil || placeholder != "<tool returned no output>" {
		t.Fatalf("空工具结果应填占位文本，得到 %q", tr.Content)
	}
	tu := body.Messages[1].Content[0]
	if tu.Type != "tool_use" || tu.ID != "toolu_2" || tu.Name != "Read" || string(tu.Input) != "{}" {
		t.Fatalf("tool_use 编码不正确: %+v", tu)
	}
}

func TestAnthropicThinkingValidation(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	p := NewAnthropic(fastOptions(srv.URL, "k"))
	temp := 0.5
	cases := []struct {
		name string
		req  Request
	}{
		{"budget too small", Request{Model: "m", MaxTokens: 2048, Thinking: &Thinking{Enabled: true, BudgetTokens: 100}}},
		{"budget >= max_tokens", Request{Model: "m", MaxTokens: 1024, Thinking: &Thinking{Enabled: true, BudgetTokens: 1024}}},
		{"temperature conflict", Request{Model: "m", MaxTokens: 4096, Temperature: &temp, Thinking: &Thinking{Enabled: true, BudgetTokens: 1024}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := p.Stream(context.Background(), tc.req)
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("期望 ErrInvalidRequest，得到 %v", err)
			}
			var se *StreamError
			if !errors.As(err, &se) || se.Kind != ErrKindInvalidRequest {
				t.Fatalf("期望 *StreamError{invalid_request}，得到 %v", err)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("非法请求不应发网络请求，实际 %d 次", calls)
	}

	// 合法配置应通过并发出请求。
	st, err := p.Stream(context.Background(), Request{Model: "m", MaxTokens: 4096, Thinking: &Thinking{Enabled: true, BudgetTokens: 1024}})
	if err != nil {
		t.Fatalf("合法 thinking 配置被拒绝: %v", err)
	}
	_ = st.Close()
	if calls != 1 {
		t.Fatalf("合法请求应发出一次，实际 %d", calls)
	}
}

func TestAnthropicEncodesThinkingBlock(t *testing.T) {
	srv, bodyCh := newAnthropicTestServer(t, sseEventText("message_stop", `{"type":"message_stop"}`))
	p := NewAnthropic(fastOptions(srv.URL, "k"))
	req := Request{
		Model:     "m",
		MaxTokens: 4096,
		Messages: []Message{{
			Role: RoleAssistant,
			Blocks: []ContentBlock{
				{Type: BlockThinking, Thinking: "hmm", Signature: "sig"},
				{Type: BlockText, Text: "ok"},
			},
		}},
	}
	st, err := p.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	_ = st.Close()

	var body anthropicRequest
	if err := json.Unmarshal(<-bodyCh, &body); err != nil {
		t.Fatalf("解析请求体: %v", err)
	}
	if len(body.Messages) != 1 || len(body.Messages[0].Content) != 2 {
		t.Fatalf("消息块数不正确: %+v", body.Messages)
	}
	think := body.Messages[0].Content[0]
	if think.Type != "thinking" || think.Thinking != "hmm" || think.Signature != "sig" {
		t.Fatalf("thinking 块编码不正确: %+v", think)
	}
}

func TestAnthropicEOFWithoutMessageStop(t *testing.T) {
	// 服务器写完工具块后直接断连，解码器必须在 EOF 时补 tool_use_stop 与 message_stop。
	frames := []string{
		sseEventText("message_start", `{"type":"message_start","message":{"id":"m"}}`),
		sseEventText("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t1","name":"Read"}}`),
	}
	srv, _ := newAnthropicTestServer(t, frames...)
	p := NewAnthropic(fastOptions(srv.URL, "k"))
	st, err := p.Stream(context.Background(), Request{Model: "m", MaxTokens: 16})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer st.Close()
	events, err := recvAll(t, st)
	if err != nil {
		t.Fatalf("recvAll: %v", err)
	}
	if countEvents(events, EventToolUseStop) != 1 {
		t.Fatalf("EOF 时应补 tool_use_stop: %v", eventTypes(events))
	}
	if last := events[len(events)-1]; last.Type != EventMessageStop {
		t.Fatalf("最后一个事件应是 message_stop，得到 %v", last.Type)
	}
}
