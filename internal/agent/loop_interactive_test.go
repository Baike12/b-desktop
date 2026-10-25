package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/Baike12/b-desktop/internal/llm"
	"github.com/Baike12/b-desktop/internal/permission"
	"github.com/Baike12/b-desktop/internal/tool"
)

// interactiveTool 是实现 tool.Interactive 的假工具，记录收到的输入。
type interactiveTool struct {
	fakeTool
}

func (t *interactiveTool) Interactive() bool { return true }

// newInteractiveTool 创建交互式假工具。
func newInteractiveTool(name string) *interactiveTool {
	return &interactiveTool{fakeTool: *newFakeTool(name, true, true)}
}

// TestInteractiveToolForcesAskAndMergesAnswers 验证三件事：
// 交互工具在 bypassPermissions 下仍强制 ask；用户应答经审批通道回填进输入；
// 工具按回填后的输入执行并产出结果。
func TestInteractiveToolForcesAskAndMergesAnswers(t *testing.T) {
	ask := &interactiveTool{fakeTool: *newFakeTool("AskUserQuestion", true, true)}
	var gotInput json.RawMessage
	ask.runFn = func(_ context.Context, input json.RawMessage) (tool.Result, error) {
		gotInput = append(json.RawMessage(nil), input...)
		return tool.Result{Content: "answers recorded"}, nil
	}
	h := newHarness(t, harnessOptions{
		tools: []tool.Tool{ask},
		steps: []fakeStep{
			toolUseStep("m1", plannedCall{id: "t1", name: "AskUserQuestion", input: `{"question":"选A还是B?"}`}),
			textStep("m2", "done"),
		},
		// bypassPermissions 会让普通工具直接放行；交互工具必须仍然 ask。
	})
	rec := newRecorder()

	decisionDone := make(chan struct{})
	var decideErr bool
	go func() {
		defer close(decisionDone)
		// 等审批请求出现后，以回填输入的应答放行。
		ev := rec.waitFor(t, EventPermissionRequest, 5*1000*1000*1000)
		if ev.Approval == nil {
			decideErr = true
			return
		}
		updated := json.RawMessage(`{"question":"选A还是B?","answers":{"选A还是B?":"B"}}`)
		if !h.approvals.Decide(ev.Approval.RequestID, permission.Response{
			Option:       permission.OptionAllowOnce,
			UpdatedInput: updated,
		}) {
			decideErr = true
		}
	}()

	res, err := h.engine.Run(context.Background(), Input{TurnID: "t1", Prompt: "ask me"}, rec.sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	<-decisionDone
	if decideErr {
		t.Fatal("approval decision failed")
	}
	if res.StopReason != StopCompleted {
		t.Fatalf("stop reason = %s", res.StopReason)
	}
	if len(gotInput) == 0 || !strings.Contains(string(gotInput), `"answers"`) {
		t.Fatalf("tool input missing answers: %s", gotInput)
	}

	// 第二次模型调用的历史里，工具结果应为成功。
	calls := h.provider.Calls()
	if len(calls) != 2 {
		t.Fatalf("model calls = %d", len(calls))
	}
	found := false
	for _, m := range calls[1].Messages {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult && b.ToolUseID == "t1" && !b.IsError {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("tool_result for interactive tool should be successful")
	}
}

// TestInteractiveToolDenialProducesErrorResult 验证用户拒绝交互时工具不执行。
func TestInteractiveToolDenialProducesErrorResult(t *testing.T) {
	ask := &interactiveTool{fakeTool: *newFakeTool("AskUserQuestion", true, true)}
	ask.runFn = func(context.Context, json.RawMessage) (tool.Result, error) {
		t.Fatal("interactive tool must not run after denial")
		return tool.Result{}, nil
	}
	h := newHarness(t, harnessOptions{
		tools: []tool.Tool{ask},
		steps: []fakeStep{
			toolUseStep("m1", plannedCall{id: "t1", name: "AskUserQuestion", input: `{}`}),
			textStep("m2", "ok"),
		},
	})
	rec := newRecorder()

	go func() {
		ev := rec.waitFor(t, EventPermissionRequest, 5*1000*1000*1000)
		if ev.Approval != nil {
			h.approvals.Decide(ev.Approval.RequestID, permission.Response{Option: permission.OptionDenyOnce})
		}
	}()

	res, err := h.engine.Run(context.Background(), Input{TurnID: "t1", Prompt: "ask me"}, rec.sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ask.callCount() != 0 {
		t.Fatalf("tool calls = %d, want 0", ask.callCount())
	}
	calls := h.provider.Calls()
	denied := false
	for _, m := range calls[1].Messages {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult && b.ToolUseID == "t1" && b.IsError {
				denied = true
			}
		}
	}
	if !denied {
		t.Fatal("denied interactive call should produce an error tool_result")
	}
	if res.StopReason != StopCompleted {
		t.Fatalf("stop reason = %s", res.StopReason)
	}
}

// TestNotificationsJoinToolResultMessage 验证后台任务通知作为文本块
// 并入工具结果所在的同一条 user 消息（角色交替约束）。
func TestNotificationsJoinToolResultMessage(t *testing.T) {
	notifs := NewNotificationBuffer()
	echo := newFakeTool("Echo", true, true)
	echo.runFn = func(context.Context, json.RawMessage) (tool.Result, error) {
		// 模拟工具执行期间后台任务完成并投递通知。
		notifs.Push("<task-notification><task_id>b1</task_id><status>completed</status></task-notification>")
		return tool.Result{Content: "echo"}, nil
	}
	h := newHarness(t, harnessOptions{
		tools:         []tool.Tool{echo},
		notifications: notifs,
		steps: []fakeStep{
			toolUseStep("m1", plannedCall{id: "t1", name: "Echo", input: `{}`}),
			textStep("m2", "done"),
		},
	})
	rec := newRecorder()

	if _, err := h.engine.Run(context.Background(), Input{TurnID: "t1", Prompt: "go"}, rec.sink); err != nil {
		t.Fatalf("Run: %v", err)
	}

	calls := h.provider.Calls()
	if len(calls) != 2 {
		t.Fatalf("model calls = %d", len(calls))
	}
	var notificationMessage *llm.Message
	for i := range calls[1].Messages {
		m := &calls[1].Messages[i]
		for _, b := range m.Blocks {
			if b.Type == llm.BlockText && strings.Contains(b.Text, "<task-notification>") {
				notificationMessage = m
			}
		}
	}
	if notificationMessage == nil {
		t.Fatal("second request is missing the notification text")
	}
	if notificationMessage.Role != llm.RoleUser {
		t.Fatalf("notification rides on role %s", notificationMessage.Role)
	}
	// 同一条消息内必须同时有 tool_result（通知不能独立成消息，避免连续 user）。
	hasToolResult := false
	for _, b := range notificationMessage.Blocks {
		if b.Type == llm.BlockToolResult && b.ToolUseID == "t1" {
			hasToolResult = true
		}
	}
	if !hasToolResult {
		t.Fatal("notification message must also carry the tool_result blocks")
	}
	if remaining := notifs.Drain(); len(remaining) != 0 {
		t.Fatalf("queue not drained: %v", remaining)
	}
}

// TestNotificationBufferContract 验证通知队列的基本语义。
func TestNotificationBufferContract(t *testing.T) {
	var q NotificationQueue = NewNotificationBuffer()
	q.Push("")
	if got := q.Drain(); got != nil {
		t.Fatalf("empty push drained %v", got)
	}
	q.Push("a")
	q.Push("b")
	if got := q.Drain(); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("drain = %v", got)
	}
	if got := q.Drain(); got != nil {
		t.Fatalf("second drain = %v", got)
	}
	// 并发 Push/Drain 不死锁即可。
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				q.Push("x")
				q.Drain()
			}
		}()
	}
	wg.Wait()
}
