package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sysctx "github.com/Baike12/b-desktop/internal/context"
	"github.com/Baike12/b-desktop/internal/llm"
	"github.com/Baike12/b-desktop/internal/permission"
	"github.com/Baike12/b-desktop/internal/session"
	"github.com/Baike12/b-desktop/internal/tool"
)

// fixedNow 是测试用的固定时间源。
func fixedNow() time.Time { return time.Unix(1700000000, 0) }

// testBuilder 创建不触盘、不调用 git 的系统提示组装器。
func testBuilder(workDir string) *sysctx.Builder {
	return sysctx.NewBuilder(sysctx.Options{
		WorkDir: workDir,
		Model:   "test-model",
		Now:     fixedNow,
		ReadFile: func(string) ([]byte, error) {
			return nil, os.ErrNotExist
		},
		RunGit: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("no git")
		},
	})
}

// fakeStep 是一次 Stream 调用的脚本。
type fakeStep struct {
	events []llm.StreamEvent
	err    error
}

// fakeProvider 按队列产出脚本化响应。
type fakeProvider struct {
	mu    sync.Mutex
	steps []fakeStep
	calls []llm.Request
}

// newFakeProvider 创建脚本化 Provider。
func newFakeProvider(steps ...fakeStep) *fakeProvider {
	return &fakeProvider{steps: steps}
}

// Name 返回 "fake"。
func (p *fakeProvider) Name() string { return "fake" }

// Stream 实现 llm.Provider。
func (p *fakeProvider) Stream(_ context.Context, req llm.Request) (llm.Stream, error) {
	p.mu.Lock()
	p.calls = append(p.calls, req)
	var step fakeStep
	if len(p.steps) > 0 {
		step = p.steps[0]
		p.steps = p.steps[1:]
	}
	p.mu.Unlock()
	return &fakeStream{events: step.events, err: step.err}, nil
}

// CallCount 返回已发生的模型调用次数。
func (p *fakeProvider) CallCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

// Calls 返回请求快照。
func (p *fakeProvider) Calls() []llm.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]llm.Request, len(p.calls))
	copy(out, p.calls)
	return out
}

// fakeStream 逐个返回脚本事件，结束时返回 err 或 io.EOF。
type fakeStream struct {
	events []llm.StreamEvent
	err    error
	i      int
}

// Recv 实现 llm.Stream；ctx 取消时立即返回取消错误。
func (s *fakeStream) Recv(ctx context.Context) (llm.StreamEvent, error) {
	if err := ctx.Err(); err != nil {
		return llm.StreamEvent{}, err
	}
	if s.i < len(s.events) {
		ev := s.events[s.i]
		s.i++
		return ev, nil
	}
	if s.err != nil {
		return llm.StreamEvent{}, s.err
	}
	return llm.StreamEvent{}, io.EOF
}

// Close 实现 llm.Stream。
func (s *fakeStream) Close() error { return nil }

// fakeTool 是可编程的 tool.Tool。
type fakeTool struct {
	name     string
	readOnly bool
	safe     bool
	runFn    func(ctx context.Context, input json.RawMessage) (tool.Result, error)

	mu    sync.Mutex
	calls int
}

// newFakeTool 创建 fake 工具。
func newFakeTool(name string, readOnly, safe bool) *fakeTool {
	return &fakeTool{name: name, readOnly: readOnly, safe: safe}
}

// Name 返回工具名。
func (f *fakeTool) Name() string { return f.name }

// Description 返回工具说明。
func (f *fakeTool) Description() string { return f.name + " tool" }

// InputSchema 返回固定 schema。
func (f *fakeTool) InputSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }

// ReadOnly 返回配置的只读标记。
func (f *fakeTool) ReadOnly(json.RawMessage) bool { return f.readOnly }

// ConcurrencySafe 返回配置的并发安全标记。
func (f *fakeTool) ConcurrencySafe(json.RawMessage) bool { return f.safe }

// Run 执行可编程行为，默认返回成功文本。
func (f *fakeTool) Run(ctx context.Context, input json.RawMessage, _ tool.ProgressFunc) (tool.Result, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.runFn != nil {
		return f.runFn(ctx, input)
	}
	return tool.Result{Content: f.name + " ok"}, nil
}

