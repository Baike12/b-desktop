package memory

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Baike12/b-desktop/internal/agent"
	"github.com/Baike12/b-desktop/internal/llm"
)

// fakeStream 是一次性返回预设文本的 llm.Stream。
type fakeStream struct {
	events []llm.StreamEvent
	done   bool
}

// Recv 依次返回事件，耗尽后返回 io.EOF。
func (s *fakeStream) Recv(ctx context.Context) (llm.StreamEvent, error) {
	if s.done {
		return llm.StreamEvent{}, io.EOF
	}
	s.done = true
	for _, ev := range s.events {
		return ev, nil
	}
	return llm.StreamEvent{}, io.EOF
}

// Close 释放流；fake 实现无资源。
func (s *fakeStream) Close() error { return nil }

// fakeProvider 返回预设响应或错误，可阻塞等待释放信号。
type fakeProvider struct {
	mu       sync.Mutex
	response string
	err      error
	release  chan struct{}
	started  chan struct{}
	calls    int
}

// Name 返回供应商标识。
func (f *fakeProvider) Name() string { return "fake" }

// Stream 记录调用次数并返回预设流。
func (f *fakeProvider) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	f.mu.Lock()
	f.calls++
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	response, err, release := f.response, f.err, f.release
	f.mu.Unlock()
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	return &fakeStream{events: []llm.StreamEvent{
		{Type: llm.EventTextDelta, Text: response},
	}}, nil
}

// callCount 返回已被调用的次数。
func (f *fakeProvider) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// blockedStream 是等待释放信号的流，用于测试合并行为。
func newBlockingStream() *blockingStream { return &blockingStream{release: make(chan struct{})} }

type blockingStream struct {
	release chan struct{}
	closed  bool
	mu      sync.Mutex
}

// Recv 阻塞直到释放或取消。
func (s *blockingStream) Recv(ctx context.Context) (llm.StreamEvent, error) {
	select {
	case <-s.release:
		return llm.StreamEvent{}, io.EOF
	case <-ctx.Done():
		return llm.StreamEvent{}, ctx.Err()
	}
}

// Close 释放流并唤醒等待者。
func (s *blockingStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.release)
	}
	return nil
}

// blockingProvider 让第一个调用阻塞在 Recv，直到测试放行。
type blockingProvider struct {
	stream  *blockingStream
	started chan struct{}
	mu      sync.Mutex
	calls   int
}

// Name 返回供应商标识。
func (p *blockingProvider) Name() string { return "blocking" }

// Stream 计数并返回共享的阻塞流；首次调用发 started 信号。
func (p *blockingProvider) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	if p.started != nil {
		select {
		case p.started <- struct{}{}:
		default:
		}
	}
	return p.stream, nil
}

