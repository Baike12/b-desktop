package context

import (
	stdctx "context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"anc-dsk/internal/llm"
)

// fixedEstimator 对任何输入都返回同一个 token 数，用于精确控制阈值。
type fixedEstimator int

func (f fixedEstimator) EstimateString(string) int          { return int(f) }
func (f fixedEstimator) EstimateMessages([]llm.Message) int { return int(f) }

// fakeSummarizer 是可编程的摘要器，记录调用次数与调用顺序钩子。
type fakeSummarizer struct {
	mu        sync.Mutex
	calls     int
	summary   string
	err       error
	onCall    func()
	lastInstr string
	lastMsgs  int
}

func (f *fakeSummarizer) Summarize(_ stdctx.Context, msgs []llm.Message, instruction string) (string, error) {
	f.mu.Lock()
	f.calls++
	f.lastInstr = instruction
	f.lastMsgs = len(msgs)
	hook := f.onCall
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return f.summary, f.err
}

func (f *fakeSummarizer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type progressLog struct {
	mu     sync.Mutex
	events []CompactProgress
}

func (p *progressLog) fn() CompactProgressFunc {
	return func(ev CompactProgress) {
		p.mu.Lock()
		p.events = append(p.events, ev)
		p.mu.Unlock()
	}
}

func (p *progressLog) phases() []CompactPhase {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]CompactPhase, 0, len(p.events))
	for _, ev := range p.events {
		out = append(out, ev.Phase)
	}
	return out
}

func toolUseMessage(id string) llm.Message {
	return llm.Message{
		Role: llm.RoleAssistant,
		Blocks: []llm.ContentBlock{{
			Type:      llm.BlockToolUse,
			ToolUseID: id,
			ToolName:  "Read",
			Input:     json.RawMessage(`{"file_path":"a.go"}`),
		}},
	}
}

func toolResultMessage(id, text string) llm.Message {
	return llm.Message{
		Role: llm.RoleUser,
		Blocks: []llm.ContentBlock{{
			Type:      llm.BlockToolResult,
			ToolUseID: id,
			Text:      text,
		}},
	}
}

func userText(text string) llm.Message {
	return llm.Message{Role: llm.RoleUser, Blocks: []llm.ContentBlock{{Type: llm.BlockText, Text: text}}}
}

func assistantText(text string) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Blocks: []llm.ContentBlock{{Type: llm.BlockText, Text: text}}}
}

// toolRounds 生成 n 组「assistant tool_use + user tool_result」。
func toolRounds(n int, result string) []llm.Message {
	msgs := make([]llm.Message, 0, n*2)
	for i := 0; i < n; i++ {
		id := "t" + string(rune('a'+i))
		msgs = append(msgs, toolUseMessage(id), toolResultMessage(id, result))
	}
	return msgs
}

func TestBudgetThresholds(t *testing.T) {
	cases := []struct {
		window      int
		effective   int
		auto        int
		warning     int
		blocking    int
		autoEnabled bool
	}{
		{8192, 6144, 3072, 0, 3144, false},
		{32768, 24576, 12288, 0, 21576, true},
		{200000, 191808, 178808, 158808, 188808, true},
	}
	for _, c := range cases {
		b := NewBudget(Budget{ContextWindow: c.window})
		if got := b.EffectiveWindow(); got != c.effective {
			t.Errorf("window=%d EffectiveWindow=%d, want %d", c.window, got, c.effective)
		}
		if got := b.AutoCompactThreshold(); got != c.auto {
			t.Errorf("window=%d AutoCompactThreshold=%d, want %d", c.window, got, c.auto)
		}
		if got := b.WarningThreshold(); got != c.warning {
			t.Errorf("window=%d WarningThreshold=%d, want %d", c.window, got, c.warning)
		}
		if got := b.BlockingLimit(); got != c.blocking {
			t.Errorf("window=%d BlockingLimit=%d, want %d", c.window, got, c.blocking)
		}
		if got := b.AutoCompactEnabled(); got != c.autoEnabled {
			t.Errorf("window=%d AutoCompactEnabled=%v, want %v", c.window, got, c.autoEnabled)
		}
	}
}

