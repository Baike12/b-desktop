package llm

import (
	"encoding/json"
	"testing"
)

// TestNormalizeThinking 验证扩展思考预算按协议约束归一化。
func TestNormalizeThinking(t *testing.T) {
	cases := []struct {
		name    string
		enabled bool
		budget  int
		max     int
		want    *Thinking
	}{
		{
			name:    "未启用返回 nil",
			enabled: false,
			budget:  4096,
			max:     8192,
			want:    nil,
		},
		{
			name:    "预算为零取默认值",
			enabled: true,
			budget:  0,
			max:     32768,
			want:    &Thinking{Enabled: true, BudgetTokens: DefaultThinkingBudget},
		},
		{
			name:    "预算低于下限时抬到下限",
			enabled: true,
			budget:  10,
			max:     32768,
			want:    &Thinking{Enabled: true, BudgetTokens: MinThinkingBudget},
		},
		{
			name:    "预算不小于 max_tokens 时放弃扩展思考",
			enabled: true,
			budget:  8192,
			max:     8192,
			want:    nil,
		},
		{
			name:    "预算严格小于 max_tokens 时保留",
			enabled: true,
			budget:  4096,
			max:     8192,
			want:    &Thinking{Enabled: true, BudgetTokens: 4096},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeThinking(tc.enabled, tc.budget, tc.max)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("NormalizeThinking = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("NormalizeThinking = nil, want %+v", tc.want)
			}
			if *got != *tc.want {
				t.Fatalf("NormalizeThinking = %+v, want %+v", *got, *tc.want)
			}
		})
	}
}

// TestAnthropicRequestEncodesThinking 验证 Anthropic 请求体带上 thinking 字段。
func TestAnthropicRequestEncodesThinking(t *testing.T) {
	req := Request{
		Model:     "claude",
		MaxTokens: 16384,
		Messages: []Message{{
			Role:   RoleUser,
			Blocks: []ContentBlock{{Type: BlockText, Text: "hi"}},
		}},
		Thinking: &Thinking{Enabled: true, BudgetTokens: 4096},
	}
	payload, err := encodeAnthropicRequest(req)
	if err != nil {
		t.Fatalf("encodeAnthropicRequest: %v", err)
	}
	var got struct {
		Thinking *struct {
			Type         string `json:"type"`
			BudgetTokens int    `json:"budget_tokens"`
		} `json:"thinking"`
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Thinking == nil {
		t.Fatalf("请求体缺少 thinking 字段: %s", payload)
	}
	if got.Thinking.Type != "enabled" || got.Thinking.BudgetTokens != 4096 {
		t.Fatalf("thinking = %+v, want enabled/4096", *got.Thinking)
	}
}
