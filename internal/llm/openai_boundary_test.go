package llm

import (
	"context"
	"testing"
)

// TestOpenAITextAfterToolGetsOwnBlock 验证工具调用之后到达的正文成为独立块。
//
// 兼容端点会把 Anthropic 的「文本 → 工具 → 文本」摊平成 Chat Completions 的
// delta 序列。若正文块在工具开始时不关闭，工具之后的正文会沿用同一索引并被
// 并入工具之前那段，工具调用之间的模型输出就此丢失。
func TestOpenAITextAfterToolGetsOwnBlock(t *testing.T) {
	frames := []string{
		sseFrameText(`{"id":"c","choices":[{"index":0,"delta":{"role":"assistant","content":"先看文件"}}]}`),
		sseFrameText(`{"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"t1","type":"function","function":{"name":"Read","arguments":"{\"path\":\"a\"}"}}]}}]}`),
		sseFrameText(`{"id":"c","choices":[{"index":0,"delta":{"content":"工具之后的补充说明"}}]}`),
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

	// 按到达顺序收集文本增量及其块索引。
	type textPiece struct {
		index int
		text  string
	}
	var pieces []textPiece
	var toolIndex = -1
	for _, ev := range events {
		switch ev.Type {
		case EventTextDelta:
			pieces = append(pieces, textPiece{index: ev.Index, text: ev.Text})
		case EventToolUseStart:
			toolIndex = ev.Index
		}
	}
	if len(pieces) != 2 {
		t.Fatalf("文本增量数 = %d, want 2", len(pieces))
	}
	if pieces[0].text != "先看文件" || pieces[1].text != "工具之后的补充说明" {
		t.Fatalf("文本内容 = %q / %q", pieces[0].text, pieces[1].text)
	}
	if pieces[0].index == pieces[1].index {
		t.Fatalf("工具之后的正文必须使用新的块索引，两段都是 %d", pieces[0].index)
	}
	if toolIndex == pieces[0].index || toolIndex == pieces[1].index {
		t.Fatalf("工具块索引 %d 与文本块索引冲突", toolIndex)
	}
}

// TestOpenAIReasoningAfterToolGetsOwnBlock 验证工具之后的思考同样成为独立块。
func TestOpenAIReasoningAfterToolGetsOwnBlock(t *testing.T) {
	frames := []string{
		sseFrameText(`{"id":"c","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"先想想"}}]}`),
		sseFrameText(`{"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"t1","type":"function","function":{"name":"Read","arguments":"{}"}}]}}]}`),
		sseFrameText(`{"id":"c","choices":[{"index":0,"delta":{"reasoning_content":"再想想"}}]}`),
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

	var indexes []int
	for _, ev := range events {
		if ev.Type == EventThinkingDelta {
			indexes = append(indexes, ev.Index)
		}
	}
	if len(indexes) != 2 {
		t.Fatalf("思考增量数 = %d, want 2", len(indexes))
	}
	if indexes[0] == indexes[1] {
		t.Fatalf("工具之后的思考必须使用新的块索引，两段都是 %d", indexes[0])
	}
}
