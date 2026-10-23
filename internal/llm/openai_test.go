package llm

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// openAIStream 覆盖正文、reasoning、两个工具调用、usage 与收尾。
func openAIStream() []string {
	return []string{
		sseFrameText(`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"role":"assistant"}}]}`),
		sseFrameText(`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"Hel"}}]}`),
		sseFrameText(`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"lo","reasoning_content":"thinking..."}}]}`),
		sseFrameText(`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Read","arguments":""}}]}}]}`),
		sseFrameText(`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`),
		sseFrameText(`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_2","type":"function","function":{"name":"Write","arguments":"{}"}}]}}]}`),
		sseFrameText(`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`),
		sseFrameText(`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`),
		sseFrameText(`{"id":"chatcmpl-1","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":4}}}`),
		sseFrameText(`[DONE]`),
	}
}

func TestOpenAIStreamMapping(t *testing.T) {
	srv, _ := newOpenAITestServer(t, openAIStream()...)
	p := NewOpenAI(fastOptions(srv.URL, "k"))

	st, err := p.Stream(context.Background(), Request{Model: "gpt-4o", MaxTokens: 256})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer st.Close()
	events, err := recvAll(t, st)
	if err != nil {
		t.Fatalf("recvAll: %v", err)
	}

	if events[0].Type != EventMessageStart || events[0].MessageID != "chatcmpl-1" {
		t.Fatalf("首事件应为 message_start，得到 %+v", events[0])
	}
	if countEvents(events, EventMessageStart) != 1 {
		t.Fatalf("message_start 只能出现一次: %v", eventTypes(events))
	}

	textStart, _ := findEvent(events, EventBlockStart)
	if textStart.Kind != BlockText || textStart.Index != 0 {
		t.Fatalf("文本块索引不正确: %+v", textStart)
	}
	var text string
	for _, ev := range events {
		if ev.Type == EventTextDelta {
			if ev.Kind != BlockText || ev.Index != textStart.Index {
				t.Fatalf("text_delta 的 Kind/Index 不正确: %+v", ev)
			}
			text += ev.Text
		}
	}
	if text != "Hello" {
		t.Fatalf("文本增量拼接 = %q", text)
	}

	var thinking string
	for _, ev := range events {
		if ev.Type == EventThinkingDelta {
			if ev.Kind != BlockThinking || ev.Index == textStart.Index {
				t.Fatalf("thinking_delta 必须使用独立索引: %+v", ev)
			}
			thinking += ev.Thinking
		}
	}
	if thinking != "thinking..." {
		t.Fatalf("reasoning_content 未映射成 thinking: %q", thinking)
	}

	// 两个工具块必须获得互不相同的索引，且都不等于文本块索引。
	toolStart := map[int]StreamEvent{}
	for _, ev := range events {
		if ev.Type == EventToolUseStart {
			if ev.Kind != BlockToolUse {
				t.Fatalf("tool_use_start 缺少 Kind: %+v", ev)
			}
			toolStart[ev.Index] = ev
		}
	}
	if len(toolStart) != 2 {
		t.Fatalf("期望 2 个工具块，得到 %d: %v", len(toolStart), eventTypes(events))
	}
	seen := map[int]bool{textStart.Index: true}
	for idx, ev := range toolStart {
		if seen[idx] {
			t.Fatalf("块索引 %d 被复用", idx)
		}
		seen[idx] = true
		if ev.ToolUseID == "" || ev.ToolName == "" {
			t.Fatalf("工具块缺少 id/name: %+v", ev)
		}
	}

	// 收尾顺序：tool_use_stop（按块索引升序）→ message_delta → message_stop。
	var tail []EventType
	for _, ev := range events {
		if ev.Type == EventToolUseStop || ev.Type == EventMessageDelta || ev.Type == EventMessageStop {
			tail = append(tail, ev.Type)
		}
	}
	wantTail := []EventType{EventToolUseStop, EventToolUseStop, EventMessageDelta, EventMessageStop}
	if !reflect.DeepEqual(tail, wantTail) {
		t.Fatalf("收尾顺序不正确: %v", tail)
	}
	var stopIdx []int
	for _, ev := range events {
		if ev.Type == EventToolUseStop {
			stopIdx = append(stopIdx, ev.Index)
		}
	}
	if !(stopIdx[0] < stopIdx[1]) {
		t.Fatalf("tool_use_stop 必须按块索引升序: %v", stopIdx)
	}

	md, _ := findEvent(events, EventMessageDelta)
	if md.StopReason != StopToolUse {
		t.Fatalf("finish_reason=tool_calls 应映射为 tool_use，得到 %q", md.StopReason)
	}
	if md.Usage == nil || md.Usage.InputTokens != 12 || md.Usage.OutputTokens != 5 || md.Usage.CacheReadInputTokens != 4 {
		t.Fatalf("usage 未合并进 message_delta: %+v", md.Usage)
	}
	if events[len(events)-1].Type != EventMessageStop {
		t.Fatalf("最后事件应为 message_stop: %v", eventTypes(events))
	}
}