func TestBudgetDefaultsAndStatus(t *testing.T) {
	b := NewBudget(Budget{})
	if b.MaxOutputTokens != defaultMaxOutputTokens {
		t.Fatalf("MaxOutputTokens = %d, want %d", b.MaxOutputTokens, defaultMaxOutputTokens)
	}
	if b.AutoBuffer != defaultAutoBuffer || b.ReservedForSummary != defaultReservedForSummary || b.BlockingBuffer != defaultBlockingBuffer {
		t.Fatalf("零值预算未填默认值: %+v", b)
	}

	small := NewBudget(Budget{ContextWindow: 8192})
	if got := small.Status(0); got != StatusOK {
		t.Errorf("8k/0 = %q, want ok", got)
	}
	if got := small.Status(3071); got != StatusOK {
		t.Errorf("8k/3071 = %q, want ok", got)
	}
	if got := small.Status(3072); got != StatusAuto {
		t.Errorf("8k/3072 = %q, want auto", got)
	}
	if got := small.Status(3144); got != StatusBlocking {
		t.Errorf("8k/3144 = %q, want blocking", got)
	}

	large := NewBudget(Budget{ContextWindow: 200000})
	if got := large.Status(158807); got != StatusOK {
		t.Errorf("200k/158807 = %q, want ok", got)
	}
	if got := large.Status(158808); got != StatusWarning {
		t.Errorf("200k/158808 = %q, want warning", got)
	}
	if got := large.Status(178808); got != StatusAuto {
		t.Errorf("200k/178808 = %q, want auto", got)
	}
	if got := large.Status(188808); got != StatusBlocking {
		t.Errorf("200k/188808 = %q, want blocking", got)
	}
}

func TestMicroCompactClearsOldToolResults(t *testing.T) {
	long := strings.Repeat("x", 2000)
	msgs := toolRounds(7, long)

	out, cleared, saved := MicroCompact(msgs, 5)
	if cleared != 2 {
		t.Fatalf("cleared = %d, want 2", cleared)
	}
	if saved < microCompactMinSaved {
		t.Fatalf("saved = %d, want >= %d", saved, microCompactMinSaved)
	}
	if len(out) != len(msgs) {
		t.Fatalf("消息数量应保持不变: %d → %d", len(msgs), len(out))
	}
	for _, idx := range []int{1, 3} {
		if got := out[idx].Blocks[0].Text; got != clearedToolResult {
			t.Errorf("out[%d] 应被清空，实际 %q", idx, got)
		}
		if out[idx].Blocks[0].ToolUseID == "" {
			t.Errorf("out[%d] 丢失 tool_use 关联", idx)
		}
	}
	for _, idx := range []int{5, 7, 9, 11, 13} {
		if got := out[idx].Blocks[0].Text; got != long {
			t.Errorf("最近结果 out[%d] 不应被清空", idx)
		}
	}
	// 入参不可被修改。
	if got := msgs[1].Blocks[0].Text; got != long {
		t.Fatalf("MicroCompact 修改了入参")
	}
	// tool_use 消息原样保留。
	if out[0].Blocks[0].Type != llm.BlockToolUse || out[0].Blocks[0].ToolUseID != "ta" {
		t.Fatalf("tool_use 消息被破坏: %+v", out[0])
	}
}

func TestMicroCompactAbandonsSmallSavings(t *testing.T) {
	msgs := toolRounds(6, "short")
	msgs[1].Blocks[0].Text = "short"
	out, cleared, saved := MicroCompact(msgs, 5)
	if cleared != 0 || saved != 0 {
		t.Fatalf("节省不足时应放弃：cleared=%d saved=%d", cleared, saved)
	}
	if out[1].Blocks[0].Text != "short" {
		t.Fatalf("放弃时应返回原内容")
	}
}

