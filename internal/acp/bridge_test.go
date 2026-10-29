package acp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Baike12/b-desktop/internal/agent"
	"github.com/Baike12/b-desktop/internal/permission"
	"github.com/Baike12/b-desktop/internal/session"
)

// fakeSession 是受控的 Session 实现：按脚本吐更新并返回结算。
type fakeSession struct {
	updates   []SessionUpdate
	result    PromptResult
	err       error
	prompted  [][]ContentBlock
	params    SessionParams
	modeSet   string
	cancelled bool
	closed    bool
	sink      UpdateSink
	client    Client
	// permissionRequest 非空时 Prompt 期间发起一次权限请求。
	permissionRequest *PermissionRequest
}

func (s *fakeSession) Prompt(ctx context.Context, blocks []ContentBlock) (PromptResult, error) {
	s.prompted = append(s.prompted, blocks)
	for _, update := range s.updates {
		s.sink(update)
	}
	// 权限请求在 Prompt 期间由后端发起（真实链路）。
	if s.permissionRequest != nil {
		if _, err := s.client.RequestPermission(ctx, *s.permissionRequest); err != nil {
			return PromptResult{}, err
		}
	}
	if s.err != nil {
		return PromptResult{}, s.err
	}
	return s.result, nil
}

func (s *fakeSession) Cancel() { s.cancelled = true }
func (s *fakeSession) SetMode(ctx context.Context, m string) error {
	s.modeSet = m
	return nil
}
func (s *fakeSession) Modes() SessionModes { return SessionModes{} }
func (s *fakeSession) Close() error        { s.closed = true; return nil }

// fakeBackend 返回预置会话。
type fakeBackend struct {
	session *fakeSession
	info    AgentInfo
}

func (b *fakeBackend) Info() AgentInfo { return b.info }

func (b *fakeBackend) NewSession(ctx context.Context, params SessionParams, sink UpdateSink, client Client) (Session, error) {
	b.session.sink = sink
	b.session.params = params
	b.session.client = client
	return b.session, nil
}

// memoryStore 是内存 transcript 存储，只实现测试需要的接口。
type memoryStore struct {
	records []session.Record
}

func (s *memoryStore) Append(ctx context.Context, rec session.Record) error {
	s.records = append(s.records, rec)
	return nil
}

// 以下方法为满足 session.Store 的编译占位（测试不触达）。
func (s *memoryStore) CreateSession(ctx context.Context, meta session.Meta) error { return nil }
func (s *memoryStore) Flush(ctx context.Context) error                            { return nil }
func (s *memoryStore) Load(ctx context.Context, sessionID string) ([]session.Record, error) {
	return nil, session.ErrSessionNotFound
}
func (s *memoryStore) RewindBefore(ctx context.Context, sessionID, messageID string) error {
	return nil
}
func (s *memoryStore) ForkSession(ctx context.Context, srcID string, dst session.Meta) (session.Info, error) {
	return session.Info{}, nil
}
func (s *memoryStore) List(ctx context.Context) ([]session.Info, error) { return nil, nil }
func (s *memoryStore) Delete(ctx context.Context, sessionID string) error {
	return nil
}
func (s *memoryStore) Close() error { return nil }

// newTestBridge 组装桥接与依赖。
func newTestBridge(t *testing.T, sess *fakeSession) (*Bridge, *memoryStore, *[]agent.Event) {
	t.Helper()
	store := &memoryStore{}
	var events []agent.Event
	backend := &fakeBackend{session: sess, info: AgentInfo{ID: "fake", Name: "Fake"}}
	bridge := NewBridge(backend, BridgeOptions{
		SessionID: "s_test",
		WorkDir:   "/tmp/work",
		Approvals: agent.NewApprovals(),
		Store:     store,
		AgentID:   "fake",
	})
	return bridge, store, &events
}

