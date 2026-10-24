package agent

import (
	"context"
	"testing"

	"github.com/Baike12/b-desktop/internal/llm"
	"github.com/Baike12/b-desktop/internal/tool"
)

// TestRunRequestsExtendedThinking 验证开启后请求里带上扩展思考配置。
func TestRunRequestsExtendedThinking(t *testing.T) {
	h := newHarness(t, harnessOptions{
		steps: []fakeStep{textStep("m1", "done")},
	})
	h.engine.cfg.ThinkingEnabled = true
	h.engine.cfg.ThinkingBudget = 4096

	rec := newRecorder()
	if _, err := h.engine.Run(context.Background(), Input{TurnID: "t1", Prompt: "hi"}, rec.sink); err != nil {
		t.Fatalf("Run: %v", err)
	}
	calls := h.provider.Calls()
	if len(calls) == 0 {
		t.Fatal("provider 未收到请求")
	}
	req := calls[0]
	if req.Thinking == nil || !req.Thinking.Enabled {
		t.Fatalf("请求缺少扩展思考配置: %+v", req.Thinking)
	}
	if req.Thinking.BudgetTokens != 4096 {
		t.Fatalf("budget = %d, want 4096", req.Thinking.BudgetTokens)
	}
}

// TestRunOmitsThinkingWhenDisabled 验证默认不请求扩展思考。
func TestRunOmitsThinkingWhenDisabled(t *testing.T) {
	h := newHarness(t, harnessOptions{
		steps: []fakeStep{textStep("m1", "done")},
	})
	rec := newRecorder()
	if _, err := h.engine.Run(context.Background(), Input{TurnID: "t1", Prompt: "hi"}, rec.sink); err != nil {
		t.Fatalf("Run: %v", err)
	}
	calls := h.provider.Calls()
	if len(calls) == 0 {
		t.Fatal("provider 未收到请求")
	}
	if calls[0].Thinking != nil {
		t.Fatalf("未开启扩展思考时请求不应带 Thinking: %+v", calls[0].Thinking)
	}
}

// TestRunDropsThinkingWhenBudgetTooLarge 验证预算不小于 max_tokens 时不启用扩展思考，
// 而不是让整个请求失败。
func TestRunDropsThinkingWhenBudgetTooLarge(t *testing.T) {
	h := newHarness(t, harnessOptions{
		steps: []fakeStep{textStep("m1", "done")},
	})
	h.engine.cfg.ThinkingEnabled = true
	// 预算等于 max_output_tokens，违反 Anthropic 的 budget_tokens < max_tokens。
	h.engine.cfg.ThinkingBudget = h.engine.cfg.Compactor.Budget().MaxOutputTokens

	rec := newRecorder()
	if _, err := h.engine.Run(context.Background(), Input{TurnID: "t1", Prompt: "hi"}, rec.sink); err != nil {
		t.Fatalf("Run: %v", err)
	}
	calls := h.provider.Calls()
	if len(calls) == 0 {
		t.Fatal("provider 未收到请求")
	}
	if calls[0].Thinking != nil {
		t.Fatalf("预算非法时不应启用扩展思考: %+v", calls[0].Thinking)
	}
}

// TestRunKeepsTextAfterToolInSeparateBlock 验证工具调用之后又出现的正文独立成块，
// 不与工具之前那段合并。
func TestRunKeepsTextAfterToolInSeparateBlock(t *testing.T) {
	step := fakeStep{events: []llm.StreamEvent{
		{Type: llm.EventMessageStart, MessageID: "m1"},
		{Type: llm.EventBlockStart, Index: 0, Kind: llm.BlockText},
		{Type: llm.EventTextDelta, Index: 0, Kind: llm.BlockText, Text: "工具之前的说明"},
		{Type: llm.EventToolUseStart, Index: 1, Kind: llm.BlockToolUse, ToolUseID: "t1", ToolName: "Read"},
		{Type: llm.EventToolInputDelta, Index: 1, Kind: llm.BlockToolUse, ToolUseID: "t1", PartialJSON: `{"path":"a"}`},
		{Type: llm.EventToolUseStop, Index: 1, Kind: llm.BlockToolUse},
		{Type: llm.EventBlockStart, Index: 2, Kind: llm.BlockText},
		{Type: llm.EventTextDelta, Index: 2, Kind: llm.BlockText, Text: "工具之后的说明"},
		{Type: llm.EventMessageDelta, StopReason: llm.StopToolUse, Usage: &llm.Usage{InputTokens: 5, OutputTokens: 2}},
		{Type: llm.EventMessageStop},
	}}
	h := newHarness(t, harnessOptions{
		tools: []tool.Tool{newFakeTool("Read", true, true)},
		steps: []fakeStep{step, textStep("m2", "ok")},
	})
	rec := newRecorder()
	if _, err := h.engine.Run(context.Background(), Input{TurnID: "t1", Prompt: "hi"}, rec.sink); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var texts []string
	var kinds []llm.BlockType
	for _, m := range h.engine.History() {
		if m.Role != llm.RoleAssistant || m.ID != "m1" {
			continue
		}
		for _, b := range m.Blocks {
			kinds = append(kinds, b.Type)
			if b.Type == llm.BlockText {
				texts = append(texts, b.Text)
			}
		}
	}
	if len(texts) != 2 {
		t.Fatalf("文本块数 = %d, want 2: %v", len(texts), texts)
	}
	if texts[0] != "工具之前的说明" || texts[1] != "工具之后的说明" {
		t.Fatalf("文本块内容 = %v, want 两段独立文本", texts)
	}
	if len(kinds) != 3 || kinds[0] != llm.BlockText || kinds[1] != llm.BlockToolUse || kinds[2] != llm.BlockText {
		t.Fatalf("块顺序 = %v, want text/tool_use/text", kinds)
	}
}
