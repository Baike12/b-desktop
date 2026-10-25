package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// TestThinkingConfigFromEnv 验证扩展思考配置可通过环境变量开启。
func TestThinkingConfigFromEnv(t *testing.T) {
	env := map[string]string{
		"BDESKTOP_CONFIG":           filepath.Join(t.TempDir(), "missing.json"),
		"BDESKTOP_THINKING_ENABLED": "1",
		"BDESKTOP_THINKING_BUDGET":  "4096",
	}
	cfg, err := LoadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.ThinkingEnabled {
		t.Fatal("扩展思考应被环境变量开启")
	}
	if cfg.ThinkingBudget != 4096 {
		t.Fatalf("thinking budget = %d, want 4096", cfg.ThinkingBudget)
	}
}

// TestThinkingConfigDisabledByEnv 验证 BDESKTOP_THINKING_ENABLED=0 可关闭扩展思考。
func TestThinkingConfigDisabledByEnv(t *testing.T) {
	env := map[string]string{
		"BDESKTOP_CONFIG":           filepath.Join(t.TempDir(), "missing.json"),
		"BDESKTOP_THINKING_ENABLED": "0",
	}
	cfg, err := LoadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.ThinkingEnabled {
		t.Fatal("BDESKTOP_THINKING_ENABLED=0 应关闭扩展思考")
	}
}

// TestThinkingBudgetDefaults 验证未配置预算时取默认值。
func TestThinkingBudgetDefaults(t *testing.T) {
	env := map[string]string{"BDESKTOP_CONFIG": filepath.Join(t.TempDir(), "missing.json")}
	cfg, err := LoadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.ThinkingBudget != 8192 {
		t.Fatalf("thinking budget = %d, want 8192", cfg.ThinkingBudget)
	}
}

// TestThinkingBudgetInvalid 验证负数预算被拒绝。
func TestThinkingBudgetInvalid(t *testing.T) {
	env := map[string]string{
		"BDESKTOP_CONFIG":          filepath.Join(t.TempDir(), "missing.json"),
		"BDESKTOP_THINKING_BUDGET": "-1",
	}
	if _, err := LoadConfig(func(key string) string { return env[key] }); !errors.Is(err, ErrInvalidProviderConfig) {
		t.Fatalf("invalid thinking budget error = %v", err)
	}
}

// TestThinkingDisabledWhenBudgetExceedsMaxTokens 验证预算不小于 max_tokens 时关闭扩展思考。
func TestThinkingDisabledWhenBudgetExceedsMaxTokens(t *testing.T) {
	env := map[string]string{
		"BDESKTOP_CONFIG":            filepath.Join(t.TempDir(), "missing.json"),
		"BDESKTOP_THINKING_ENABLED":  "1",
		"BDESKTOP_THINKING_BUDGET":   "8192",
		"BDESKTOP_MAX_OUTPUT_TOKENS": "8192",
	}
	cfg, err := LoadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.ThinkingEnabled {
		t.Fatal("预算不小于 max_output_tokens 时应关闭扩展思考")
	}
}

// TestSetProviderConfigThinking 验证设置面板能保存扩展思考配置。
func TestSetProviderConfigThinking(t *testing.T) {
	app, err := NewWithDeps(Config{
		Provider:        "anthropic",
		Model:           "m",
		ContextWindow:   200000,
		MaxOutputTokens: 16384,
	}, func(context.Context, string, any) {}, nil, nil)
	if err != nil {
		t.Fatalf("NewWithDeps: %v", err)
	}
	if err := app.SetProviderConfig(ProviderConfigInput{
		Provider:        "anthropic",
		Model:           "m",
		MaxOutputTokens: 16384,
		ThinkingEnabled: true,
		ThinkingBudget:  4096,
	}); err != nil {
		t.Fatalf("SetProviderConfig: %v", err)
	}
	view := app.GetProviderConfig()
	if !view.ThinkingEnabled || view.ThinkingBudget != 4096 {
		t.Fatalf("thinking config = %v/%d, want true/4096", view.ThinkingEnabled, view.ThinkingBudget)
	}
}
