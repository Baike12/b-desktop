package app

import (
	"context"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Baike12/b-desktop/internal/agent"
	"github.com/Baike12/b-desktop/internal/llm"
	"github.com/Baike12/b-desktop/internal/permission"
	"github.com/Baike12/b-desktop/internal/session"
)

// captureProvider 记录最后一次模型请求，并按脚本返回一段文本。
type captureProvider struct {
	mu   sync.Mutex
	reqs []llm.Request
}

// Name 实现 llm.Provider。
func (p *captureProvider) Name() string { return "capture" }

// Stream 实现 llm.Provider。
func (p *captureProvider) Stream(_ context.Context, req llm.Request) (llm.Stream, error) {
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	p.mu.Unlock()
	return &textStream{}, nil
}

// lastRequest 返回最近一次请求。
func (p *captureProvider) lastRequest() (llm.Request, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.reqs) == 0 {
		return llm.Request{}, false
	}
	return p.reqs[len(p.reqs)-1], true
}

// textStream 返回一条完整 assistant 消息。
type textStream struct {
	done bool
}

// Recv 实现 llm.Stream。
func (s *textStream) Recv(context.Context) (llm.StreamEvent, error) {
	if s.done {
		return llm.StreamEvent{}, io.EOF
	}
	s.done = true
	return llm.StreamEvent{Type: llm.EventTextDelta, Text: "ok"}, nil
}

// Close 实现 llm.Stream。
func (s *textStream) Close() error { return nil }

// extensionsTestConfig 返回隔离配置：临时 HOME 下的扩展目录 + 临时工作区。
func extensionsTestConfig(t *testing.T) Config {
	t.Helper()
	dir := t.TempDir()
	return Config{
		Provider:           "anthropic",
		APIKey:             "sk-test-1234",
		Model:              "test-model",
		PermissionMode:     string(permission.ModeBypassPermissions),
		WorkDir:            dir,
		DataDir:            filepath.Join(dir, "data"),
		ContextWindow:      200000,
		MaxOutputTokens:    4096,
		MaxTurns:           10,
		MaxToolConcurrency: 4,
	}
}

// TestBuildExtensionsWiresComponents 验证装配结果：工具、上下文、钩子、指标与子 Agent
// 都来自组件，且扩展工具真正进入模型请求。
func TestBuildExtensionsWiresComponents(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	provider := &captureProvider{}
	cfg := extensionsTestConfig(t)
	collector := newEventCollector()
	app, err := NewWithDeps(cfg, collector.send, session.NewMemStore(), func(Config) (llm.Provider, error) {
		return provider, nil
	})
	if err != nil {
		t.Fatalf("NewWithDeps: %v", err)
	}

	ctx := context.Background()
	app.Startup(ctx)
	defer app.Shutdown(ctx)

	ext, err := app.buildExtensions(cfg.WorkDir, nil, cfg, 0)
	if err != nil {
		t.Fatalf("buildExtensions: %v", err)
	}
	if ext.Hooks == nil {
		t.Fatal("Hooks 未装配")
	}
	if ext.Metrics == nil {
		t.Fatal("Metrics 未装配")
	}
	if ext.Subagent == nil {
		t.Fatal("Subagent 未装配")
	}
	if len(ext.Context) != 2 {
		t.Fatalf("Context 数量 = %d, want 2（skills + memory）", len(ext.Context))
	}

	names := make(map[string]bool, len(ext.Tools))
	for _, tl := range ext.Tools {
		names[tl.Name()] = true
	}
	for _, want := range []string{"Skill", "MemorySave", "Agent"} {
		if !names[want] {
			t.Fatalf("扩展工具缺少 %s，实际 %v", want, names)
		}
	}

	info, err := app.CreateSession(CreateSessionInput{Title: "扩展"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := app.SendMessage(info.ID, "hello", nil); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	collector.waitFor(t, agent.EventTurnEnd, 3*time.Second)

	req, ok := provider.lastRequest()
	if !ok {
		t.Fatal("没有捕获到模型请求")
	}
	sent := make(map[string]bool, len(req.Tools))
	for _, spec := range req.Tools {
		sent[spec.Name] = true
	}
	for _, want := range []string{"Skill", "MemorySave", "Agent"} {
		if !sent[want] {
			t.Fatalf("模型请求缺少扩展工具 %s，实际 %v", want, sent)
		}
	}
}

// TestStartupAndShutdownClosesExtensions 验证 Shutdown 释放全部扩展组件
// （extData 在关闭时摘空），且重复调用保持幂等。
func TestStartupAndShutdownClosesExtensions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	cfg := extensionsTestConfig(t)
	app, err := NewWithDeps(cfg, func(context.Context, string, any) {}, session.NewMemStore(), nil)
	if err != nil {
		t.Fatalf("NewWithDeps: %v", err)
	}
	ctx := context.Background()
	app.Startup(ctx)

	if _, err := app.buildExtensions(cfg.WorkDir, nil, cfg, 0); err != nil {
		t.Fatalf("buildExtensions: %v", err)
	}

	app.Shutdown(ctx)

	app.extMu.Lock()
	left := len(app.extData)
	app.extMu.Unlock()
	if left != 0 {
		t.Fatalf("Shutdown 后仍有 %d 个扩展组件未释放", left)
	}

	// 幂等：重复 Shutdown 不应阻塞或 panic。
	app.Shutdown(ctx)
}

// TestBuildExtensionsSkipsBrokenComponent 验证单个组件初始化失败不会阻断装配：
// 记忆组件类型损坏时被跳过，其余组件照常注入。
func TestBuildExtensionsSkipsBrokenComponent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	cfg := extensionsTestConfig(t)
	app, err := NewWithDeps(cfg, func(context.Context, string, any) {}, session.NewMemStore(), nil)
	if err != nil {
		t.Fatalf("NewWithDeps: %v", err)
	}
	// 预置错误类型，让 memoryComponent 的类型断言失败。
	app.extData = map[string]any{"memory": "not-a-memory-store"}

	ext, err := app.buildExtensions(cfg.WorkDir, nil, cfg, 0)
	if err != nil {
		t.Fatalf("buildExtensions: %v", err)
	}
	if len(ext.Context) != 1 {
		t.Fatalf("Context 数量 = %d, want 1（只剩 skills）", len(ext.Context))
	}
	if ext.Metrics == nil || ext.Subagent == nil || ext.Hooks == nil {
		t.Fatal("其余组件应照常装配")
	}
	for _, tl := range ext.Tools {
		if tl.Name() == "MemorySave" {
			t.Fatal("损坏的记忆组件不应提供工具")
		}
	}
}
