package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	sysctx "github.com/Baike12/b-desktop/internal/context"
	"github.com/Baike12/b-desktop/internal/llm"
	"github.com/Baike12/b-desktop/internal/permission"
	"github.com/Baike12/b-desktop/internal/session"
	"github.com/Baike12/b-desktop/internal/tool"
)

// withUsage 覆盖脚本化响应上报的 usage，用于构造大用量场景。
func withUsage(step fakeStep, u llm.Usage) fakeStep {
	for i := range step.events {
		if step.events[i].Type == llm.EventMessageDelta {
			usage := u
			step.events[i].Usage = &usage
		}
	}
	return step
}

// TestUsageAcrossTurnsDoesNotAccumulate 验证跨轮 usage 不累加：
// 每轮只上报 4000 token，8 轮后累计已远超 auto/blocking 线，但单轮上下文很小，
// 任何一轮都不应因累计值被判定为阻塞。
func TestUsageAcrossTurnsDoesNotAccumulate(t *testing.T) {
	const turns = 8
	steps := make([]fakeStep, turns)
	for i := range steps {
		steps[i] = withUsage(textStep(fmt.Sprintf("m%d", i+1), "ok"), llm.Usage{InputTokens: 4000, OutputTokens: 100})
	}
	h := newHarness(t, harnessOptions{
		budget: sysctx.Budget{ContextWindow: 32000, MaxOutputTokens: 8192},
		steps:  steps,
	})
	ctx := context.Background()
	for i := 0; i < turns; i++ {
		res, err := h.engine.Run(ctx, Input{TurnID: fmt.Sprintf("t%d", i+1), Prompt: "hi"}, func(Event) {})
		if err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
		if res.StopReason != StopCompleted {
			t.Fatalf("run %d StopReason = %s (%s), want completed", i+1, res.StopReason, res.Err)
		}
	}
}

// TestUsageWithinTurnDoesNotAccumulate 验证单轮多步任务不会因为累计 usage 被提前结束。
// 每步上报 6000 token；4 步后累计 24000 已超过 blocking 21000，但真实上下文很小。
func TestUsageWithinTurnDoesNotAccumulate(t *testing.T) {
	noop := newFakeTool("Noop", true, true)
	steps := make([]fakeStep, 0, 5)
	for i := 1; i <= 4; i++ {
		step := toolUseStep(fmt.Sprintf("m%d", i), plannedCall{id: fmt.Sprintf("c%d", i), name: "Noop", input: `{}`})
		steps = append(steps, withUsage(step, llm.Usage{InputTokens: 5000, OutputTokens: 1000}))
	}
	steps = append(steps, withUsage(textStep("m5", "done"), llm.Usage{InputTokens: 5000, OutputTokens: 1000}))

	h := newHarness(t, harnessOptions{
		budget: sysctx.Budget{ContextWindow: 32000, MaxOutputTokens: 8192},
		tools:  []tool.Tool{noop},
		steps:  steps,
	})
	res, err := h.engine.Run(context.Background(), Input{TurnID: "t1", Prompt: "go"}, func(Event) {})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != StopCompleted {
		t.Fatalf("StopReason = %s (%s), want completed", res.StopReason, res.Err)
	}
	if got := h.provider.CallCount(); got != 5 {
		t.Fatalf("model calls = %d, want 5", got)
	}
}

