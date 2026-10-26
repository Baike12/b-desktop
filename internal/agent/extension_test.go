package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Baike12/b-desktop/internal/llm"
	"github.com/Baike12/b-desktop/internal/permission"
	"github.com/Baike12/b-desktop/internal/tool"
)

// hookFuncs 是可按方法注入的 Hooks 实现。
type hookFuncs struct {
	sessionStart func(context.Context, string, string) error
	userPrompt   func(context.Context, string, string) (string, error)
	preTool      func(context.Context, ToolEvent) (permission.Effect, string, error)
	postTool     func(context.Context, ToolEvent, tool.Result)
}

// SessionStart 实现 Hooks。
func (h hookFuncs) SessionStart(ctx context.Context, sessionID, workDir string) error {
	if h.sessionStart == nil {
		return nil
	}
	return h.sessionStart(ctx, sessionID, workDir)
}

// UserPromptSubmit 实现 Hooks。
func (h hookFuncs) UserPromptSubmit(ctx context.Context, sessionID, prompt string) (string, error) {
	if h.userPrompt == nil {
		return "", nil
	}
	return h.userPrompt(ctx, sessionID, prompt)
}

// PreToolUse 实现 Hooks。
func (h hookFuncs) PreToolUse(ctx context.Context, ev ToolEvent) (permission.Effect, string, error) {
	if h.preTool == nil {
		return "", "", nil
	}
	return h.preTool(ctx, ev)
}

// PostToolUse 实现 Hooks。
func (h hookFuncs) PostToolUse(ctx context.Context, ev ToolEvent, res tool.Result) {
	if h.postTool != nil {
		h.postTool(ctx, ev, res)
	}
}

// contextFunc 是可按函数注入的 ContextProvider。
type contextFunc func(context.Context, string, string, string) string

// ExtraContext 实现 ContextProvider。
func (f contextFunc) ExtraContext(ctx context.Context, sessionID, workDir, prompt string) string {
	return f(ctx, sessionID, workDir, prompt)
}

// recordingSink 收集模型调用指标。
type recordingSink struct {
	mu      sync.Mutex
	metrics []RequestMetric
}

// Record 实现 MetricsSink。
func (s *recordingSink) Record(m RequestMetric) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metrics = append(s.metrics, m)
}

// snapshot 返回指标快照。
func (s *recordingSink) snapshot() []RequestMetric {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RequestMetric, len(s.metrics))
	copy(out, s.metrics)
	return out
}

func TestNewEngineRegistersExtensionTools(t *testing.T) {
	extra := newFakeTool("Extra", true, true)
	h := newHarness(t, harnessOptions{extensions: Extensions{Tools: []tool.Tool{extra}}})

	if _, ok := h.engine.cfg.Tools.Lookup("Extra"); !ok {
		t.Fatalf("extension tool not registered: %v", h.engine.cfg.Tools.Names())
	}

	cfg := h.engine.cfg
	cfg.Extensions.Tools = []tool.Tool{extra}
	if _, err := NewEngine(cfg); err == nil {
		t.Fatal("duplicate extension tool should return error")
	}
}

func TestExtensionPromptAndContext(t *testing.T) {
	var gotPrompt string
	h := newHarness(t, harnessOptions{
		steps: []fakeStep{textStep("m1", "ok")},
		extensions: Extensions{
			Hooks: hookFuncs{
				userPrompt: func(_ context.Context, sessionID, prompt string) (string, error) {
					gotPrompt = prompt
					return "hook-extra", nil
				},
			},
			Context: []ContextProvider{
				contextFunc(func(context.Context, string, string, string) string { return "ctx-extra" }),
				contextFunc(func(context.Context, string, string, string) string { return "  " }),
				contextFunc(func(context.Context, string, string, string) string { return "ctx-second" }),
			},
		},
	})

	rec := newRecorder()
	if _, err := h.engine.Run(context.Background(), Input{TurnID: "t1", Prompt: "hi"}, rec.sink); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotPrompt != "hi" {
		t.Fatalf("UserPromptSubmit prompt = %q, want hi", gotPrompt)
	}
	calls := h.provider.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	system := calls[0].System
	for _, want := range []string{"hook-extra", "ctx-extra", "ctx-second"} {
		if !strings.Contains(system, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, system)
		}
	}
	if strings.Contains(system, "  \n\n") {
		t.Fatalf("empty context should be skipped:\n%s", system)
	}
}

