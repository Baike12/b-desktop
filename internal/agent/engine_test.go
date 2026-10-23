package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	sysctx "github.com/Baike12/b-desktop/internal/context"
	"github.com/Baike12/b-desktop/internal/llm"
	"github.com/Baike12/b-desktop/internal/permission"
	"github.com/Baike12/b-desktop/internal/session"
	"github.com/Baike12/b-desktop/internal/tool"
)

func TestThinkingDeltaAndSignatureForwarded(t *testing.T) {
	step := fakeStep{events: []llm.StreamEvent{
		{Type: llm.EventMessageStart, MessageID: "m1"},
		{Type: llm.EventBlockStart, Index: 0, Kind: llm.BlockThinking},
		{Type: llm.EventThinkingDelta, Index: 0, Kind: llm.BlockThinking, Thinking: "think"},
		{Type: llm.EventSignature, Index: 0, Kind: llm.BlockThinking, Signature: "sig"},
		{Type: llm.EventMessageStop},
	}}
	h := newHarness(t, harnessOptions{steps: []fakeStep{step}})
	rec := newRecorder()

	res, err := h.engine.Run(context.Background(), Input{TurnID: "t1", Prompt: "q"}, rec.sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != StopCompleted {
		t.Fatalf("StopReason = %s", res.StopReason)
	}
	if got := rec.count(EventThinkingDelta); got != 1 {
		t.Fatalf("thinking_delta count = %d, want 1", got)
	}
	ev := rec.snapshot()[indexOf(rec.snapshot(), EventThinkingDelta)]
	if ev.Text != "think" {
		t.Fatalf("thinking event text = %q", ev.Text)
	}

	hist := h.engine.History()
	var thinking *llm.ContentBlock
	for _, m := range hist {
		for i := range m.Blocks {
			if m.Blocks[i].Type == llm.BlockThinking {
				thinking = &m.Blocks[i]
			}
		}
	}
	if thinking == nil || thinking.Thinking != "think" || thinking.Signature != "sig" {
		t.Fatalf("thinking block = %+v", thinking)
	}
}

func TestCompactionWritesSessionBoundary(t *testing.T) {
	h := newHarness(t, harnessOptions{
		budget: sysctx.Budget{ContextWindow: 32000, MaxOutputTokens: 8192},
		steps: []fakeStep{
			textStep("m1", "a1"),
			textStep("m2", "a2"),
			textStep("m3", "done"),
		},
	})
	ctx := context.Background()
	rec := newRecorder()
	if _, err := h.engine.Run(ctx, Input{TurnID: "t1", Prompt: "first"}, rec.sink); err != nil {
		t.Fatalf("turn1: %v", err)
	}
	if _, err := h.engine.Run(ctx, Input{TurnID: "t2", Prompt: "second"}, rec.sink); err != nil {
		t.Fatalf("turn2: %v", err)
	}

	big := strings.Repeat("a", 60000) // 约 15000 token，超过 auto 12288 但低于 blocking 21576
	res, err := h.engine.Run(ctx, Input{TurnID: "t3", Prompt: big}, rec.sink)
	if err != nil {
		t.Fatalf("turn3: %v", err)
	}
	if res.StopReason != StopCompleted {
		t.Fatalf("StopReason = %s, err = %s", res.StopReason, res.Err)
	}
	if h.provider.CallCount() != 3 {
		t.Fatalf("model calls = %d, want 3", h.provider.CallCount())
	}

	recs := loadRecords(t, h.store, "s1")
	boundaries := 0
	var boundary session.Compact
	for _, r := range recs {
		if r.Type == session.RecordCompact && r.Compact != nil {
			boundaries++
			boundary = *r.Compact
		}
	}
	if boundaries != 1 {
		t.Fatalf("compact records = %d, want 1", boundaries)
	}
	if boundary.Summary != "compacted summary" {
		t.Fatalf("boundary summary = %q", boundary.Summary)
	}
	if boundary.Trigger != "auto" || boundary.PreTokens <= 0 || boundary.PostTokens <= 0 {
		t.Fatalf("boundary = %+v", boundary)
	}

	loaded, err := session.Replay(recs)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(loaded.Messages) == 0 || len(loaded.Messages[0].Blocks) == 0 {
		t.Fatalf("replayed messages = %+v", loaded.Messages)
	}
	if got := loaded.Messages[0].Blocks[0].Text; got != "compacted summary" {
		t.Fatalf("first replayed message = %q, want summary", got)
	}
	if loaded.Boundary == nil || loaded.Boundary.Summary != "compacted summary" {
		t.Fatalf("replayed boundary = %+v", loaded.Boundary)
	}

	if got := rec.count(EventCompactStart); got != 1 {
		t.Fatalf("compact_start count = %d, want 1", got)
	}
	if got := rec.count(EventCompactEnd); got != 1 {
		t.Fatalf("compact_end count = %d, want 1", got)
	}
	for _, ev := range rec.snapshot() {
		if ev.Type == EventCompactEnd {
			if ev.Compact == nil || ev.Compact.Trigger != "auto" || ev.Compact.Failed {
				t.Fatalf("compact_end = %+v", ev.Compact)
			}
		}
	}
}

func TestBlockingLimitEndsTurn(t *testing.T) {
	h := newHarness(t, harnessOptions{
		budget: sysctx.Budget{ContextWindow: 32000, MaxOutputTokens: 8192},
		steps: []fakeStep{
			textStep("m1", "a1"),
			textStep("m2", "a2"),
			textStep("m3", "unused"),
		},
	})
	ctx := context.Background()
	if _, err := h.engine.Run(ctx, Input{TurnID: "t1", Prompt: "first"}, func(Event) {}); err != nil {
		t.Fatalf("turn1: %v", err)
	}
	if _, err := h.engine.Run(ctx, Input{TurnID: "t2", Prompt: "second"}, func(Event) {}); err != nil {
		t.Fatalf("turn2: %v", err)
	}

	rec := newRecorder()
	big := strings.Repeat("a", 100000) // 约 25000 token，超过 blocking 21576
	res, err := h.engine.Run(ctx, Input{TurnID: "t3", Prompt: big}, rec.sink)
	if err != nil {
		t.Fatalf("turn3: %v", err)
	}
	if res.StopReason != StopBlockingLimit {
		t.Fatalf("StopReason = %s, want blocking_limit", res.StopReason)
	}
	if h.provider.CallCount() != 2 {
		t.Fatalf("model calls = %d, want 2 (blocked before third)", h.provider.CallCount())
	}
	events := rec.snapshot()
	if last := events[len(events)-1]; last.Type != EventTurnEnd || last.StopReason != StopBlockingLimit {
		t.Fatalf("last event = %+v", last)
	}
	if indexOf(events, EventError) < 0 {
		t.Fatalf("missing error event: %v", eventTypes(events))
	}
}

func TestRestoreLoadsPersistedHistory(t *testing.T) {
	h := newHarness(t, harnessOptions{steps: []fakeStep{textStep("m1", "hello")}})
	if _, err := h.engine.Run(context.Background(), Input{TurnID: "t1", Prompt: "hi"}, func(Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	cfg := h.engine.cfg
	eng2, err := NewEngine(cfg)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if err := eng2.Restore(context.Background()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	hist := eng2.History()
	if len(hist) != 2 || hist[0].Role != llm.RoleUser || hist[1].Role != llm.RoleAssistant {
		t.Fatalf("restored history = %+v", hist)
	}
	if got := textOf(hist[1]); got != "hello" {
		t.Fatalf("restored assistant text = %q", got)
	}
}

func TestRunRejectsConcurrentTurn(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{})
	settled := make(chan struct{})
	var once sync.Once
	slow := newFakeTool("Slow", true, false)
	slow.runFn = func(context.Context, json.RawMessage) (tool.Result, error) {
		once.Do(func() { close(started) })
		<-block
		close(settled)
		return tool.Result{Content: "ok"}, nil
	}
	h := newHarness(t, harnessOptions{
		tools: []tool.Tool{slow},
		steps: []fakeStep{
			toolUseStep("m1", plannedCall{id: "t1", name: "Slow", input: `{}`}),
			textStep("m2", "done"),
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := runAsync(t, h, ctx, newRecorder(), Input{TurnID: "t1", Prompt: "x"})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first turn did not start the tool")
	}
	_, err := h.engine.Run(ctx, Input{TurnID: "t2", Prompt: "y"}, func(Event) {})
	if !errors.Is(err, ErrTurnRunning) {
		t.Fatalf("second Run error = %v, want ErrTurnRunning", err)
	}
	close(block)
	<-settled
	out := awaitRun(t, first, 3*time.Second)
	if out.res.StopReason != StopCompleted {
		t.Fatalf("first StopReason = %s", out.res.StopReason)
	}
}

func TestWireBuildsDefaultComponents(t *testing.T) {
	dir := t.TempDir()
	provider := newFakeProvider()
	comps, err := Wire(WireOptions{
		WorkDir:         dir,
		Model:           "test-model",
		Provider:        provider,
		PermissionMode:  permission.ModeDefault,
		ContextWindow:   128000,
		MaxOutputTokens: 4096,
		Now:             fixedNow,
		ReadFile:        func(string) ([]byte, error) { return nil, os.ErrNotExist },
		RunGit:          func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("no git") },
	})
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}

	want := []string{"Read", "Write", "Edit", "Glob", "Grep", "Bash", "TodoWrite"}
	got := comps.Tools.Names()
	if len(got) != len(want) {
		t.Fatalf("tool names = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tool names = %v, want %v", got, want)
		}
	}
	if comps.Permissions.Mode() != permission.ModeDefault {
		t.Fatalf("permission mode = %s", comps.Permissions.Mode())
	}
	system, err := comps.Builder.System(context.Background(), comps.Tools.Specs())
	if err != nil {
		t.Fatalf("System: %v", err)
	}
	if !strings.Contains(system, "<tools>") || !strings.Contains(system, "Read") {
		t.Fatalf("system prompt missing tool section:\n%s", system)
	}
	if got := comps.Compactor.Budget().ContextWindow; got != 128000 {
		t.Fatalf("budget window = %d, want 128000", got)
	}
}