func TestBridgeRunEmitsEventsAndPersists(t *testing.T) {
	sess := &fakeSession{
		updates: []SessionUpdate{
			{Kind: UpdateAgentMessageChunk, MessageID: "m1", Content: &ContentBlock{Type: "text", Text: "你好"}},
			{Kind: UpdateAgentThoughtChunk, MessageID: "m1", Content: &ContentBlock{Type: "text", Text: "想想"}},
			{Kind: UpdateAgentMessageChunk, MessageID: "m2", Content: &ContentBlock{Type: "text", Text: ""}}, // 边界
			{Kind: UpdateAgentMessageChunk, MessageID: "m2", Content: &ContentBlock{Type: "text", Text: "第二条"}},
			{Kind: UpdateToolCall, MessageID: "m2", ToolCall: &ToolCall{
				ToolCallID: "t1", Title: "Read a.go", ToolName: "Read", Kind: ToolRead,
				Status: StatusPending, RawInput: json.RawMessage(`{"file_path":"a.go"}`),
			}},
			{Kind: UpdateToolCallUpdate, ToolCallUpdate: &ToolCallUpdate{
				ToolCallID: "t1", Status: StatusCompleted, RawOutput: json.RawMessage(`"文件内容"`),
			}},
			{Kind: UpdatePlan, Plan: &Plan{Entries: []PlanEntry{{Content: "调研", Status: "completed"}}}},
			{Kind: UpdateAvailableCommands, Commands: []AvailableCommand{{Name: "review"}}},
		},
		result: PromptResult{StopReason: StopEndTurn, Usage: &UsageSummary{InputTokens: 100, OutputTokens: 20, CachedRead: 5}, Turns: 2},
	}
	bridge, store, events := newTestBridge(t, sess)

	res, err := bridge.Run(context.Background(), agent.Input{TurnID: "turn_1", Prompt: "测试"}, func(ev agent.Event) {
		*events = append(*events, ev)
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != agent.StopCompleted || res.Turns != 2 {
		t.Fatalf("result = %+v", res)
	}
	if res.Usage.InputTokens != 100 || res.Usage.ContextTokens() != 125 {
		t.Fatalf("usage = %+v", res.Usage)
	}

	// 事件序列完整性。
	types := make([]agent.EventType, 0, len(*events))
	var todoSeen, commandsSeen bool
	for _, ev := range *events {
		types = append(types, ev.Type)
		if ev.Type == agent.EventTodoUpdate {
			todoSeen = true
		}
		if ev.Type == agent.EventCommandsUpdate {
			commandsSeen = true
		}
	}
	expect := []agent.EventType{
		agent.EventTurnStart,
		agent.EventStepStart,
		agent.EventTextDelta, agent.EventThinkingDelta,
		agent.EventTextDelta,
		agent.EventToolCallStart, agent.EventToolCallReady,
		agent.EventToolResult,
		agent.EventTodoUpdate,
		agent.EventCommandsUpdate,
		agent.EventUsage,
		agent.EventTurnEnd,
	}
	if len(types) != len(expect) {
		t.Fatalf("event types = %v, want %v", types, expect)
	}
	for i := range expect {
		if types[i] != expect[i] {
			t.Fatalf("event[%d] = %s, want %s (all: %v)", i, types[i], expect[i], types)
		}
	}
	if !todoSeen || !commandsSeen {
		t.Fatalf("todo/commands missing")
	}

	// transcript：用户消息、assistant 消息 ×2、tool_result 消息、usage、turn_end。
	var userCount, assistantCount int
	var resultBlockFound bool
	for _, rec := range store.records {
		switch rec.Type {
		case session.RecordMessage:
			switch rec.Message.Role {
			case "user":
				userCount++
				for _, block := range rec.Message.Blocks {
					if block.Type == "tool_result" && block.ToolUseID == "t1" && block.Text == "文件内容" {
						resultBlockFound = true
					}
				}
			case "assistant":
				assistantCount++
			}
		}
	}
	if userCount != 2 || assistantCount != 2 {
		t.Fatalf("user=%d assistant=%d records=%d", userCount, assistantCount, len(store.records))
	}
	if !resultBlockFound {
		t.Fatalf("tool_result not persisted")
	}

	// tool_result 消息必须紧跟其 assistant 消息（rebuildToolPairs 的配对约定）。
	var lastAssistantIdx, resultMsgIdx = -1, -1
	for i, rec := range store.records {
		if rec.Type != session.RecordMessage {
			continue
		}
		if rec.Message.Role == "assistant" {
			lastAssistantIdx = i
		}
		if rec.Message.Role == "user" && resultMsgIdx == -1 {
			for _, block := range rec.Message.Blocks {
				if block.Type == "tool_result" {
					resultMsgIdx = i
				}
			}
		}
	}
	if resultMsgIdx != lastAssistantIdx+1 {
		t.Fatalf("tool_result message at %d, want right after assistant at %d", resultMsgIdx, lastAssistantIdx)
	}

	// 第二轮：模拟 Prompt 错误走 failTurn。
	sess2 := &fakeSession{err: errors.New("boom")}
	bridge2, _, events2 := newTestBridge(t, sess2)
	res2, err := bridge2.Run(context.Background(), agent.Input{TurnID: "turn_2", Prompt: "x"}, func(ev agent.Event) {
		*events2 = append(*events2, ev)
	})
	if err == nil || res2.StopReason != agent.StopError {
		t.Fatalf("fail turn = %+v err=%v", res2, err)
	}
	last := (*events2)[len(*events2)-1]
	if last.Type != agent.EventTurnEnd {
		t.Fatalf("last event = %s", last.Type)
	}
}

func TestBridgePermissionFlow(t *testing.T) {
	sess := &fakeSession{
		permissionRequest: &PermissionRequest{
			ToolName: "Bash",
			ToolCall: ToolCallUpdate{ToolCallID: "t9", RawInput: json.RawMessage(`{"command":"ls"}`)},
			Options: []PermissionOption{
				{OptionID: "allow-once", Name: "允许一次", Kind: AllowOnce},
				{OptionID: "reject", Name: "拒绝", Kind: RejectOnce},
			},
		},
	}
	bridge, _, events := newTestBridge(t, sess)

	go func() {
		// 稍后台答审批，模拟前端 ApproveTool 的时序。
		time.Sleep(100 * time.Millisecond)
		bridge.opts.Approvals.Decide(bridge.pendingRequestID(), permission.Response{Option: "allow-once"})
	}()
	res, err := bridge.Run(context.Background(), agent.Input{TurnID: "t", Prompt: "x"}, func(ev agent.Event) {
		*events = append(*events, ev)
	})
	if err != nil || res.StopReason != agent.StopCompleted {
		t.Fatalf("run = %+v err=%v", res, err)
	}

	// 事件里应有 permission_request 与 permission_resolved。
	var sawRequest, sawResolved bool
	for _, ev := range *events {
		if ev.Type == agent.EventPermissionRequest && ev.Approval != nil && ev.Approval.ToolName == "Bash" {
			sawRequest = true
		}
		if ev.Type == agent.EventPermissionResolved && ev.Text == "allow" {
			sawResolved = true
		}
	}
	if !sawRequest || !sawResolved {
		t.Fatalf("permission events: request=%v resolved=%v events=%+v", sawRequest, sawResolved, *events)
	}
}

func TestBridgePermissionCancelledOnTurnCancel(t *testing.T) {
	sess := &fakeSession{
		permissionRequest: &PermissionRequest{ToolName: "Bash"},
	}
	bridge, _, events := newTestBridge(t, sess)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel() // 模拟用户停止本轮
	}()
	res, err := bridge.Run(ctx, agent.Input{TurnID: "t", Prompt: "x"}, func(ev agent.Event) {
		*events = append(*events, ev)
	})
	if err != nil {
		t.Fatalf("run err: %v", err)
	}
	// 取消后权限等待以 cancelled 收场，轮次正常结算（真实后端会据此
	// deny+interrupt CLI），必须发出 turn_end，前端才不会卡在运行态。
	if res.StopReason != agent.StopCompleted {
		t.Fatalf("stop reason = %s", res.StopReason)
	}
	var last agent.Event
	if len(*events) > 0 {
		last = (*events)[len(*events)-1]
	}
	if last.Type != agent.EventTurnEnd {
		t.Fatalf("last event = %s", last.Type)
	}
	var resolvedCancelled bool
	for _, ev := range *events {
		if ev.Type == agent.EventPermissionResolved && ev.Text == "canceled" {
			resolvedCancelled = true
		}
	}
	if !resolvedCancelled {
		t.Fatalf("permission_resolved(canceled) missing in %+v", *events)
	}
}

// pendingRequestID 返回最近一次登记的审批 requestID（测试辅助）。
func (b *Bridge) pendingRequestID() string {
	for _, view := range b.opts.Approvals.Pending(b.opts.SessionID) {
		return view.RequestID
	}
	return ""
}

func TestBridgeCancelClosesSessionPrompt(t *testing.T) {
	sess := &fakeSession{}
	bridge, _, _ := newTestBridge(t, sess)
	// Run 一次让桥接持有会话实例。
	if _, err := bridge.Run(context.Background(), agent.Input{TurnID: "t", Prompt: "x"}, nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	bridge.Cancel()
	if err := bridge.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !sess.closed {
		t.Fatalf("session not closed")
	}
	// Close 幂等。
	_ = bridge.Close()
}

func TestBridgePathAllowed(t *testing.T) {
	sess := &fakeSession{}
	bridge, _, _ := newTestBridge(t, sess)
	if !bridge.pathAllowed("/tmp/work/a.go") {
		t.Fatalf("inside path rejected")
	}
	if bridge.pathAllowed("/tmp/other/a.go") {
		t.Fatalf("outside path allowed")
	}
	_ = permission.ModeDefault
}