// callCount 返回执行次数。
func (f *fakeTool) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeSummarizer 返回固定摘要。
type fakeSummarizer struct {
	summary string
	err     error
}

// Summarize 实现 sysctx.Summarizer。
func (s fakeSummarizer) Summarize(context.Context, []llm.Message, string) (string, error) {
	return s.summary, s.err
}

// concurrencyTracker 记录同时运行的调用峰值。
type concurrencyTracker struct {
	cur int32
	max int32
}

func (c *concurrencyTracker) enter() {
	n := atomic.AddInt32(&c.cur, 1)
	for {
		m := atomic.LoadInt32(&c.max)
		if n <= m || atomic.CompareAndSwapInt32(&c.max, m, n) {
			return
		}
	}
}

func (c *concurrencyTracker) exit() { atomic.AddInt32(&c.cur, -1) }

func (c *concurrencyTracker) peak() int32 { return atomic.LoadInt32(&c.max) }

// recorder 收集事件并提供按类型等待。
type recorder struct {
	mu     sync.Mutex
	events []Event
	notify chan Event
}

func newRecorder() *recorder { return &recorder{notify: make(chan Event, 4096)} }

func (r *recorder) sink(ev Event) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
	select {
	case r.notify <- ev:
	default:
	}
}

func (r *recorder) snapshot() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Event, len(r.events))
	copy(out, r.events)
	return out
}

func (r *recorder) waitFor(t *testing.T, typ EventType, timeout time.Duration) Event {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev := <-r.notify:
			if ev.Type == typ {
				return ev
			}
		case <-deadline:
			t.Fatalf("timeout waiting for event %s", typ)
			return Event{}
		}
	}
}