func TestOpenAIDoneClosesToolsWithoutFinish(t *testing.T) {
	frames := []string{
		sseFrameText(`{"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Read","arguments":"{}"}}]}}]}`),
		sseFrameText(`[DONE]`),
	}
	srv, _ := newOpenAITestServer(t, frames...)
	p := NewOpenAI(fastOptions(srv.URL, "k"))
	st, err := p.Stream(context.Background(), Request{Model: "m"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer st.Close()
	events, err := recvAll(t, st)
	if err != nil {
		t.Fatalf("recvAll: %v", err)
	}
	if countEvents(events, EventToolUseStop) != 1 {
		t.Fatalf("[DONE] 时应补 tool_use_stop: %v", eventTypes(events))
	}
	n := len(events)
	if events[n-2].Type != EventToolUseStop || events[n-1].Type != EventMessageStop {
		t.Fatalf("[DONE] 收尾顺序不正确: %v", eventTypes(events))
	}
}

func TestOpenAIEOFFinishesStream(t *testing.T) {
	frames := []string{
		sseFrameText(`{"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Read","arguments":"{}"}}]}}]}`),
	}
	srv, _ := newOpenAITestServer(t, frames...)
	p := NewOpenAI(fastOptions(srv.URL, "k"))
	st, err := p.Stream(context.Background(), Request{Model: "m"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer st.Close()
	events, err := recvAll(t, st)
	if err != nil {
		t.Fatalf("recvAll: %v", err)
	}
	if countEvents(events, EventToolUseStop) != 1 || countEvents(events, EventMessageStop) != 1 {
		t.Fatalf("EOF 时应补齐收尾事件: %v", eventTypes(events))
	}
}

func TestOpenAIRequestEncoding(t *testing.T) {
	srv, bodyCh := newOpenAITestServer(t, sseFrameText(`[DONE]`))
	p := NewOpenAI(fastOptions(srv.URL, "k"))

	temp := 0.3
	req := Request{
		Model:       "gpt-4o",
		System:      "you are helpful",
		MaxTokens:   512,
		Temperature: &temp,
		Tools: []ToolSpec{{
			Name:        "Read",
			Description: "read a file",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
		Messages: []Message{
			{Role: RoleUser, Blocks: []ContentBlock{{Type: BlockText, Text: "hi"}}},
			{Role: RoleAssistant, Blocks: []ContentBlock{
				{Type: BlockThinking, Thinking: "secret", Signature: "s"},
				{Type: BlockText, Text: "answer"},
				{Type: BlockToolUse, ToolUseID: "call_1", ToolName: "Read", Input: json.RawMessage(`{"path":"a.go"}`)},
			}},
			{Role: RoleUser, Blocks: []ContentBlock{
				{Type: BlockToolResult, ToolUseID: "call_1", Text: ""},
			}},
			{Role: RoleAssistant, Blocks: []ContentBlock{
				{Type: BlockThinking, Thinking: "only thinking"},
			}},
		},
	}
	st, err := p.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	_ = st.Close()

	var body openAIRequest
	if err := json.Unmarshal(<-bodyCh, &body); err != nil {
		t.Fatalf("解析请求体: %v", err)
	}
	if body.Model != "gpt-4o" || !body.Stream || body.MaxTokens != 512 {
		t.Fatalf("基础字段不正确: %+v", body)
	}
	if body.StreamOptions == nil || !body.StreamOptions.IncludeUsage {
		t.Fatal("必须请求 include_usage")
	}
	if body.Temperature == nil || *body.Temperature != 0.3 {
		t.Fatalf("temperature 不正确: %+v", body.Temperature)
	}
	if len(body.Tools) != 1 || body.Tools[0].Type != "function" || body.Tools[0].Function.Name != "Read" {
		t.Fatalf("tools 编码不正确: %+v", body.Tools)
	}

	if len(body.Messages) != 4 {
		t.Fatalf("消息数不正确（thinking-only assistant 应被丢弃）: %d -> %+v", len(body.Messages), body.Messages)
	}
	if body.Messages[0].Role != "system" || body.Messages[0].Content != "you are helpful" {
		t.Fatalf("system 消息不正确: %+v", body.Messages[0])
	}
	if body.Messages[1].Role != "user" || body.Messages[1].Content != "hi" {
		t.Fatalf("user 消息不正确: %+v", body.Messages[1])
	}
	asst := body.Messages[2]
	if asst.Role != "assistant" || asst.Content != "answer" {
		t.Fatalf("assistant 消息不正确: %+v", asst)
	}
	if len(asst.ToolCalls) != 1 || asst.ToolCalls[0].ID != "call_1" ||
		asst.ToolCalls[0].Function.Name != "Read" || asst.ToolCalls[0].Function.Arguments != `{"path":"a.go"}` {
		t.Fatalf("tool_calls 编码不正确: %+v", asst.ToolCalls)
	}
	toolMsg := body.Messages[3]
	if toolMsg.Role != "tool" || toolMsg.ToolCallID != "call_1" || toolMsg.Content != "<tool returned no output>" {
		t.Fatalf("tool 消息不正确: %+v", toolMsg)
	}
}

func TestOpenAIStreamErrorFrame(t *testing.T) {
	frames := []string{
		sseFrameText(`{"id":"c","choices":[{"index":0,"delta":{"content":"x"}}]}`),
		sseFrameText(`{"error":{"message":"context_length_exceeded: too many tokens","type":"invalid_request_error","code":"context_length_exceeded"}}`),
	}
	srv, _ := newOpenAITestServer(t, frames...)
	p := NewOpenAI(fastOptions(srv.URL, "k"))
	st, err := p.Stream(context.Background(), Request{Model: "m"})
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
	if se.Kind != ErrKindContextOverflow {
		t.Fatalf("期望 context_overflow，得到 %s (%s)", se.Kind, se.Message)
	}
	if len(events) == 0 {
		t.Fatal("错误前已产出的 delta 应交付")
	}
}

func TestOpenAIReasoningContentMapsToThinking(t *testing.T) {
	frames := []string{
		sseFrameText(`{"id":"c","choices":[{"index":0,"delta":{"reasoning_content":"why"}}]}`),
		sseFrameText(`{"id":"c","choices":[{"index":0,"delta":{"content":"because"}}]}`),
		sseFrameText(`{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`),
		sseFrameText(`[DONE]`),
	}
	srv, _ := newOpenAITestServer(t, frames...)
	p := NewOpenAI(fastOptions(srv.URL, "k"))
	st, err := p.Stream(context.Background(), Request{Model: "m"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer st.Close()
	events, err := recvAll(t, st)
	if err != nil {
		t.Fatalf("recvAll: %v", err)
	}

	var thinkIdx, textIdx int = -1, -1
	for _, ev := range events {
		if ev.Type == EventThinkingDelta {
			thinkIdx = ev.Index
		}
		if ev.Type == EventTextDelta {
			textIdx = ev.Index
		}
	}
	if thinkIdx < 0 || textIdx < 0 || thinkIdx == textIdx {
		t.Fatalf("思考块与文本块必须使用不同索引: think=%d text=%d", thinkIdx, textIdx)
	}
	md, _ := findEvent(events, EventMessageDelta)
	if md.StopReason != StopEndTurn {
		t.Fatalf("finish_reason=stop 应映射为 end_turn，得到 %q", md.StopReason)
	}
}