func TestBeforeCallMicroTriggeredByIdleTime(t *testing.T) {
	now := fixedNow()
	sum := &fakeSummarizer{summary: "summary"}
	c, err := NewCompactor(CompactorConfig{
		Summarizer: sum,
		Estimator:  fixedEstimator(100),
		Budget:     Budget{ContextWindow: 200000},
		Now:        func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	msgs := toolRounds(6, strings.Repeat("x", 2000))
	prog := &progressLog{}
	res, err := c.BeforeCall(stdctx.Background(), BeforeCallInput{
		Messages:        msgs,
		LastAssistantAt: now.Add(-2 * time.Hour),
	}, prog.fn())
	if err != nil {
		t.Fatalf("BeforeCall: %v", err)
	}
	if !res.Compacted || !res.Micro {
		t.Fatalf("应按空闲时间触发 microCompact: %+v", res)
	}
	if res.Info != nil {
		t.Fatalf("microCompact 不应产生摘要信息")
	}
	if got := prog.phases(); !reflect.DeepEqual(got, []CompactPhase{CompactPhaseStart, CompactPhaseDone}) {
		t.Fatalf("进度 = %v", got)
	}
	if sum.callCount() != 0 {
		t.Fatalf("microCompact 不应调用摘要器")
	}
}

func TestBeforeCallAutoTriggeredByEstimate(t *testing.T) {
	var order []string
	var mu sync.Mutex
	appendOrder := func(s string) {
		mu.Lock()
		order = append(order, s)
		mu.Unlock()
	}
	sum := &fakeSummarizer{summary: "Goal: ship it\nState: done", onCall: func() { appendOrder("summarize") }}
	c, err := NewCompactor(CompactorConfig{
		Summarizer: sum,
		Estimator:  fixedEstimator(190000),
		Budget:     Budget{ContextWindow: 200000},
		Model:      "summary-model",
	})
	if err != nil {
		t.Fatal(err)
	}

	msgs := toolRounds(4, "result")
	prog := &progressLog{}
	progress := func(ev CompactProgress) {
		appendOrder(string(ev.Phase))
		prog.fn()(ev)
	}
	res, err := c.BeforeCall(stdctx.Background(), BeforeCallInput{Messages: msgs}, progress)
	if err != nil {
		t.Fatalf("BeforeCall: %v", err)
	}
	if !res.Compacted || res.Info == nil {
		t.Fatalf("应触发 autoCompact: %+v", res)
	}
	if res.Info.Trigger != "auto" || res.Info.Model != "summary-model" {
		t.Fatalf("CompactInfo 元数据错误: %+v", res.Info)
	}
	if res.Info.PreTokens != 190000 {
		t.Fatalf("PreTokens = %d", res.Info.PreTokens)
	}
	if len(res.Info.KeptMessages) != 4 {
		t.Fatalf("KeptMessages = %d, want 4", len(res.Info.KeptMessages))
	}
	if res.Messages[0].Role != llm.RoleUser || !strings.Contains(res.Messages[0].Blocks[0].Text, "The conversation was compacted") {
		t.Fatalf("新历史首条应为摘要 user 消息: %+v", res.Messages[0])
	}
	if !strings.Contains(res.Messages[0].Blocks[0].Text, "Goal: ship it") {
		t.Fatalf("摘要内容缺失")
	}
	if sum.callCount() != 1 {
		t.Fatalf("摘要器调用次数 = %d, want 1", sum.callCount())
	}
	if sum.lastInstr != SummaryInstruction {
		t.Fatalf("摘要指令未使用固定五段式提示")
	}
	want := []string{string(CompactPhaseStart), "summarize", string(CompactPhaseDone)}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("顺序 = %v, want %v", order, want)
	}
}

func TestBeforeCallAutoDisabledForSmallWindow(t *testing.T) {
	sum := &fakeSummarizer{summary: "s"}
	c, err := NewCompactor(CompactorConfig{
		Summarizer: sum,
		Estimator:  fixedEstimator(10_000_000),
		Budget:     Budget{ContextWindow: 8192},
	})
	if err != nil {
		t.Fatal(err)
	}
	prog := &progressLog{}
	res, err := c.BeforeCall(stdctx.Background(), BeforeCallInput{Messages: toolRounds(4, "x")}, prog.fn())
	if err != nil {
		t.Fatal(err)
	}
	if res.Compacted || res.Micro || res.Info != nil {
		t.Fatalf("8k 窗口不应触发任何压缩: %+v", res)
	}
	if sum.callCount() != 0 || len(prog.phases()) != 0 {
		t.Fatalf("不应调用摘要器或上报进度")
	}
}

func TestAutoCompactKeepsLastUserAndToolPair(t *testing.T) {
	sum := &fakeSummarizer{summary: "S"}
	c, err := NewCompactor(CompactorConfig{
		Summarizer:      sum,
		Estimator:       fixedEstimator(100),
		Budget:          Budget{ContextWindow: 200000},
		KeepMinMessages: 3,
		KeepMinTokens:   100,
		KeepMaxTokens:   40000,
	})
	if err != nil {
		t.Fatal(err)
	}

	msgs := []llm.Message{
		userText("old question"),
		toolUseMessage("t1"),
		toolResultMessage("t1", "old result"),
		toolUseMessage("t2"),
		toolResultMessage("t2", "middle result"),
		assistantText("answer"),
		userText("latest question"),
	}
	info, out, err := c.AutoCompact(stdctx.Background(), msgs, "auto", nil)
	if err != nil {
		t.Fatalf("AutoCompact: %v", err)
	}
	if len(info.KeptMessages) == 0 {
		t.Fatal("应保留尾部消息")
	}
	if got := info.KeptMessages[0]; got.Role != llm.RoleAssistant || got.Blocks[0].Type != llm.BlockToolUse {
		t.Fatalf("起点落在 tool_result 时应前移到对应 assistant: %+v", info.KeptMessages[0])
	}
	if got := info.KeptMessages[len(info.KeptMessages)-1]; got.Role != llm.RoleUser || got.Blocks[0].Text != "latest question" {
		t.Fatalf("最后一条用户输入必须保留: %+v", got)
	}
	if out[0].Role != llm.RoleUser || !strings.Contains(out[0].Blocks[0].Text, compactSummaryPrefix) {
		t.Fatalf("输出首条应为摘要消息")
	}
	if len(out) != len(info.KeptMessages)+1 {
		t.Fatalf("输出长度 = %d, want %d", len(out), len(info.KeptMessages)+1)
	}
}

func TestAutoCompactCircuitBreaker(t *testing.T) {
	sum := &fakeSummarizer{err: errors.New("boom")}
	c, err := NewCompactor(CompactorConfig{
		Summarizer:      sum,
		Estimator:       fixedEstimator(100),
		Budget:          Budget{ContextWindow: 200000},
		KeepMinMessages: 1,
		KeepMinTokens:   1,
	})
	if err != nil {
		t.Fatal(err)
	}
	msgs := toolRounds(4, "result")

	for i := 1; i <= 2; i++ {
		if _, _, err := c.AutoCompact(stdctx.Background(), msgs, "auto", nil); err == nil {
			t.Fatalf("第 %d 次失败应返回错误", i)
		} else if errors.Is(err, ErrCompactionDisabled) {
			t.Fatalf("第 %d 次不应触发熔断", i)
		}
	}
	if _, _, err := c.AutoCompact(stdctx.Background(), msgs, "auto", nil); !errors.Is(err, ErrCompactionDisabled) {
		t.Fatalf("连续第 3 次失败应熔断，实际 %v", err)
	}
	if _, _, err := c.AutoCompact(stdctx.Background(), msgs, "auto", nil); !errors.Is(err, ErrCompactionDisabled) {
		t.Fatalf("熔断后应直接返回 ErrCompactionDisabled")
	}
	if sum.callCount() != 3 {
		t.Fatalf("熔断后不应再调用摘要器，调用次数 = %d", sum.callCount())
	}
}

func TestAutoCompactEmptySummaryCountsAsFailure(t *testing.T) {
	sum := &fakeSummarizer{summary: "   \n"}
	c, err := NewCompactor(CompactorConfig{
		Summarizer:      sum,
		Estimator:       fixedEstimator(100),
		Budget:          Budget{ContextWindow: 200000},
		KeepMinMessages: 1,
		KeepMinTokens:   1,
	})
	if err != nil {
		t.Fatal(err)
	}
	msgs := toolRounds(4, "result")
	if _, _, err := c.AutoCompact(stdctx.Background(), msgs, "auto", nil); !errors.Is(err, ErrEmptySummary) {
		t.Fatalf("空摘要应以 ErrEmptySummary 失败，实际 %v", err)
	}
	for i := 0; i < 2; i++ {
		_, _, _ = c.AutoCompact(stdctx.Background(), msgs, "auto", nil)
	}
	if _, _, err := c.AutoCompact(stdctx.Background(), msgs, "auto", nil); !errors.Is(err, ErrCompactionDisabled) {
		t.Fatalf("空摘要连续失败后应熔断，实际 %v", err)
	}
}

func TestBeforeCallDegradesOnSummaryFailure(t *testing.T) {
	sum := &fakeSummarizer{err: errors.New("provider down")}
	c, err := NewCompactor(CompactorConfig{
		Summarizer: sum,
		Estimator:  fixedEstimator(190000),
		Budget:     Budget{ContextWindow: 200000},
	})
	if err != nil {
		t.Fatal(err)
	}
	msgs := toolRounds(4, "result")
	prog := &progressLog{}
	res, err := c.BeforeCall(stdctx.Background(), BeforeCallInput{Messages: msgs}, prog.fn())
	if err == nil {
		t.Fatal("摘要失败应返回错误供调用方记录")
	}
	if res.Compacted {
		t.Fatal("失败时不应标记为已压缩")
	}
	if !reflect.DeepEqual(res.Messages, msgs) {
		t.Fatal("失败时应返回原消息，保证调用可继续")
	}
	want := []CompactPhase{CompactPhaseStart, CompactPhaseFailed}
	if got := prog.phases(); !reflect.DeepEqual(got, want) {
		t.Fatalf("进度 = %v, want %v", got, want)
	}
	if prog.events[len(prog.events)-1].Err == "" {
		t.Fatal("Failed 阶段应带错误说明")
	}
}

func TestNewCompactorRequiresSummarizer(t *testing.T) {
	if _, err := NewCompactor(CompactorConfig{}); !errors.Is(err, ErrNoSummarizer) {
		t.Fatalf("err = %v, want ErrNoSummarizer", err)
	}
	c, err := NewCompactor(CompactorConfig{Summarizer: &fakeSummarizer{}, Budget: Budget{ContextWindow: 32768}})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Budget().AutoCompactThreshold(); got != 12288 {
		t.Fatalf("冻结预算阈值 = %d, want 12288", got)
	}
}

func TestAutoCompactRetriesAfterContextOverflow(t *testing.T) {
	sum := &trimmingSummarizer{firstErr: overflowError{}, summary: "S"}
	c, err := NewCompactor(CompactorConfig{
		Summarizer:      sum,
		Estimator:       fixedEstimator(100),
		Budget:          Budget{ContextWindow: 200000},
		KeepMinMessages: 1,
		KeepMinTokens:   1,
	})
	if err != nil {
		t.Fatal(err)
	}
	msgs := toolRounds(4, "result")
	info, out, err := c.AutoCompact(stdctx.Background(), msgs, "auto", nil)
	if err != nil {
		t.Fatalf("超窗重试后应成功: %v", err)
	}
	if sum.calls != 2 {
		t.Fatalf("摘要调用次数 = %d, want 2", sum.calls)
	}
	if got := len(sum.seen[0]); got != len(msgs) {
		t.Fatalf("首次摘要应使用完整历史: %d", got)
	}
	if got := len(sum.seen[1]); got != len(msgs)-2 {
		t.Fatalf("重试应删掉最旧一组（assistant + tool_result）: %d", got)
	}
	if info.PreTokens != 100 {
		t.Fatalf("PreTokens 应记录压缩前用量")
	}
	if out[0].Blocks[0].Text != compactSummaryPrefix+"S" {
		t.Fatalf("摘要消息错误: %q", out[0].Blocks[0].Text)
	}
}

// overflowError 是实现了 ContextOverflow() bool 的摘要错误。
type overflowError struct{}

func (overflowError) Error() string         { return "context overflow" }
func (overflowError) ContextOverflow() bool { return true }

// trimmingSummarizer 首次返回超窗错误，之后返回成功摘要，并记录每次看到的消息数。
type trimmingSummarizer struct {
	firstErr error
	summary  string
	calls    int
	seen     [][]llm.Message
}

func (s *trimmingSummarizer) Summarize(_ stdctx.Context, msgs []llm.Message, _ string) (string, error) {
	s.calls++
	s.seen = append(s.seen, msgs)
	if s.calls == 1 {
		return "", s.firstErr
	}
	return s.summary, nil
}

// fakeProvider 记录请求并返回脚本化文本流。
type fakeProvider struct {
	mu   sync.Mutex
	reqs []llm.Request
}

func (p *fakeProvider) Name() string { return "fake" }

func (p *fakeProvider) Stream(_ stdctx.Context, req llm.Request) (llm.Stream, error) {
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	p.mu.Unlock()
	return &fakeStream{events: []llm.StreamEvent{
		{Type: llm.EventTextDelta, Text: "Goal: "},
		{Type: llm.EventTextDelta, Text: "done"},
	}}, nil
}

type fakeStream struct {
	events []llm.StreamEvent
}

func (s *fakeStream) Recv(stdctx.Context) (llm.StreamEvent, error) {
	if len(s.events) == 0 {
		return llm.StreamEvent{}, io.EOF
	}
	ev := s.events[0]
	s.events = s.events[1:]
	return ev, nil
}

func (s *fakeStream) Close() error { return nil }

func TestProviderSummarizer(t *testing.T) {
	p := &fakeProvider{}
	s := NewProviderSummarizer(p, "model-x", 0)

	var deltas []string
	summary, err := s.SummarizeStream(stdctx.Background(), []llm.Message{userText("hi")}, SummaryInstruction, func(d string) {
		deltas = append(deltas, d)
	})
	if err != nil {
		t.Fatalf("SummarizeStream: %v", err)
	}
	if summary != "Goal: done" {
		t.Fatalf("summary = %q", summary)
	}
	if !reflect.DeepEqual(deltas, []string{"Goal: ", "done"}) {
		t.Fatalf("deltas = %v", deltas)
	}
	if len(p.reqs) != 1 {
		t.Fatalf("请求数 = %d", len(p.reqs))
	}
	req := p.reqs[0]
	if req.Model != "model-x" || req.System != SummaryInstruction || req.MaxTokens != defaultSummaryMaxTokens {
		t.Fatalf("摘要请求字段错误: %+v", req)
	}
	if len(req.Tools) != 0 {
		t.Fatalf("摘要请求不应带工具")
	}
}

// overflowThenOKProvider 第一次流返回真实的 *llm.StreamError 超窗错误，之后返回正常摘要。
type overflowThenOKProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *overflowThenOKProvider) Name() string { return "overflow-then-ok" }