func TestPreToolUseMergeRules(t *testing.T) {
	boom := errors.New("hook failed")
	cases := []struct {
		name     string
		existing permission.Effect
		effect   permission.Effect
		err      error
		panic    bool
		want     permission.Effect
	}{
		{name: "deny overrides allow", existing: permission.EffectAllow, effect: permission.EffectDeny, want: permission.EffectDeny},
		{name: "ask upgrades allow", existing: permission.EffectAllow, effect: permission.EffectAsk, want: permission.EffectAsk},
		{name: "allow keeps ask", existing: permission.EffectAsk, effect: permission.EffectAllow, want: permission.EffectAsk},
		{name: "ask cannot downgrade deny", existing: permission.EffectDeny, effect: permission.EffectAsk, want: permission.EffectDeny},
		{name: "allow keeps allow", existing: permission.EffectAllow, effect: permission.EffectAllow, want: permission.EffectAllow},
		{name: "error keeps existing", existing: permission.EffectAllow, err: boom, want: permission.EffectAllow},
		{name: "panic keeps existing", existing: permission.EffectAllow, panic: true, want: permission.EffectAllow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hooks := hookFuncs{preTool: func(context.Context, ToolEvent) (permission.Effect, string, error) {
				if tc.panic {
					panic("boom")
				}
				return tc.effect, "reason", tc.err
			}}
			engine := &Engine{cfg: Config{SessionID: "s1", Now: fixedNow, Extensions: Extensions{Hooks: hooks}}}
			tr := &turn{engine: engine, ctx: context.Background(), emit: func(Event) {}}

			got := tr.preToolUse(permission.Decision{Effect: tc.existing}, tool.Call{Name: "Read", Input: json.RawMessage(`{}`)}, nil)
			if got.Effect != tc.want {
				t.Fatalf("effect = %s, want %s", got.Effect, tc.want)
			}
		})
	}
}

