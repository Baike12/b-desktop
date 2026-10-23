package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Baike12/b-desktop/internal/llm"
	"github.com/Baike12/b-desktop/internal/permission"
	"github.com/Baike12/b-desktop/internal/session"
	"github.com/Baike12/b-desktop/internal/tool"
)

// requireOrder 断言这些事件类型第一次出现的顺序单调递增。
func requireOrder(t *testing.T, events []Event, types ...EventType) {
	t.Helper()
	prev := -1
	for _, typ := range types {
		idx := indexOfFrom(events, typ, prev+1)
		if idx < 0 {
			t.Fatalf("event %s not found after index %d in %v", typ, prev, eventTypes(events))
		}
		prev = idx
	}
}

// eventTypes 提取事件类型序列，便于失败时排查。
func eventTypes(events []Event) []EventType {
	out := make([]EventType, len(events))
	for i, ev := range events {
		out[i] = ev.Type
	}
	return out
}

func TestRunCompletesWithoutToolUse(t *testing.T) {
	h := newHarness(t, harnessOptions{steps: []fakeStep{textStep("m1", "he", "llo")}})
	rec := newRecorder()

	res, err := h.engine.Run(context.Background(), Input{TurnID: "t1", Prompt: "hi"}, rec.sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != StopCompleted {
		t.Fatalf("StopReason = %s, want completed", res.StopReason)
	}
	if res.Text != "hello" {
		t.Fatalf("Text = %q, want hello", res.Text)
	}
	if res.Turns != 1 {
		t.Fatalf("Turns = %d, want 1", res.Turns)
	}
	if res.Usage.InputTokens != 10 || res.Usage.OutputTokens != 2 {
		t.Fatalf("Usage = %+v", res.Usage)
	}

	events := rec.snapshot()
	if len(events) == 0 || events[0].Type != EventTurnStart {
		t.Fatalf("first event = %v, want turn_start", eventTypes(events))
	}
	last := events[len(events)-1]
	if last.Type != EventTurnEnd || last.StopReason != StopCompleted {
		t.Fatalf("last event = %+v, want turn_end completed", last)
	}
	if got := rec.count(EventTurnEnd); got != 1 {
		t.Fatalf("turn_end count = %d, want 1", got)
	}
	// 上下文规模来自最近一次调用（输入 + 输出），随 usage 与 turn_end 下发。
	if last.ContextTokens != 12 {
		t.Fatalf("turn_end contextTokens = %d, want 12", last.ContextTokens)
	}
	var usageEvent *Event
	for i := range events {
		if events[i].Type == EventUsage {
			usageEvent = &events[i]
		}
	}
	if usageEvent == nil || usageEvent.ContextTokens != 12 {
		t.Fatalf("usage event = %+v, want contextTokens 12", usageEvent)
	}
	// 每个 provider delta 各转成一条事件，不能聚合成一条。
	if got := rec.count(EventTextDelta); got != 2 {
		t.Fatalf("text_delta count = %d, want 2", got)
	}
	requireOrder(t, events,
		EventTurnStart, EventStepStart, EventMessageStart, EventBlockStart,
		EventTextDelta, EventMessageStop, EventUsage, EventTurnEnd)

	hist := h.engine.History()
	if len(hist) != 2 || hist[0].Role != llm.RoleUser || hist[1].Role != llm.RoleAssistant {
		t.Fatalf("history = %+v", hist)
	}
	if got := historyBlockTexts(hist, llm.BlockText); len(got) != 2 || got[1] != "hello" {
		t.Fatalf("history text blocks = %v", got)
	}

	recs := loadRecords(t, h.store, "s1")
	if got := countRecords(recs, session.RecordMessage); got != 2 {
		t.Fatalf("message records = %d, want 2", got)
	}
	if got := countRecords(recs, session.RecordUsage); got != 1 {
		t.Fatalf("usage records = %d, want 1", got)
	}
	if got := countRecords(recs, session.RecordTurnEnd); got != 1 {
		t.Fatalf("turn_end records = %d, want 1", got)
	}
}

func TestRunAdvancesOnToolUse(t *testing.T) {
	read := newFakeTool("Read", true, true)
	read.runFn = func(context.Context, json.RawMessage) (tool.Result, error) {
		return tool.Result{Content: "file-content"}, nil
	}
	h := newHarness(t, harnessOptions{
		tools: []tool.Tool{read},
		steps: []fakeStep{
			toolUseStep("m1", plannedCall{id: "t1", name: "Read", input: `{"path":"a"}`}),
			textStep("m2", "done"),
		},
	})
	rec := newRecorder()

	res, err := h.engine.Run(context.Background(), Input{TurnID: "t1", Prompt: "read it"}, rec.sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != StopCompleted || res.Turns != 2 {
		t.Fatalf("res = %+v, want completed with 2 turns", res)
	}
	if read.callCount() != 1 {
		t.Fatalf("tool calls = %d, want 1", read.callCount())
	}

	calls := h.provider.Calls()
	if len(calls) != 2 {
		t.Fatalf("model calls = %d, want 2", len(calls))
	}
	var gotResult *llm.ContentBlock
	for _, m := range calls[1].Messages {
		for i := range m.Blocks {
			b := m.Blocks[i]
			if b.Type == llm.BlockToolResult && b.ToolUseID == "t1" {
				gotResult = &b
			}
		}
	}
	if gotResult == nil {
		t.Fatalf("second request has no tool_result: %+v", calls[1].Messages)
	}
	if gotResult.Text != "file-content" || gotResult.IsError {
		t.Fatalf("tool_result = %+v", gotResult)
	}

	events := rec.snapshot()
	// 工具结果在第一步 message_stop 之后、第二步 step_start 之前。
	firstStop := indexOf(events, EventMessageStop)
	secondStep := indexOfFrom(events, EventStepStart, firstStop+1)
	toolResult := indexOf(events, EventToolResult)
	if firstStop < 0 || secondStep < 0 || toolResult < 0 {
		t.Fatalf("missing events: %v", eventTypes(events))
	}
	if !(firstStop < toolResult && toolResult < secondStep) {
		t.Fatalf("tool_result order: message_stop=%d tool_result=%d step_start=%d", firstStop, toolResult, secondStep)
	}
	if got := rec.count(EventToolResult); got != 1 {
		t.Fatalf("tool_result count = %d, want 1", got)
	}
	if ev := events[toolResult]; ev.Result == nil || ev.Result.IsError || ev.Result.Preview != "file-content" {
		t.Fatalf("tool_result event = %+v", ev)
	}
}

func TestMaxTurnsBoundsModelCalls(t *testing.T) {
	read := newFakeTool("Read", true, true)
	h := newHarness(t, harnessOptions{
		maxTurns: 2,
		tools:    []tool.Tool{read},
		steps: []fakeStep{
			toolUseStep("m1", plannedCall{id: "t1", name: "Read", input: `{}`}),
			toolUseStep("m2", plannedCall{id: "t2", name: "Read", input: `{}`}),
			textStep("m3", "unused"),
		},
	})
	rec := newRecorder()

	res, err := h.engine.Run(context.Background(), Input{TurnID: "t1", Prompt: "loop"}, rec.sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != StopMaxTurns {
		t.Fatalf("StopReason = %s, want max_turns", res.StopReason)
	}
	if h.provider.CallCount() != 2 {
		t.Fatalf("model calls = %d, want 2", h.provider.CallCount())
	}
	if res.Turns != 2 {
		t.Fatalf("Turns = %d, want 2", res.Turns)
	}
	events := rec.snapshot()
	if last := events[len(events)-1]; last.Type != EventTurnEnd || last.StopReason != StopMaxTurns {
		t.Fatalf("last event = %+v", last)
	}
}

func TestReadOnlyToolsRunInParallel(t *testing.T) {
	tracker := &concurrencyTracker{}
	var tools []tool.Tool
	var calls []plannedCall
	for i := 0; i < 4; i++ {
		name := "Read" + string(rune('A'+i))
		ft := newFakeTool(name, true, true)
		ft.runFn = func(context.Context, json.RawMessage) (tool.Result, error) {
			tracker.enter()
			defer tracker.exit()
			time.Sleep(40 * time.Millisecond)
			return tool.Result{Content: "ok"}, nil
		}
		tools = append(tools, ft)
		calls = append(calls, plannedCall{id: "t" + string(rune('1'+i)), name: name, input: `{}`})
	}
	h := newHarness(t, harnessOptions{
		tools: tools,
		steps: []fakeStep{toolUseStep("m1", calls...), textStep("m2", "done")},
	})

	res, err := h.engine.Run(context.Background(), Input{TurnID: "t1", Prompt: "parallel"}, func(Event) {})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != StopCompleted {
		t.Fatalf("StopReason = %s", res.StopReason)
	}
	if peak := tracker.peak(); peak < 2 {
		t.Fatalf("peak concurrency = %d, want >= 2", peak)
	}
}

func TestWriteToolsRunSerially(t *testing.T) {
	tracker := &concurrencyTracker{}
	var tools []tool.Tool
	var calls []plannedCall
	for i := 0; i < 3; i++ {
		name := "Write" + string(rune('A'+i))
		ft := newFakeTool(name, false, false)
		ft.runFn = func(context.Context, json.RawMessage) (tool.Result, error) {
			tracker.enter()
			defer tracker.exit()
			time.Sleep(20 * time.Millisecond)
			return tool.Result{Content: "written"}, nil
		}
		tools = append(tools, ft)
		calls = append(calls, plannedCall{id: "t" + string(rune('1'+i)), name: name, input: `{}`})
	}
	h := newHarness(t, harnessOptions{
		tools: tools,
		steps: []fakeStep{toolUseStep("m1", calls...), textStep("m2", "done")},
	})

	res, err := h.engine.Run(context.Background(), Input{TurnID: "t1", Prompt: "serial"}, func(Event) {})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != StopCompleted {
		t.Fatalf("StopReason = %s", res.StopReason)
	}
	if peak := tracker.peak(); peak != 1 {
		t.Fatalf("peak concurrency = %d, want 1", peak)
	}
}

func TestPermissionDenyProducesResult(t *testing.T) {
	bash := newFakeTool("Bash", false, false)
	h := newHarness(t, harnessOptions{
		mode:  permission.ModePlan,
		tools: []tool.Tool{bash},
		steps: []fakeStep{
			toolUseStep("m1", plannedCall{id: "t1", name: "Bash", input: `{"command":"echo hi"}`}),
			textStep("m2", "ok"),
		},
	})
	rec := newRecorder()

	res, err := h.engine.Run(context.Background(), Input{TurnID: "t1", Prompt: "run"}, rec.sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != StopCompleted {
		t.Fatalf("StopReason = %s", res.StopReason)
	}
	if bash.callCount() != 0 {
		t.Fatalf("denied tool ran %d times", bash.callCount())
	}
	texts := historyBlockTexts(h.engine.History(), llm.BlockToolResult)
	if len(texts) != 1 || !strings.Contains(texts[0], "permission denied") {
		t.Fatalf("tool results = %v", texts)
	}
	events := rec.snapshot()
	if got := rec.count(EventToolResult); got != 1 {
		t.Fatalf("tool_result count = %d", got)
	}
	if idx := indexOf(events, EventToolResult); idx < 0 || events[idx].Result == nil || !events[idx].Result.IsError {
		t.Fatalf("tool_result event = %+v", events[idx])
	}
}

func TestToolFailuresEachProduceExactlyOneResult(t *testing.T) {
	boom := newFakeTool("Boom", false, false)
	boom.runFn = func(context.Context, json.RawMessage) (tool.Result, error) {
		return tool.Result{}, context.DeadlineExceeded
	}
	panicky := newFakeTool("Panic", false, false)
	panicky.runFn = func(context.Context, json.RawMessage) (tool.Result, error) {
		panic("kaboom")
	}
	read := newFakeTool("Read", true, true)
	h := newHarness(t, harnessOptions{
		tools: []tool.Tool{boom, panicky, read},
		steps: []fakeStep{
			toolUseStep("m1",
				plannedCall{id: "c1", name: "Nope", input: `{}`},
				plannedCall{id: "c2", name: "Boom", input: `{}`},
				plannedCall{id: "c3", name: "Panic", input: `{}`},
				plannedCall{id: "c4", name: "Read", input: `{bad`},
			),
			textStep("m2", "done"),
		},
	})
	rec := newRecorder()

	res, err := h.engine.Run(context.Background(), Input{TurnID: "t1", Prompt: "fail"}, rec.sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != StopCompleted {
		t.Fatalf("StopReason = %s", res.StopReason)
	}
	if got := rec.count(EventToolResult); got != 4 {
		t.Fatalf("tool_result events = %d, want 4", got)
	}
	for _, ev := range rec.snapshot() {
		if ev.Type == EventToolResult && (ev.Result == nil || !ev.Result.IsError) {
			t.Fatalf("expected error result, got %+v", ev)
		}
	}

	// 最后一条 user 消息必须按顺序包含四个结果，且每个 tool_use 恰好一个。
	hist := h.engine.History()
	var blocks []llm.ContentBlock
	for _, m := range hist {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult {
				blocks = append(blocks, b)
			}
		}
	}
	if len(blocks) != 4 {
		t.Fatalf("tool_result blocks = %d, want 4", len(blocks))
	}
	wantIDs := []string{"c1", "c2", "c3", "c4"}
	for i, b := range blocks {
		if b.ToolUseID != wantIDs[i] {
			t.Fatalf("block %d id = %s, want %s", i, b.ToolUseID, wantIDs[i])
		}
		if !b.IsError {
			t.Fatalf("block %d not marked error: %+v", i, b)
		}
	}
}

func TestApprovalAllowOnceRunsTool(t *testing.T) {
	write := newFakeTool("Write", false, false)
	h := newHarness(t, harnessOptions{
		mode:  permission.ModeDefault,
		tools: []tool.Tool{write},
		steps: []fakeStep{
			toolUseStep("m1", plannedCall{id: "t1", name: "Write", input: `{"file_path":"a.txt","content":"x"}`}),
			textStep("m2", "done"),
		},
	})
	rec := newRecorder()
	ch := runAsync(t, h, context.Background(), rec, Input{TurnID: "t1", Prompt: "write"})

	ev := rec.waitFor(t, EventPermissionRequest, 2*time.Second)
	if ev.Approval == nil || ev.Approval.RequestID == "" {
		t.Fatalf("approval event = %+v", ev)
	}
	if len(ev.Approval.Options) != 4 {
		t.Fatalf("approval options = %+v, want 4", ev.Approval.Options)
	}
	if !h.approvals.Decide(ev.Approval.RequestID, permission.Response{Option: permission.OptionAllowOnce}) {
		t.Fatal("Decide returned false")
	}

	out := awaitRun(t, ch, 3*time.Second)
	if out.res.StopReason != StopCompleted {
		t.Fatalf("StopReason = %s", out.res.StopReason)
	}
	if write.callCount() != 1 {
		t.Fatalf("tool calls = %d, want 1", write.callCount())
	}
	if got := rec.count(EventPermissionResolved); got != 1 {
		t.Fatalf("permission_resolved count = %d, want 1", got)
	}
	for _, e := range rec.snapshot() {
		if e.Type == EventPermissionResolved && e.Text != permission.OptionAllowOnce {
			t.Fatalf("resolved text = %q", e.Text)
		}
	}
}

// TestApprovalAllowAlwaysSkipsRepeat 验证「总是允许」写入会话规则后，
// 同样的 Bash 命令第二次执行不再询问。
func TestApprovalAllowAlwaysSkipsRepeat(t *testing.T) {
	bash := newFakeTool("Bash", false, false)
	h := newHarness(t, harnessOptions{
		mode:  permission.ModeDefault,
		tools: []tool.Tool{bash},
		steps: []fakeStep{
			toolUseStep("m1", plannedCall{id: "t1", name: "Bash", input: `{"command":"echo hi"}`}),
			toolUseStep("m2", plannedCall{id: "t2", name: "Bash", input: `{"command":"echo hi"}`}),
			textStep("m3", "done"),
		},
	})
	rec := newRecorder()
	ch := runAsync(t, h, context.Background(), rec, Input{TurnID: "t1", Prompt: "run"})

	ev := rec.waitFor(t, EventPermissionRequest, 2*time.Second)
	if ev.Approval == nil || ev.Approval.RequestID == "" {
		t.Fatalf("approval event = %+v", ev)
	}
	if len(ev.Approval.Options) != 4 {
		t.Fatalf("approval options = %+v, want 4", ev.Approval.Options)
	}
	if !h.approvals.Decide(ev.Approval.RequestID, permission.Response{Option: permission.OptionAllowAlways}) {
		t.Fatal("Decide returned false")
	}

	out := awaitRun(t, ch, 3*time.Second)
	if out.res.StopReason != StopCompleted {
		t.Fatalf("StopReason = %s (%s)", out.res.StopReason, out.res.Err)
	}
	if got := rec.count(EventPermissionRequest); got != 1 {
		t.Fatalf("permission requests = %d, want 1 (rule must suppress the repeat)", got)
	}
	if got := bash.callCount(); got != 2 {
		t.Fatalf("tool calls = %d, want 2", got)
	}
	rules := h.permissions.Rules()
	if len(rules) != 1 {
		t.Fatalf("session rules = %+v, want 1", rules)
	}
	if rules[0].Effect != permission.EffectAllow || rules[0].Pattern != "echo hi" {
		t.Fatalf("session rule = %+v", rules[0])
	}
}

func TestApprovalCanceledByContext(t *testing.T) {
	write := newFakeTool("Write", false, false)
	h := newHarness(t, harnessOptions{
		mode:  permission.ModeDefault,
		tools: []tool.Tool{write},
		steps: []fakeStep{
			toolUseStep("m1", plannedCall{id: "t1", name: "Write", input: `{}`}),
			textStep("m2", "unused"),
		},
	})
	rec := newRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	ch := runAsync(t, h, ctx, rec, Input{TurnID: "t1", Prompt: "write"})

	rec.waitFor(t, EventPermissionRequest, 2*time.Second)
	cancel()

	out := awaitRun(t, ch, 3*time.Second)
	if out.res.StopReason != StopCanceled {
		t.Fatalf("StopReason = %s, want canceled", out.res.StopReason)
	}
	if write.callCount() != 0 {
		t.Fatalf("tool ran %d times after cancel", write.callCount())
	}
	if got := rec.count(EventPermissionResolved); got != 1 {
		t.Fatalf("permission_resolved count = %d", got)
	}
	texts := historyBlockTexts(h.engine.History(), llm.BlockToolResult)
	if len(texts) != 1 || !strings.Contains(texts[0], "canceled") {
		t.Fatalf("tool results = %v", texts)
	}
	if got := rec.count(EventToolResult); got != 1 {
		t.Fatalf("tool_result events = %d", got)
	}
}

func TestCancelDuringToolReturnsCanceledResult(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	slow := newFakeTool("Slow", true, false)
	slow.runFn = func(ctx context.Context, _ json.RawMessage) (tool.Result, error) {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return tool.Result{}, ctx.Err()
	}
	h := newHarness(t, harnessOptions{
		tools: []tool.Tool{slow},
		steps: []fakeStep{
			toolUseStep("m1", plannedCall{id: "t1", name: "Slow", input: `{}`}),
			textStep("m2", "unused"),
		},
	})
	rec := newRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	ch := runAsync(t, h, ctx, rec, Input{TurnID: "t1", Prompt: "slow"})

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("tool did not start")
	}
	cancel()

	out := awaitRun(t, ch, 3*time.Second)
	if out.res.StopReason != StopCanceled {
		t.Fatalf("StopReason = %s, want canceled", out.res.StopReason)
	}
	events := rec.snapshot()
	if last := events[len(events)-1]; last.Type != EventTurnEnd || last.StopReason != StopCanceled {
		t.Fatalf("last event = %+v", last)
	}
	if got := rec.count(EventToolResult); got != 1 {
		t.Fatalf("tool_result events = %d, want 1", got)
	}
	for _, e := range rec.snapshot() {
		if e.Type == EventToolResult && (e.Result == nil || !e.Result.IsError) {
			t.Fatalf("canceled tool result = %+v", e)
		}
	}

	// 取消后的 tool_result 仍必须落盘。
	recs := loadRecords(t, h.store, "s1")
	found := false
	for _, r := range recs {
		if r.Type != session.RecordMessage || r.Message == nil {
			continue
		}
		for _, b := range r.Message.Blocks {
			if b.Type == llm.BlockToolResult && b.ToolUseID == "t1" && strings.Contains(b.Text, "cancel") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("canceled tool_result was not persisted")
	}
}

// countRecords 统计某种类型的记录数。
func countRecords(recs []session.Record, typ session.RecordType) int {
	n := 0
	for _, r := range recs {
		if r.Type == typ {
			n++
		}
	}
	return n
}

func TestEventsAreEmittedFromSingleGoroutine(t *testing.T) {
	var inFlight int32
	var overlapped int32
	sink := func(Event) {
		if !atomic.CompareAndSwapInt32(&inFlight, 0, 1) {
			atomic.StoreInt32(&overlapped, 1)
		}
		time.Sleep(200 * time.Microsecond)
		atomic.StoreInt32(&inFlight, 0)
	}

	var tools []tool.Tool
	var calls []plannedCall
	for i := 0; i < 4; i++ {
		name := "Read" + string(rune('A'+i))
		ft := newFakeTool(name, true, true)
		ft.runFn = func(ctx context.Context, _ json.RawMessage) (tool.Result, error) {
			time.Sleep(10 * time.Millisecond)
			return tool.Result{Content: "ok"}, nil
		}
		tools = append(tools, ft)
		calls = append(calls, plannedCall{id: "t" + string(rune('1'+i)), name: name, input: `{}`})
	}
	h := newHarness(t, harnessOptions{
		tools: tools,
		steps: []fakeStep{toolUseStep("m1", calls...), textStep("m2", "done")},
	})
	if _, err := h.engine.Run(context.Background(), Input{TurnID: "t1", Prompt: "x"}, sink); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if atomic.LoadInt32(&overlapped) != 0 {
		t.Fatal("EventSink was called concurrently")
	}
}
