package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Baike12/b-desktop/internal/llm"
	"github.com/Baike12/b-desktop/internal/session"
	"github.com/Baike12/b-desktop/internal/tool"
)

// recoveryFailureStep 构造一次在建立请求阶段就失败的调用。
func recoveryFailureStep(message string) fakeStep {
	return fakeStep{err: errors.New(message)}
}

// recoveryInterruptedStep 构造先产出文本增量、随后流中断的调用。
func recoveryInterruptedStep(messageID, text, message string) fakeStep {
	return fakeStep{
		events: []llm.StreamEvent{
			{Type: llm.EventMessageStart, MessageID: messageID},
			{Type: llm.EventBlockStart, Index: 0, Kind: llm.BlockText},
			{Type: llm.EventTextDelta, Index: 0, Kind: llm.BlockText, Text: text},
		},
		err: errors.New(message),
	}
}

// recoveryFailureMessages 返回历史中带失败摘要前缀的 user 消息文本。
func recoveryFailureMessages(msgs []llm.Message) []string {
	var out []string
	for _, m := range msgs {
		if m.Role != llm.RoleUser {
			continue
		}
		for _, b := range m.Blocks {
			if b.Type == llm.BlockText && strings.HasPrefix(b.Text, modelFailurePrefix) {
				out = append(out, b.Text)
			}
		}
	}
	return out
}

// recoveryRequestHasText 报告某次请求的消息里是否有完全匹配的文本块。
func recoveryRequestHasText(req llm.Request, text string) bool {
	for _, m := range req.Messages {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockText && b.Text == text {
				return true
			}
		}
	}
	return false
}

