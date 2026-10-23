package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Baike12/b-desktop/internal/agent"
	"github.com/Baike12/b-desktop/internal/llm"
	"github.com/Baike12/b-desktop/internal/permission"
	"github.com/Baike12/b-desktop/internal/tool"
)

// scriptStep 是一次 Stream 调用的脚本。
type scriptStep struct {
	events []llm.StreamEvent
	err    error
	block  bool
}

// fakeProvider 按队列产出脚本化响应，并记录每次请求。
type fakeProvider struct {
	mu    sync.Mutex
	steps []scriptStep
	calls []llm.Request
}

// newFakeProvider 创建脚本化 Provider。
func newFakeProvider(steps ...scriptStep) *fakeProvider {
	return &fakeProvider{steps: steps}
}

// Name 返回 "fake"。
func (p *fakeProvider) Name() string { return "fake" }

// Stream 实现 llm.Provider。
func (p *fakeProvider) Stream(_ context.Context, req llm.Request) (llm.Stream, error) {
	p.mu.Lock()
	p.calls = append(p.calls, req)
	var step scriptStep
	if len(p.steps) > 0 {
		step = p.steps[0]
		p.steps = p.steps[1:]
	}
	p.mu.Unlock()
	return &fakeStream{step: step}, nil
}

// Calls 返回请求快照。
func (p *fakeProvider) Calls() []llm.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]llm.Request, len(p.calls))
	copy(out, p.calls)
	return out
}

// CallCount 返回已发生的模型调用次数。
func (p *fakeProvider) CallCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

// fakeStream 逐个返回脚本事件；block 为 true 时等待 ctx 取消。
type fakeStream struct {
	step scriptStep
	i    int
}

// Recv 实现 llm.Stream。
func (s *fakeStream) Recv(ctx context.Context) (llm.StreamEvent, error) {
	if s.step.block {
		<-ctx.Done()
		return llm.StreamEvent{}, ctx.Err()
	}
	if s.i < len(s.step.events) {
		ev := s.step.events[s.i]
		s.i++
		return ev, nil
	}
	if s.step.err != nil {
		return llm.StreamEvent{}, s.step.err
	}
	return llm.StreamEvent{}, io.EOF
}

// Close 实现 llm.Stream。
func (s *fakeStream) Close() error { return nil }

// recorder 收集转发给父会话的事件。
type recorder struct {
	mu     sync.Mutex
	events []agent.Event
}

// sink 实现 agent.EventSink。
func (r *recorder) sink(ev agent.Event) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

// snapshot 返回事件快照。
func (r *recorder) snapshot() []agent.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]agent.Event, len(r.events))
	copy(out, r.events)
	return out
}