func TestPostToolUseBeforeHistoryAppend(t *testing.T) {
	read := newFakeTool("Read", true, true)
	var (
		eng        *Engine
		mu         sync.Mutex
		order      []string
		historyLen int
		sawResult  bool
	)
	hooks := hookFuncs{
		preTool: func(context.Context, ToolEvent) (permission.Effect, string, error) {
			mu.Lock()
			order = append(order, "pre")
			mu.Unlock()
			return "", "", nil
		},
		postTool: func(_ context.Context, ev ToolEvent, res tool.Result) {
			mu.Lock()
			order = append(order, "post")
			historyLen = len(eng.History())
			mu.Unlock()
			for _, m := range eng.History() {
				for _, b := range m.Blocks {
					if b.Type == llm.BlockToolResult {
						sawResult = true
					}
				}
			}
			if ev.ToolName != "Read" || res.Content != "Read ok" {
				t.Errorf("post tool event = %+v result = %+v", ev, res)
			}
		},
	}
	h := newHarness(t, harnessOptions{
		tools: []tool.Tool{read},
		steps: []fakeStep{
			toolUseStep("m1", plannedCall{id: "tu1", name: "Read", input: `{}`}),
			textStep("m2", "done"),
		},
		extensions: Extensions{Hooks: hooks},
	})
	eng = h.engine

	rec := newRecorder()
	if _, err := h.engine.Run(context.Background(), Input{TurnID: "t1", Prompt: "go"}, rec.sink); err != nil {
		t.Fatalf("Run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "pre" || order[1] != "post" {
		t.Fatalf("hook order = %v, want [pre post]", order)
	}
	if sawResult {
		t.Fatal("PostToolUse must run before tool_result is appended to history")
	}
	if historyLen != 2 {
		t.Fatalf("history len at PostToolUse = %d, want 2 (user + assistant)", historyLen)
	}
}

func TestMetricsRecordedOnSuccessAndFailure(t *testing.T) {
	sink := &recordingSink{}
	h := newHarness(t, harnessOptions{
		steps:      []fakeStep{textStep("m1", "ok")},
		extensions: Extensions{Metrics: sink},
	})
	if _, err := h.engine.Run(context.Background(), Input{TurnID: "t1", Prompt: "hi"}, newRecorder().sink); err != nil {
		t.Fatalf("Run: %v", err)
	}
	metrics := sink.snapshot()
	if len(metrics) != 1 {
		t.Fatalf("metrics = %d, want 1", len(metrics))
	}
	if metrics[0].StopReason != StopCompleted || metrics[0].Err != "" {
		t.Fatalf("metric = %+v", metrics[0])
	}
	if metrics[0].Usage.InputTokens != 10 || metrics[0].Usage.OutputTokens != 2 {
		t.Fatalf("metric usage = %+v", metrics[0].Usage)
	}
	if metrics[0].Model != "test-model" || metrics[0].SessionID != "s1" {
		t.Fatalf("metric identity = %+v", metrics[0])
	}

	failSink := &recordingSink{}
	fail := newHarness(t, harnessOptions{
		steps:      []fakeStep{{err: errors.New("boom")}},
		extensions: Extensions{Metrics: failSink},
	})
	if _, err := fail.engine.Run(context.Background(), Input{TurnID: "t2", Prompt: "hi"}, newRecorder().sink); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 请求失败不再直接结束本轮：失败摘要回灌后重试，每次 Stream 调用各记一条指标。
	failed := failSink.snapshot()
	if len(failed) == 0 {
		t.Fatal("no metrics recorded")
	}
	errorMetrics := 0
	for _, m := range failed {
		if m.StopReason == StopError && strings.Contains(m.Err, "boom") {
			errorMetrics++
		}
	}
	if errorMetrics == 0 {
		t.Fatalf("no StopError metric with cause: %+v", failed)
	}
	if last := failed[len(failed)-1]; last.StopReason != StopCompleted {
		t.Fatalf("last metric = %+v, want completed after retry", last)
	}
}

// blockingProvider 在 ctx 取消前不返回任何事件，用于验证取消指标。
type blockingProvider struct {
	once    sync.Once
	started chan struct{}
}

// Name 返回 "blocking"。
func (*blockingProvider) Name() string { return "blocking" }

// Stream 返回阻塞流，并通知已进入模型调用。
func (p *blockingProvider) Stream(context.Context, llm.Request) (llm.Stream, error) {
	p.once.Do(func() { close(p.started) })
	return blockingStream{}, nil
}

// blockingStream 一直等待取消。
type blockingStream struct{}

// Recv 阻塞到 ctx 取消。
func (blockingStream) Recv(ctx context.Context) (llm.StreamEvent, error) {
	<-ctx.Done()
	return llm.StreamEvent{}, ctx.Err()
}

// Close 实现 llm.Stream。
func (blockingStream) Close() error { return nil }

func TestMetricsRecordedOnCancel(t *testing.T) {
	sink := &recordingSink{}
	h := newHarness(t, harnessOptions{extensions: Extensions{Metrics: sink}})
	provider := &blockingProvider{started: make(chan struct{})}
	h.engine.cfg.Provider = provider

	rec := newRecorder()
	done := runAsync(t, h, context.Background(), rec, Input{TurnID: "t1", Prompt: "hi"})
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for model call")
	}
	h.engine.Cancel()
	out := awaitRun(t, done, 5*time.Second)
	if out.res.StopReason != StopCanceled {
		t.Fatalf("StopReason = %s, want canceled", out.res.StopReason)
	}
	metrics := sink.snapshot()
	if len(metrics) != 1 || metrics[0].StopReason != StopCanceled {
		t.Fatalf("cancel metrics = %+v", metrics)
	}
}