func (r *recorder) count(typ EventType) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, ev := range r.events {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

// harness 是一轮端到端测试的装配。
type harness struct {
	t           *testing.T
	dir         string
	provider    *fakeProvider
	store       *session.MemStore
	permissions *permission.Engine
	approvals   *Approvals
	builder     *sysctx.Builder
	compactor   *sysctx.Compactor
	engine      *Engine
}

// harnessOptions 是 harness 的可调参数。
type harnessOptions struct {
	mode           permission.Mode
	tools          []tool.Tool
	steps          []fakeStep
	maxTurns       int
	maxConcurrency int
	budget         sysctx.Budget
	summarizer     sysctx.Summarizer
	extensions     Extensions
}

// idGen 生成稳定可读的 ID。
type idGen struct {
	mu sync.Mutex
	n  int
}

func (g *idGen) next(prefix string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	return fmt.Sprintf("%s_%d", prefix, g.n)
}

func newHarness(t *testing.T, opts harnessOptions) *harness {
	t.Helper()
	dir := t.TempDir()
	if opts.mode == "" {
		opts.mode = permission.ModeBypassPermissions
	}
	registry, err := NewRegistry(opts.tools...)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	perms, err := permission.NewEngine(permission.Config{Mode: opts.mode, WorkDir: dir})
	if err != nil {
		t.Fatalf("NewEngine(permission): %v", err)
	}
	store := session.NewMemStore()
	if err := store.CreateSession(context.Background(), session.Meta{ID: "s1", WorkDir: dir}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	provider := newFakeProvider(opts.steps...)
	budget := opts.budget
	if budget.ContextWindow == 0 {
		budget = sysctx.Budget{ContextWindow: 200000, MaxOutputTokens: 8192}
	}
	summarizer := opts.summarizer
	if summarizer == nil {
		summarizer = fakeSummarizer{summary: "compacted summary"}
	}
	compactor, err := sysctx.NewCompactor(sysctx.CompactorConfig{
		Provider:   provider,
		Model:      "test-model",
		Summarizer: summarizer,
		Budget:     sysctx.NewBudget(budget),
		Now:        fixedNow,
	})
	if err != nil {
		t.Fatalf("NewCompactor: %v", err)
	}
	approvals := NewApprovals()
	ids := &idGen{}
	engine, err := NewEngine(Config{
		SessionID:          "s1",
		WorkDir:            dir,
		Provider:           provider,
		Model:              "test-model",
		MaxTurns:           opts.maxTurns,
		MaxToolConcurrency: opts.maxConcurrency,
		Tools:              registry,
		Permissions:        perms,
		Approvals:          approvals,
		Builder:            testBuilder(dir),
		Compactor:          compactor,
		Store:              store,
		Now:                fixedNow,
		NewID:              ids.next,
		Extensions:         opts.extensions,
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return &harness{
		t:           t,
		dir:         dir,
		provider:    provider,
		store:       store,
		permissions: perms,
		approvals:   approvals,
		builder:     testBuilder(dir),
		compactor:   compactor,
		engine:      engine,
	}
}

// textStep 构造一段纯文本响应；每个 delta 单独产出，便于断言逐条转发。
func textStep(messageID string, deltas ...string) fakeStep {
	events := []llm.StreamEvent{
		{Type: llm.EventMessageStart, MessageID: messageID},
		{Type: llm.EventBlockStart, Index: 0, Kind: llm.BlockText},
	}
	for _, d := range deltas {
		events = append(events, llm.StreamEvent{Type: llm.EventTextDelta, Index: 0, Kind: llm.BlockText, Text: d})
	}
	events = append(events,
		llm.StreamEvent{Type: llm.EventMessageDelta, StopReason: llm.StopEndTurn, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 2}},
		llm.StreamEvent{Type: llm.EventMessageStop},
	)
	return fakeStep{events: events}
}

// plannedCall 描述脚本里的一个工具调用。
type plannedCall struct {
	id    string
	name  string
	input string
}

// toolUseStep 构造只包含工具调用的响应。
func toolUseStep(messageID string, calls ...plannedCall) fakeStep {
	events := []llm.StreamEvent{{Type: llm.EventMessageStart, MessageID: messageID}}
	for i, c := range calls {
		events = append(events,
			llm.StreamEvent{Type: llm.EventToolUseStart, Index: i, Kind: llm.BlockToolUse, ToolUseID: c.id, ToolName: c.name},
			llm.StreamEvent{Type: llm.EventToolInputDelta, Index: i, Kind: llm.BlockToolUse, PartialJSON: c.input},
			llm.StreamEvent{Type: llm.EventToolUseStop, Index: i, Kind: llm.BlockToolUse},
		)
	}
	events = append(events,
		llm.StreamEvent{Type: llm.EventMessageDelta, StopReason: llm.StopToolUse, Usage: &llm.Usage{InputTokens: 20, OutputTokens: 5}},
		llm.StreamEvent{Type: llm.EventMessageStop},
	)
	return fakeStep{events: events}
}

// runResult 是异步 Run 的结果。
type runResult struct {
	res Result
	err error
}

// runAsync 在 goroutine 中执行一轮，便于审批与取消测试。
func runAsync(t *testing.T, h *harness, ctx context.Context, rec *recorder, in Input) <-chan runResult {
	t.Helper()
	ch := make(chan runResult, 1)
	go func() {
		res, err := h.engine.Run(ctx, in, rec.sink)
		ch <- runResult{res: res, err: err}
	}()
	return ch
}

// awaitRun 等待 Run 结束，超时则失败。
func awaitRun(t *testing.T, ch <-chan runResult, timeout time.Duration) runResult {
	t.Helper()
	select {
	case out := <-ch:
		if out.err != nil {
			t.Fatalf("Run error: %v", out.err)
		}
		return out
	case <-time.After(timeout):
		t.Fatal("timeout waiting for Run to finish")
		return runResult{}
	}
}

// indexOf 返回某类事件第一次出现的下标，找不到返回 -1。
func indexOf(events []Event, typ EventType) int {
	for i, ev := range events {
		if ev.Type == typ {
			return i
		}
	}
	return -1
}

// indexOfFrom 从 from 开始查找某类事件。
func indexOfFrom(events []Event, typ EventType, from int) int {
	for i := from; i < len(events); i++ {
		if events[i].Type == typ {
			return i
		}
	}
	return -1
}

// historyBlockTexts 收集历史中指定类型的块文本。
func historyBlockTexts(msgs []llm.Message, typ llm.BlockType) []string {
	var out []string
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == typ {
				out = append(out, b.Text)
			}
		}
	}
	return out
}

// loadRecords 读取会话记录。
func loadRecords(t *testing.T, store session.Store, id string) []session.Record {
	t.Helper()
	recs, err := store.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return recs
}