// TestModelFailureRecoversWithinTurn 验证 Provider.Stream 失败后不结束本轮：
// 失败摘要作为 user 消息回灌历史，下一次调用正常完成。
func TestModelFailureRecoversWithinTurn(t *testing.T) {
	h := newHarness(t, harnessOptions{steps: []fakeStep{
		recoveryFailureStep("connection reset"),
		textStep("m2", "recovered"),
	}})
	rec := newRecorder()

	res, err := h.engine.Run(context.Background(), Input{TurnID: "t1", Prompt: "hi"}, rec.sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != StopCompleted || res.Text != "recovered" {
		t.Fatalf("res = %+v, want completed with recovered text", res)
	}
	if res.Turns != 2 {
		t.Fatalf("Turns = %d, want 2", res.Turns)
	}
	if got := h.provider.CallCount(); got != 2 {
		t.Fatalf("model calls = %d, want 2", got)
	}

	want := modelFailurePrefix + "connection reset"
	calls := h.provider.Calls()
	if !recoveryRequestHasText(calls[1], want) {
		t.Fatalf("retry request missing failure summary: %+v", calls[1].Messages)
	}
	hist := h.engine.History()
	msgs := recoveryFailureMessages(hist)
	if len(msgs) != 1 || msgs[0] != want {
		t.Fatalf("history failure messages = %v, want [%s]", msgs, want)
	}
	if len(hist) != 3 || hist[1].Role != llm.RoleUser {
		t.Fatalf("history = %+v, want prompt + failure summary + assistant", hist)
	}

	// 摘要必须落盘，断点恢复后仍能看到。
	recs := loadRecords(t, h.store, "s1")
	found := false
	for _, r := range recs {
		if r.Type != session.RecordMessage || r.Message == nil {
			continue
		}
		for _, b := range r.Message.Blocks {
			if b.Type == llm.BlockText && b.Text == want {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("failure summary was not persisted")
	}

	events := rec.snapshot()
	if got := rec.count(EventError); got != 1 {
		t.Fatalf("error events = %d, want 1", got)
	}
	if got := rec.count(EventRetry); got != 1 {
		t.Fatalf("retry events = %d, want 1", got)
	}
	for _, ev := range events {
		if ev.Type == EventRetry {
			if ev.Retry == nil || ev.Retry.Attempt != 2 || ev.Retry.Max != modelFailureMaxAttempts || ev.Retry.Reason != "connection reset" {
				t.Fatalf("retry event = %+v", ev.Retry)
			}
		}
	}
	if last := events[len(events)-1]; last.Type != EventTurnEnd || last.StopReason != StopCompleted {
		t.Fatalf("last event = %+v", last)
	}
}

// TestModelStreamInterruptionRetries 验证流中断（已产出增量）同样回灌失败摘要并重试，
// 半截 assistant 消息被丢弃且不进入历史。
func TestModelStreamInterruptionRetries(t *testing.T) {
	h := newHarness(t, harnessOptions{steps: []fakeStep{
		recoveryInterruptedStep("m1", "par", "stream reset"),
		textStep("m2", "ok"),
	}})
	rec := newRecorder()

	res, err := h.engine.Run(context.Background(), Input{TurnID: "t1", Prompt: "hi"}, rec.sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != StopCompleted || res.Text != "ok" {
		t.Fatalf("res = %+v, want completed with ok", res)
	}
	if got := rec.count(EventMessageDiscard); got != 1 {
		t.Fatalf("message_discard count = %d, want 1", got)
	}
	want := modelFailurePrefix + "stream reset"
	if !recoveryRequestHasText(h.provider.Calls()[1], want) {
		t.Fatalf("retry request missing failure summary: %+v", h.provider.Calls()[1].Messages)
	}
	for _, m := range h.engine.History() {
		if m.Role == llm.RoleAssistant {
			for _, b := range m.Blocks {
				if b.Type == llm.BlockText && b.Text == "par" {
					t.Fatal("partial assistant message leaked into history")
				}
			}
		}
	}
	if got := rec.count(EventRetry); got != 1 {
		t.Fatalf("retry events = %d, want 1", got)
	}
}

// TestModelFailuresStopAfterRetries 验证连续第 3 次失败按不可恢复错误结束本轮。
func TestModelFailuresStopAfterRetries(t *testing.T) {
	h := newHarness(t, harnessOptions{steps: []fakeStep{
		recoveryFailureStep("fail-1"),
		recoveryFailureStep("fail-2"),
		recoveryFailureStep("fail-3"),
	}})
	rec := newRecorder()

	res, err := h.engine.Run(context.Background(), Input{TurnID: "t1", Prompt: "hi"}, rec.sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != StopError {
		t.Fatalf("StopReason = %s, want error", res.StopReason)
	}
	if !strings.Contains(res.Err, "fail-3") {
		t.Fatalf("Err = %q, want fail-3", res.Err)
	}
	if res.Turns != modelFailureMaxAttempts {
		t.Fatalf("Turns = %d, want %d", res.Turns, modelFailureMaxAttempts)
	}
	if got := h.provider.CallCount(); got != modelFailureMaxAttempts {
		t.Fatalf("model calls = %d, want %d", got, modelFailureMaxAttempts)
	}

	msgs := recoveryFailureMessages(h.engine.History())
	if len(msgs) != maxModelFailureRetries {
		t.Fatalf("failure summaries = %v, want %d", msgs, maxModelFailureRetries)
	}
	for i, msg := range msgs {
		want := modelFailurePrefix + []string{"fail-1", "fail-2"}[i]
		if msg != want {
			t.Fatalf("summary %d = %q, want %q", i, msg, want)
		}
	}
	if got := rec.count(EventRetry); got != maxModelFailureRetries {
		t.Fatalf("retry events = %d, want %d", got, maxModelFailureRetries)
	}
	if got := rec.count(EventError); got != modelFailureMaxAttempts {
		t.Fatalf("error events = %d, want %d", got, modelFailureMaxAttempts)
	}
	events := rec.snapshot()
	if last := events[len(events)-1]; last.Type != EventTurnEnd || last.StopReason != StopError {
		t.Fatalf("last event = %+v", last)
	}
	// 初始用户消息 + 两条失败摘要落盘；失败请求没有 usage 记录。
	recs := loadRecords(t, h.store, "s1")
	if got := countRecords(recs, session.RecordMessage); got != 1+maxModelFailureRetries {
		t.Fatalf("message records = %d, want %d", got, 1+maxModelFailureRetries)
	}
	if got := countRecords(recs, session.RecordUsage); got != 0 {
		t.Fatalf("usage records = %d, want 0", got)
	}
}

// TestModelFailureStreakResetsAfterSuccess 验证一次成功调用打断连续失败计数：
// 成功之后仍可再重试两次，而不是累计到上限就终止。
func TestModelFailureStreakResetsAfterSuccess(t *testing.T) {
	read := newFakeTool("Read", true, true)
	h := newHarness(t, harnessOptions{
		tools: []tool.Tool{read},
		steps: []fakeStep{
			recoveryFailureStep("fail-a"),
			recoveryFailureStep("fail-b"),
			toolUseStep("m1", plannedCall{id: "tu1", name: "Read", input: `{}`}),
			recoveryFailureStep("fail-c"),
			recoveryFailureStep("fail-d"),
			textStep("m2", "done"),
		},
	})
	rec := newRecorder()

	res, err := h.engine.Run(context.Background(), Input{TurnID: "t1", Prompt: "hi"}, rec.sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != StopCompleted || res.Text != "done" {
		t.Fatalf("res = %+v, want completed with done", res)
	}
	if got := h.provider.CallCount(); got != 6 {
		t.Fatalf("model calls = %d, want 6", got)
	}
	if got := rec.count(EventRetry); got != 4 {
		t.Fatalf("retry events = %d, want 4", got)
	}
	if got := len(recoveryFailureMessages(h.engine.History())); got != 4 {
		t.Fatalf("failure summaries = %d, want 4", got)
	}
}

// recoveryBlockingProvider 返回一条阻塞到 ctx 取消的流。
type recoveryBlockingProvider struct {
	started chan struct{}
	once    sync.Once
}

// Name 返回 "recovery-blocking"。
func (p *recoveryBlockingProvider) Name() string { return "recovery-blocking" }

// Stream 返回阻塞流，并通知已进入模型调用。
func (p *recoveryBlockingProvider) Stream(context.Context, llm.Request) (llm.Stream, error) {
	p.once.Do(func() { close(p.started) })
	return recoveryBlockingStream{}, nil
}

// recoveryBlockingStream 等待 ctx 取消后返回取消错误。
type recoveryBlockingStream struct{}

// Recv 阻塞到 ctx 取消。
func (recoveryBlockingStream) Recv(ctx context.Context) (llm.StreamEvent, error) {
	<-ctx.Done()
	return llm.StreamEvent{}, ctx.Err()
}

// Close 实现 llm.Stream。
func (recoveryBlockingStream) Close() error { return nil }

// TestCanceledModelFailureEndsTurn 验证取消导致的失败不写失败摘要、不重试。
func TestCanceledModelFailureEndsTurn(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	provider := &recoveryBlockingProvider{started: make(chan struct{})}
	h.engine.cfg.Provider = provider
	rec := newRecorder()

	ctx, cancel := context.WithCancel(context.Background())
	ch := runAsync(t, h, ctx, rec, Input{TurnID: "t1", Prompt: "hi"})
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("model call did not start")
	}
	cancel()

	out := awaitRun(t, ch, 3*time.Second)
	if out.res.StopReason != StopCanceled {
		t.Fatalf("StopReason = %s, want canceled", out.res.StopReason)
	}
	if got := recoveryFailureMessages(h.engine.History()); len(got) != 0 {
		t.Fatalf("failure summaries = %v, want none", got)
	}
	if got := rec.count(EventRetry); got != 0 {
		t.Fatalf("retry events = %d, want 0", got)
	}
	events := rec.snapshot()
	if last := events[len(events)-1]; last.Type != EventTurnEnd || last.StopReason != StopCanceled {
		t.Fatalf("last event = %+v", last)
	}
}