func TestSessionStartHookErrorDoesNotInterrupt(t *testing.T) {
	h := newHarness(t, harnessOptions{
		steps: []fakeStep{textStep("m1", "ok")},
		extensions: Extensions{Hooks: hookFuncs{
			sessionStart: func(context.Context, string, string) error { return errors.New("hook down") },
		}},
	})
	rec := newRecorder()
	res, err := h.engine.Run(context.Background(), Input{TurnID: "t1", Prompt: "hi"}, rec.sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != StopCompleted {
		t.Fatalf("StopReason = %s, want completed", res.StopReason)
	}
	found := false
	for _, ev := range rec.snapshot() {
		if ev.Type == EventError && ev.Error != nil && ev.Error.Kind == "hook" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing hook error view: %v", eventTypes(rec.snapshot()))
	}
}

// turnObserverFunc 把函数适配成 TurnObserver。
type turnObserverFunc func(TurnInfo)

// TurnComplete 实现 TurnObserver。
func (f turnObserverFunc) TurnComplete(info TurnInfo) { f(info) }

// hooksPlusObserver 是带 TurnObserver 的最小 Hooks 实现，供测试注入。
type hooksPlusObserver struct {
	obs TurnObserver
}

// SessionStart 实现Hooks：空操作。
func (h hooksPlusObserver) SessionStart(context.Context, string, string) error { return nil }

// UserPromptSubmit 实现 Hooks：不追加文本。
func (h hooksPlusObserver) UserPromptSubmit(context.Context, string, string) (string, error) {
	return "", nil
}

// PreToolUse 实现 Hooks：不改变判定。
func (h hooksPlusObserver) PreToolUse(context.Context, ToolEvent) (permission.Effect, string, error) {
	return permission.EffectAllow, "", nil
}

// PostToolUse 实现 Hooks：空操作。
func (h hooksPlusObserver) PostToolUse(context.Context, ToolEvent, tool.Result) {}

// TurnComplete 转发给观察者。
func (h hooksPlusObserver) TurnComplete(info TurnInfo) { h.obs.TurnComplete(info) }

func TestTurnObserverFiresOnlyOnCompleted(t *testing.T) {
	cases := []struct {
		name      string
		reason    StopReason
		wantCalls int
	}{
		{name: "completed", reason: StopCompleted, wantCalls: 1},
		{name: "canceled", reason: StopCanceled, wantCalls: 0},
		{name: "error", reason: StopError, wantCalls: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			calls := 0
			hooks := hooksPlusObserver{obs: turnObserverFunc(func(TurnInfo) {
				mu.Lock()
				calls++
				mu.Unlock()
			})}
			engine := &Engine{cfg: Config{SessionID: "s1", Now: fixedNow, Extensions: Extensions{Hooks: hooks}}}
			tr := &turn{engine: engine, ctx: context.Background(), emit: func(Event) {}}
			tr.end(tc.reason, "")
			tr.finish()
			mu.Lock()
			defer mu.Unlock()
			if calls != tc.wantCalls {
				t.Fatalf("calls = %d, want %d", calls, tc.wantCalls)
			}
		})
	}
}

func TestTurnObserverReceivesTurnSummary(t *testing.T) {
	read := newFakeTool("Read", true, true)
	var mu sync.Mutex
	var got TurnInfo
	h := newHarness(t, harnessOptions{
		steps: []fakeStep{
			toolUseStep("m1", plannedCall{id: "t1", name: "Read", input: `{}`}),
			textStep("m3", "all finished"),
		},
		tools: []tool.Tool{read},
		extensions: Extensions{
			Hooks: hooksPlusObserver{obs: turnObserverFunc(func(info TurnInfo) {
				mu.Lock()
				got = info
				mu.Unlock()
			})},
		},
	})
	if _, err := h.engine.Run(context.Background(), Input{TurnID: "t1", Prompt: "read a.txt"}, newRecorder().sink); err != nil {
		t.Fatalf("Run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got.Prompt != "read a.txt" {
		t.Fatalf("prompt = %q", got.Prompt)
	}
	if got.FinalText != "all finished" {
		t.Fatalf("finalText = %q", got.FinalText)
	}
	if len(got.ToolNames) != 1 || got.ToolNames[0] != "Read" {
		t.Fatalf("toolNames = %v", got.ToolNames)
	}
	if got.SessionID == "" || got.TurnID == "" {
		t.Fatalf("ids = %+v", got)
	}
	if got.StopReason != StopCompleted {
		t.Fatalf("stopReason = %s", got.StopReason)
	}
}