// count 统计某类事件的数量。
func (r *recorder) count(typ agent.EventType) int {
	n := 0
	for _, ev := range r.snapshot() {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

// textStep 构造一段纯文本响应。
func textStep(messageID string, deltas ...string) scriptStep {
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
	return scriptStep{events: events}
}

// plannedCall 描述脚本里的一个工具调用。
type plannedCall struct {
	id    string
	name  string
	input string
}

// toolUseStep 构造只包含工具调用的响应。
func toolUseStep(messageID string, calls ...plannedCall) scriptStep {
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
	return scriptStep{events: events}
}

// newTestManager 创建使用 fake Provider、给定工作目录的 Manager。
func newTestManager(t *testing.T, workDir, agentsDir string, provider llm.Provider, mode permission.Mode) *Manager {
	t.Helper()
	if mode == "" {
		mode = permission.ModeBypassPermissions
	}
	mgr, err := NewManager(Config{
		WorkDir:   workDir,
		AgentsDir: agentsDir,
		Runtime: func(string) (Runtime, error) {
			return Runtime{
				Provider:        provider,
				Model:           "test-model",
				WorkDir:         workDir,
				PermissionMode:  mode,
				ContextWindow:   200000,
				MaxOutputTokens: 8192,
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	return mgr
}

// writeAgentFile 写入一个子 Agent 定义。
func writeAgentFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// TestRunSyncIsolatesHistoryAndAggregatesText 验证同步子 Agent 使用独立历史、
// 加载 agent_type 正文作为系统提示，并把最终文本汇总回调用方。
func TestRunSyncIsolatesHistoryAndAggregatesText(t *testing.T) {
	dir := t.TempDir()
	agentsDir := filepath.Join(dir, "agents")
	writeAgentFile(t, agentsDir, "reviewer.md", strings.Join([]string{
		"---",
		"name: reviewer",
		"description: 严格的代码评审员",
		"---",
		"你是严格的代码评审员，只报告确定的问题。",
	}, "\n"))

	provider := newFakeProvider(textStep("m1", "已完成", "评审"))
	mgr := newTestManager(t, dir, agentsDir, provider, "")
	rec := &recorder{}

	res, err := mgr.Run(context.Background(), agent.SubagentRequest{
		SessionID:   "parent",
		Description: "评审主文件",
		Prompt:      "分析 main.go",
		AgentType:   "reviewer",
	}, rec.sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ID == "" {
		t.Fatal("Run returned empty task ID")
	}
	if res.Text != "已完成评审" {
		t.Fatalf("Text = %q, want %q", res.Text, "已完成评审")
	}
	if res.Error != "" {
		t.Fatalf("Error = %q, want empty", res.Error)
	}
	if res.Turns != 1 {
		t.Fatalf("Turns = %d, want 1", res.Turns)
	}
	if res.Usage.InputTokens != 10 || res.Usage.OutputTokens != 2 {
		t.Fatalf("Usage = %+v, want input 10 output 2", res.Usage)
	}

	calls := provider.Calls()
	if len(calls) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(calls))
	}
	if !strings.Contains(calls[0].System, "你是严格的代码评审员") {
		t.Fatalf("system prompt missing agent type body:\n%s", calls[0].System)
	}
	if len(calls[0].Messages) != 1 {
		t.Fatalf("child history length = %d, want 1 (independent of parent)", len(calls[0].Messages))
	}
	if got := calls[0].Messages[0].Blocks[0].Text; got != "分析 main.go" {
		t.Fatalf("child prompt = %q, want %q", got, "分析 main.go")
	}

	task, ok := mgr.Task(res.ID)
	if !ok {
		t.Fatalf("task %s not found", res.ID)
	}
	if task.Status != TaskCompleted {
		t.Fatalf("task status = %s, want %s", task.Status, TaskCompleted)
	}
	if task.Result != "已完成评审" {
		t.Fatalf("task result = %q, want %q", task.Result, "已完成评审")
	}
	if task.SessionID != "parent" || task.Description != "评审主文件" || task.AgentType != "reviewer" {
		t.Fatalf("task metadata = %+v", task)
	}
	if task.FinishedAt.IsZero() {
		t.Fatal("task FinishedAt is zero")
	}
	if rec.count(agent.EventTurnStart) != 1 || rec.count(agent.EventTurnEnd) != 1 {
		t.Fatalf("forwarded events = %d turn_start, %d turn_end; want 1/1",
			rec.count(agent.EventTurnStart), rec.count(agent.EventTurnEnd))
	}
}

// TestRunBackgroundCanBeCanceled 验证后台任务立即返回、可取消并留存结果。
func TestRunBackgroundCanBeCanceled(t *testing.T) {
	dir := t.TempDir()
	provider := newFakeProvider(scriptStep{block: true})
	mgr := newTestManager(t, dir, filepath.Join(dir, "agents"), provider, "")

	res, err := mgr.Run(context.Background(), agent.SubagentRequest{
		SessionID:   "parent",
		Description: "后台任务",
		Prompt:      "一直等待",
		Background:  true,
	}, nil)
	if err != nil {
		t.Fatalf("Run background: %v", err)
	}
	if res.ID == "" {
		t.Fatal("background Run returned empty task ID")
	}

	waitFor(t, 2*time.Second, func() bool { return provider.CallCount() == 1 }, "background model call")
	if !mgr.CancelTask(res.ID) {
		t.Fatalf("CancelTask(%q) = false", res.ID)
	}
	waitFor(t, 2*time.Second, func() bool {
		task, ok := mgr.Task(res.ID)
		return ok && task.Status == TaskCanceled
	}, "task canceled")

	task, _ := mgr.Task(res.ID)
	if task.FinishedAt.IsZero() {
		t.Fatal("canceled task has zero FinishedAt")
	}
	if !task.Background {
		t.Fatal("task Background = false, want true")
	}
	if mgr.CancelTask("missing") {
		t.Fatal("CancelTask(unknown) = true, want false")
	}
}

// TestAgentToolDecoration 验证 Agent 工具的名称、schema 与结果封装。
func TestAgentToolDecoration(t *testing.T) {
	dir := t.TempDir()
	provider := newFakeProvider(
		textStep("m1", "工具结果"),
		textStep("m2", "后台结果"),
	)
	mgr := newTestManager(t, dir, filepath.Join(dir, "agents"), provider, "")
	var tl tool.Tool = NewAgentTool(mgr)

	if tl.Name() != ToolName {
		t.Fatalf("tool name = %q, want %q", tl.Name(), ToolName)
	}
	if tl.ReadOnly(nil) {
		t.Fatal("Agent tool must not be read-only")
	}
	if tl.ConcurrencySafe(nil) {
		t.Fatal("Agent tool must not be concurrency safe")
	}
	var schema map[string]any
	if err := json.Unmarshal(tl.InputSchema(), &schema); err != nil {
		t.Fatalf("schema is not JSON: %v", err)
	}
	props := schema["properties"].(map[string]any)
	for _, key := range []string{"description", "prompt", "agent_type", "background"} {
		if _, ok := props[key]; !ok {
			t.Fatalf("schema missing property %q", key)
		}
	}

	out, err := tl.Run(context.Background(), json.RawMessage(`{"description":"执行任务","prompt":"做点事"}`), nil)
	if err != nil {
		t.Fatalf("tool Run: %v", err)
	}
	if out.Content != "工具结果" || out.IsError {
		t.Fatalf("tool result = %+v", out)
	}
	if out.Meta["taskId"] == "" {
		t.Fatalf("tool result meta missing taskId: %+v", out.Meta)
	}

	bg, err := tl.Run(context.Background(), json.RawMessage(`{"description":"后台任务","prompt":"后台","background":true}`), nil)
	if err != nil {
		t.Fatalf("tool Run background: %v", err)
	}
	if bg.Meta["status"] != string(TaskRunning) {
		t.Fatalf("background meta status = %q, want %q", bg.Meta["status"], TaskRunning)
	}
	waitFor(t, 2*time.Second, func() bool {
		task, ok := mgr.Task(bg.Meta["taskId"])
		return ok && task.Status == TaskCompleted
	}, "background task completed")

	if _, err := tl.Run(context.Background(), json.RawMessage(`{"description":"x","prompt":""}`), nil); !errors.Is(err, ErrEmptyPrompt) {
		t.Fatalf("empty prompt error = %v, want ErrEmptyPrompt", err)
	}
	if _, err := tl.Run(context.Background(), json.RawMessage(`{"description":"x","prompt":"y","agent_type":"nope"}`), nil); !errors.Is(err, ErrUnknownAgentType) {
		t.Fatalf("unknown type error = %v, want ErrUnknownAgentType", err)
	}
}

// TestRunAutoDeniesApproval 验证子 Agent 无法交互时把 ask 判定按拒绝执行，
// 且审批事件不会转发给父会话。
func TestRunAutoDeniesApproval(t *testing.T) {
	dir := t.TempDir()
	provider := newFakeProvider(
		toolUseStep("m1", plannedCall{id: "t1", name: "Write", input: `{"file_path":"x.txt","content":"hi"}`}),
		textStep("m2", "写入被拒绝"),
	)
	mgr := newTestManager(t, dir, filepath.Join(dir, "agents"), provider, permission.ModeDefault)
	rec := &recorder{}

	res, err := mgr.Run(context.Background(), agent.SubagentRequest{
		SessionID:   "parent",
		Description: "尝试写入",
		Prompt:      "写文件",
	}, rec.sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Text != "写入被拒绝" || res.Error != "" {
		t.Fatalf("result = %+v", res)
	}
	if rec.count(agent.EventPermissionRequest) != 0 {
		t.Fatalf("permission_request forwarded to parent: %d", rec.count(agent.EventPermissionRequest))
	}

	calls := provider.Calls()
	if len(calls) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(calls))
	}
	var denied bool
	for _, msg := range calls[1].Messages {
		for _, block := range msg.Blocks {
			if block.Type == llm.BlockToolResult && block.ToolUseID == "t1" {
				denied = block.IsError && strings.Contains(block.Text, "permission denied")
			}
		}
	}
	if !denied {
		t.Fatalf("write tool result was not denied: %+v", calls[1].Messages)
	}
	if _, err := os.Stat(filepath.Join(dir, "x.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("x.txt should not exist after denial, stat err = %v", err)
	}
}

// TestManagerClosedRejectsRun 验证 Close 之后 Run 返回 ErrClosed。
func TestManagerClosedRejectsRun(t *testing.T) {
	dir := t.TempDir()
	provider := newFakeProvider(textStep("m1", "ok"))
	mgr := newTestManager(t, dir, filepath.Join(dir, "agents"), provider, "")
	if err := mgr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err := mgr.Run(context.Background(), agent.SubagentRequest{
		Description: "x",
		Prompt:      "y",
	}, nil)
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("Run after Close error = %v, want ErrClosed", err)
	}
}

// agentRequest 构造一个最小的子 Agent 请求。
func agentRequest(sessionID string) agent.SubagentRequest {
	return agent.SubagentRequest{SessionID: sessionID, Description: "任务", Prompt: "执行"}
}

// waitFor 在超时前轮询条件。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}