func (p *overflowThenOKProvider) Stream(_ stdctx.Context, _ llm.Request) (llm.Stream, error) {
	p.mu.Lock()
	p.calls++
	n := p.calls
	p.mu.Unlock()
	if n == 1 {
		return &errorStream{err: &llm.StreamError{
			Kind:     llm.ErrKindContextOverflow,
			Message:  "prompt is too long",
			Status:   400,
			Provider: "fake",
		}}, nil
	}
	return &fakeStream{events: []llm.StreamEvent{{Type: llm.EventTextDelta, Text: "S"}}}, nil
}

func (p *overflowThenOKProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// errorStream 在一次 Recv 中返回固定错误。
type errorStream struct{ err error }

func (s *errorStream) Recv(stdctx.Context) (llm.StreamEvent, error) {
	return llm.StreamEvent{}, s.err
}

func (s *errorStream) Close() error { return nil }

// TestAutoCompactRetriesOnRealStreamErrorOverflow 验证生产路径：
// Provider 返回 Kind=ErrKindContextOverflow 的 *llm.StreamError 时，
// AutoCompact 删除最旧一组后重试一次，而不是直接失败。
func TestAutoCompactRetriesOnRealStreamErrorOverflow(t *testing.T) {
	p := &overflowThenOKProvider{}
	c, err := NewCompactor(CompactorConfig{
		Provider:        p,
		Estimator:       fixedEstimator(100),
		Budget:          Budget{ContextWindow: 200000},
		KeepMinMessages: 1,
		KeepMinTokens:   1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, out, err := c.AutoCompact(stdctx.Background(), toolRounds(4, "result"), "auto", nil)
	if err != nil {
		t.Fatalf("超窗后应重试并成功: %v", err)
	}
	if got := p.callCount(); got != 2 {
		t.Fatalf("provider Stream calls = %d, want 2", got)
	}
	if len(out) == 0 || out[0].Blocks[0].Text != compactSummaryPrefix+"S" {
		t.Fatalf("摘要消息错误: %+v", out)
	}
}
