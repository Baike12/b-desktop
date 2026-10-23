package context

import (
	"encoding/json"
	"testing"

	"github.com/Baike12/b-desktop/internal/llm"
)

func TestEstimateString(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"a", 1},
		{"abcd", 1},
		{"abcde", 2},
		{"abcdefgh", 2},
		{"中", 1},
		{"中文", 2},
		{"ab中", 2}, // ASCII 2 字符 → 1 token，非 ASCII 1 token
	}
	for _, c := range cases {
		if got := EstimateString(c.in); got != c.want {
			t.Errorf("EstimateString(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestEstimateStringMatchesEstimatorInterface(t *testing.T) {
	var est Estimator = DefaultEstimator{}
	if got, want := est.EstimateString("abcdef"), EstimateString("abcdef"); got != want {
		t.Fatalf("estimator = %d, want %d", got, want)
	}
}

func TestEstimateMessagesBlockOverhead(t *testing.T) {
	text := llm.Message{Role: llm.RoleUser, Blocks: []llm.ContentBlock{{Type: llm.BlockText, Text: "abcd"}}}
	if got, want := EstimateMessages([]llm.Message{text}), 4+6+1; got != want {
		t.Fatalf("单个文本消息 = %d, want %d", got, want)
	}

	input := json.RawMessage(`{"file_path":"a"}`)
	toolUse := llm.ContentBlock{Type: llm.BlockToolUse, ToolUseID: "t1", ToolName: "Read", Input: input}
	msg := llm.Message{Role: llm.RoleAssistant, Blocks: []llm.ContentBlock{
		{Type: llm.BlockText, Text: "abcd"},
		toolUse,
	}}
	want := 4 + (6 + EstimateString("abcd")) + (6 + EstimateString("Read"+string(input)))
	if got := EstimateMessages([]llm.Message{msg}); got != want {
		t.Fatalf("工具调用消息 = %d, want %d", got, want)
	}
}

func TestEstimateMessagesEmpty(t *testing.T) {
	if got := EstimateMessages(nil); got != 0 {
		t.Fatalf("空消息 = %d, want 0", got)
	}
}