// callCount 返回已被调用的次数。
func (p *blockingProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func newTestExtractor(t *testing.T, provider llm.Provider, opts ...ExtractorOption) (*Extractor, *Hub, string) {
	t.Helper()
	hub, err := NewHub(filepath.Join(t.TempDir(), "memory"))
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	workDir := t.TempDir()
	opts = append(opts, WithMinInterval(0))
	e := NewExtractor(hub, provider, "test-model", opts...)
	return e, hub, workDir
}

func turnEventIn(workDir, prompt, reply string) agent.TurnInfo {
	return agent.TurnInfo{
		SessionID:  "sess-1",
		TurnID:     "turn-1",
		WorkDir:    workDir,
		Prompt:     prompt,
		FinalText:  reply,
		StopReason: agent.StopCompleted,
	}
}

// turnEvent 使用与断言无关的固定工作目录。
func turnEvent(prompt, reply string) agent.TurnInfo {
	return turnEventIn("/tmp/demo", prompt, reply)
}

func TestParseOpsHandlesFencesAndNoise(t *testing.T) {
	ops, err := parseOps("```json\n{\"ops\":[{\"op\":\"save\",\"name\":\"n\",\"content\":\"c\"}]}\n```")
	if err != nil {
		t.Fatalf("parseOps: %v", err)
	}
	if len(ops) != 1 || ops[0].Op != "save" || ops[0].Name != "n" {
		t.Fatalf("ops = %+v", ops)
	}
	if _, err := parseOps("模型不想说话"); err == nil {
		t.Fatal("expected error for non-JSON output")
	}
	if _, err := parseOps("{broken"); err == nil {
		t.Fatal("expected error for broken JSON")
	}
	ops, err = parseOps(`前缀 {"ops":[]} 后缀`)
	if err != nil || len(ops) != 0 {
		t.Fatalf("ops = %+v err=%v", ops, err)
	}
}

func TestExtractWorthyGates(t *testing.T) {
	cases := []struct {
		name string
		info agent.TurnInfo
		want bool
	}{
		{"completed", turnEvent("帮我修一下登录页面里的这个 bug", "done"), true},
		{"canceled", func() agent.TurnInfo {
			info := turnEvent("帮我修一下这个 bug", "done")
			info.StopReason = agent.StopCanceled
			return info
		}(), false},
		{"short prompt", turnEvent("好", "ok"), false},
		{"cjk runescape", turnEvent("修复登录页面的一个崩溃问题", ""), true},
		{"empty", turnEvent("  ", ""), false},
	}
	for _, tc := range cases {
		if got := extractWorthy(tc.info); got != tc.want {
			t.Fatalf("%s: extractWorthy = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestExtractorSavesMemories(t *testing.T) {
	response := `{"ops":[
		{"op":"save","scope":"project","type":"feedback","name":"测试策略","description":"先写测试","keywords":["测试"],"importance":8,"content":"该项目要求先写测试再实现。"},
		{"op":"save","scope":"user","type":"user","name":"语言","description":"用中文","importance":5,"content":"用户要求始终用中文交流。"}
	]}`
	provider := &fakeProvider{response: response}
	e, hub, workDir := newTestExtractor(t, provider)

	e.TurnComplete(turnEventIn(workDir, "这个项目怎么写测试？记得以后都这样", "好的，先写测试。"))
	e.Wait()

	projects, err := hub.List(ScopeProject, workDir)
	if err != nil || len(projects) != 1 || projects[0].Name != "测试策略" {
		t.Fatalf("projects = %+v err=%v", projects, err)
	}
	users, err := hub.List(ScopeUser, workDir)
	if err != nil || len(users) != 1 || users[0].Name != "语言" {
		t.Fatalf("users = %+v err=%v", users, err)
	}
	if users[0].Scope != ScopeUser || projects[0].Scope != ScopeProject {
		t.Fatalf("scope annotation wrong: %+v %+v", users[0], projects[0])
	}
	if provider.callCount() != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.callCount())
	}
}

func TestExtractorUpdatesAndDeletesByTarget(t *testing.T) {
	provider := &fakeProvider{response: `{"ops":[{"op":"save","scope":"user","type":"reference","name":"部署脚本","content":"deploy.sh 在仓库根目录。"}]}`}
	e, hub, workDir := newTestExtractor(t, provider)
	e.TurnComplete(turnEventIn(workDir, "请问部署脚本在哪里，我要手动部署一次", "在仓库根目录。"))
	e.Wait()

	provider.response = `{"ops":[
		{"op":"update","scope":"user","target":"部署脚本","content":"deploy.sh 移到了 scripts/ 目录。"},
		{"op":"delete","scope":"user","target":"不存在条目","reason":"过期"},
		{"op":"update","scope":"user","target":"missing-file.md","content":"x"}
	]}`
	e.TurnComplete(turnEventIn(workDir, "部署脚本移动位置了，之前那条记录没用了", "已移动到 scripts/。"))
	e.Wait()

	users, err := hub.List(ScopeUser, workDir)
	if err != nil || len(users) != 1 {
		t.Fatalf("users = %+v err=%v", users, err)
	}
	if users[0].Body != "deploy.sh 移到了 scripts/ 目录。" {
		t.Fatalf("body = %q", users[0].Body)
	}
}

func TestExtractorNotifiesChange(t *testing.T) {
	provider := &fakeProvider{response: `{"ops":[{"op":"save","scope":"user","name":"n1","content":"内容一。"}]}`}
	var mu sync.Mutex
	var got Change
	e, _, _ := newTestExtractor(t, provider, WithNotify(func(c Change) {
		mu.Lock()
		got = c
		mu.Unlock()
	}))
	e.TurnComplete(turnEvent("随便一条足够长的输入内容", "回复"))
	e.Wait()
	mu.Lock()
	defer mu.Unlock()
	if got.Saved != 1 || got.Err != "" {
		t.Fatalf("change = %+v", got)
	}
}

func TestExtractorReportsModelError(t *testing.T) {
	provider := &fakeProvider{err: errors.New("boom")}
	var mu sync.Mutex
	var got Change
	e, _, _ := newTestExtractor(t, provider, WithNotify(func(c Change) {
		mu.Lock()
		got = c
		mu.Unlock()
	}))
	e.TurnComplete(turnEvent("随便一条足够长的输入内容", "回复"))
	e.Wait()
	mu.Lock()
	defer mu.Unlock()
	if got.Err == "" || !strings.Contains(got.Err, "boom") {
		t.Fatalf("change = %+v, want boom error", got)
	}
}

func TestExtractorSkipsShortTurns(t *testing.T) {
	provider := &fakeProvider{response: `{"ops":[]}`}
	e, _, _ := newTestExtractor(t, provider)
	e.TurnComplete(turnEvent("好", "ok"))
	e.Wait()
	if provider.callCount() != 0 {
		t.Fatalf("provider calls = %d, want 0", provider.callCount())
	}
}

func TestExtractorRateLimitsPerSession(t *testing.T) {
	provider := &fakeProvider{response: `{"ops":[]}`}
	now := time.Unix(1_000_000, 0)
	e, _, _ := newTestExtractor(t, provider, WithMinInterval(DefaultMinInterval), WithNow(func() time.Time { return now }))
	// 显式给 Extractor 的 last 赋值走 TurnComplete 内部逻辑：第一次放行。
	e.TurnComplete(turnEvent("第一次足够长的输入", "回复"))
	e.Wait()
	first := provider.callCount()
	// 立刻再来一轮：仍在最小间隔内，必须跳过。
	e.TurnComplete(turnEvent("第二次足够长的输入", "回复"))
	e.Wait()
	if provider.callCount() != first {
		t.Fatalf("calls = %d, want %d (rate limit failed)", provider.callCount(), first)
	}
}

func TestExtractorCoalescesBusySessions(t *testing.T) {
	blocker := newBlockingStream()
	provider := &blockingProvider{stream: blocker, started: make(chan struct{}, 4)}
	e, hub, workDir := newTestExtractor(t, provider)

	// blockingProvider 无法换响应：给 hub 预置一条记忆验证合并轮次会跑即可。
	if _, _, err := hub.Save(context.Background(), ScopeUser, workDir, EntryInput{Name: "seed", Body: "seed"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	e.TurnComplete(turnEventIn(workDir, "第一条足够长的输入内容了吗", "回复"))
	<-provider.started
	e.TurnComplete(turnEventIn(workDir, "第二条足够长的输入内容了吗", "回复"))
	// 第一轮阻塞中，第二次进入 pending 而不是并发运行。
	if got := provider.callCount(); got != 1 {
		t.Fatalf("calls while blocked = %d, want 1", got)
	}
	blocker.Close()
	e.Wait()
	if got := provider.callCount(); got != 2 {
		t.Fatalf("calls after release = %d, want 2 (coalesced run)", got)
	}
	// 抽取全部结束后，合并轮次写入的种子记忆仍在。
	entries, err := hub.List(ScopeUser, workDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("entries after coalesce = %+v err=%v", entries, err)
	}
}

func TestExtractorGatesNilDependencies(t *testing.T) {
	e := NewExtractor(nil, nil, "")
	e.TurnComplete(turnEvent("任意足够长的输入", "回复"))
	e.Wait()
}