// TestRestoreToleratesCorruptTail 验证 transcript 末尾有半行时，
// Restore 使用 Store 返回的可用记录继续恢复，而不是把 ErrCorruptLines 当致命错误。
func TestRestoreToleratesCorruptTail(t *testing.T) {
	dir := t.TempDir()
	store, err := session.NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	at := time.Unix(1700000000, 0)
	if err := store.CreateSession(ctx, session.Meta{ID: "s1", WorkDir: dir, CreatedAt: at}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	msg := &llm.Message{
		ID:     "m1",
		Role:   llm.RoleUser,
		Blocks: []llm.ContentBlock{{Type: llm.BlockText, Text: "hi"}},
		Time:   at.Add(time.Second),
	}
	if err := store.Append(ctx, session.Record{
		Type:      session.RecordMessage,
		SessionID: "s1",
		Time:      msg.Time,
		Message:   msg,
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "sessions", "s1.jsonl"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	if _, err := f.WriteString(`{"type":"message","sessionId":"s1"`); err != nil {
		t.Fatalf("write truncated line: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}

	h := newHarness(t, harnessOptions{})
	h.engine.cfg.Store = store
	if err := h.engine.Restore(ctx); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	hist := h.engine.History()
	if len(hist) != 1 || textOf(hist[0]) != "hi" {
		t.Fatalf("restored history = %+v, want 1 user message", hist)
	}
}

// TestRestoreUsesLastUsageNotCumulative 验证恢复时使用最后一条 usage 记录，
// 而不是整份 transcript 的累计值。
func TestRestoreUsesLastUsageNotCumulative(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	ctx := context.Background()
	for i, u := range []llm.Usage{
		{InputTokens: 4000, OutputTokens: 100},
		{InputTokens: 4000, OutputTokens: 100},
		{InputTokens: 500, OutputTokens: 50},
	} {
		usage := u
		if err := h.store.Append(ctx, session.Record{
			Type:      session.RecordUsage,
			SessionID: "s1",
			Time:      time.Unix(int64(1700000000+i), 0),
			Usage:     &usage,
		}); err != nil {
			t.Fatalf("Append usage %d: %v", i, err)
		}
	}

	eng, err := NewEngine(h.engine.cfg)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if err := eng.Restore(ctx); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	_, got := eng.metaSnapshot()
	if got != (llm.Usage{InputTokens: 500, OutputTokens: 50}) {
		t.Fatalf("restored lastUsage = %+v, want last record only", got)
	}
}

// failTurnEndStore 让 turn_end 记录落盘失败，其余记录照常写入。
type failTurnEndStore struct {
	session.Store
}

func (s failTurnEndStore) Append(ctx context.Context, rec session.Record) error {
	if rec.Type == session.RecordTurnEnd {
		return errors.New("disk full")
	}
	return s.Store.Append(ctx, rec)
}

// TestTurnEndIsLastEventWhenRecordFails 验证 turn_end 记录落盘失败时，
// 存储错误事件排在 turn_end 之前，turn_end 仍是本轮最后一个事件。
func TestTurnEndIsLastEventWhenRecordFails(t *testing.T) {
	h := newHarness(t, harnessOptions{steps: []fakeStep{textStep("m1", "ok")}})
	h.engine.cfg.Store = failTurnEndStore{Store: h.store}
	rec := newRecorder()

	res, err := h.engine.Run(context.Background(), Input{TurnID: "t1", Prompt: "hi"}, rec.sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != StopCompleted {
		t.Fatalf("StopReason = %s (%s), want completed", res.StopReason, res.Err)
	}

	events := rec.snapshot()
	if len(events) == 0 {
		t.Fatal("no events emitted")
	}
	last := events[len(events)-1]
	if last.Type != EventTurnEnd {
		t.Fatalf("last event = %s, want turn_end (all: %v)", last.Type, eventTypes(events))
	}
	if got := rec.count(EventTurnEnd); got != 1 {
		t.Fatalf("turn_end count = %d, want 1", got)
	}
	if got := rec.count(EventError); got != 1 {
		t.Fatalf("error count = %d, want 1 (all: %v)", got, eventTypes(events))
	}
}

// TestRestoreAppliesPersistedMode 验证恢复会话时沿用 transcript 中最后的 mode 记录。
func TestRestoreAppliesPersistedMode(t *testing.T) {
	h := newHarness(t, harnessOptions{mode: permission.ModeDefault})
	ctx := context.Background()
	if err := h.store.Append(ctx, session.Record{
		Type:      session.RecordMode,
		SessionID: "s1",
		Time:      time.Unix(1700000000, 0),
		Meta:      map[string]string{"mode": string(permission.ModeBypassPermissions)},
	}); err != nil {
		t.Fatalf("Append mode: %v", err)
	}

	eng, err := NewEngine(h.engine.cfg)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if err := eng.Restore(ctx); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := h.permissions.Mode(); got != permission.ModeBypassPermissions {
		t.Fatalf("restored mode = %q, want bypassPermissions", got)
	}
}
