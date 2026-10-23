package llm

import "testing"

// TestCurrentPreset 校验设置页只生成当前这一条配置。
func TestCurrentPreset(t *testing.T) {
	p := CurrentPreset("anthropic", "https://api.example.com", "deepseek-flash")
	if p.ID != "current" {
		t.Fatalf("ID = %q, want current", p.ID)
	}
	if p.Name != "deepseek-flash" || p.DefaultModel != "deepseek-flash" {
		t.Fatalf("model fields = %+v", p)
	}
	if p.Provider != "anthropic" || p.BaseURL != "https://api.example.com" {
		t.Fatalf("provider fields = %+v", p)
	}
	if !p.NeedsKey || p.Local {
		t.Fatalf("key flags = %+v", p)
	}
}

func TestCurrentPresetEmptyModel(t *testing.T) {
	p := CurrentPreset(" ", " ", "")
	if p.Name != "当前模型（未配置）" {
		t.Fatalf("Name = %q", p.Name)
	}
	if p.DefaultModel != "" || p.Provider != "" || p.BaseURL != "" {
		t.Fatalf("empty config not preserved: %+v", p)
	}
}
