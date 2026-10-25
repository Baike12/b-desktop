package subagent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Baike12/b-desktop/internal/agent"
	"github.com/Baike12/b-desktop/internal/permission"
)

// TestSendMessageToRunningAgentQueuesMessage 验证运行中的后台任务收到消息后
// 在下一轮工具边界注入。
func TestSendMessageToRunningAgentQueuesMessage(t *testing.T) {
	dir := t.TempDir()
	// 脚本：子 Agent 先跑一条 sleep 命令（执行期间外部投递消息），
	// 消息随工具结果注入后模型输出确认文本。
	provider := newFakeProvider(
		toolUseStep("m1", plannedCall{id: "t1", name: "Bash", input: `{"command":"sleep 0.5"}`}),
		textStep("m2", "steered reply"),
	)
	mgr := newTestManager(t, dir, dir, provider, permission.ModeBypassPermissions)

	bgTool := NewAgentTool(mgr)
	res, err := bgTool.Run(context.Background(),
		json.RawMessage(`{"description":"调研","prompt":"开始","run_in_background":true}`), nil)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	taskID := res.Meta["taskId"]
	if taskID == "" {
		t.Fatalf("no taskId: %+v", res.Meta)
	}
	waitFor(t, 2*time.Second, func() bool {
		task, ok := mgr.Task(taskID)
		return ok && task.Status == TaskRunning
	}, "background task started")

	// 运行中投递：应排队并在下一轮注入。
	reply, err := mgr.SendMessageToTask(taskID, "补充指令", "请同时列出风险")
	if err != nil {
		t.Fatalf("SendMessageToTask: %v", err)
	}
	if !strings.Contains(reply, "queued") {
		t.Fatalf("reply = %q", reply)
	}

	waitFor(t, 5*time.Second, func() bool {
		task, ok := mgr.Task(taskID)
		return ok && task.Status == TaskCompleted
	}, "background task completed")

	task, _ := mgr.Task(taskID)
	if !strings.Contains(task.Result, "steered reply") {
		t.Fatalf("result = %q", task.Result)
	}
	// 注入的协调消息必须出现在发给模型的请求里。
	found := false
	for _, req := range provider.Calls() {
		for _, m := range req.Messages {
			for _, b := range m.Blocks {
				if strings.Contains(b.Text, "<coordinator-message") && strings.Contains(b.Text, "请同时列出风险") {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("steered message never reached the model")
	}
}

// TestSendMessageToFinishedAgentResumes 验证已结束的任务以消息续跑并保留历史。
func TestSendMessageToFinishedAgentResumes(t *testing.T) {
	dir := t.TempDir()
	provider := newFakeProvider(
		textStep("m1", "first result"),
		textStep("m2", "resumed with history"),
	)
	mgr := newTestManager(t, dir, dir, provider, permission.ModeBypassPermissions)

	bgTool := NewAgentTool(mgr)
	res, err := bgTool.Run(context.Background(),
		json.RawMessage(`{"description":"首轮","prompt":"开始","run_in_background":true}`), nil)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	taskID := res.Meta["taskId"]
	waitFor(t, 5*time.Second, func() bool {
		task, ok := mgr.Task(taskID)
		return ok && task.Status == TaskCompleted
	}, "first run completed")
	first, _ := mgr.Task(taskID)
	if !strings.Contains(first.Result, "first result") {
		t.Fatalf("first result = %q", first.Result)
	}

	reply, err := mgr.SendMessageToTask(taskID, "追问", "基于上文继续")
	if err != nil {
		t.Fatalf("SendMessageToTask: %v", err)
	}
	if !strings.Contains(reply, "resumed") {
		t.Fatalf("reply = %q", reply)
	}
	waitFor(t, 5*time.Second, func() bool {
		task, ok := mgr.Task(taskID)
		return ok && task.Status == TaskCompleted && strings.Contains(task.Result, "resumed with history")
	}, "resumed run completed")

	// 续跑请求必须带着第一轮的完整历史：第二条 user 消息应包含首轮 prompt。
	found := false
	for _, req := range provider.Calls() {
		if len(req.Messages) >= 2 {
			for _, m := range req.Messages {
				for _, b := range m.Blocks {
					if strings.Contains(b.Text, "基于上文继续") {
						found = true
					}
				}
			}
		}
	}
	if !found {
		t.Fatal("resume prompt never reached the model")
	}
}

// TestSendMessageToUnknownTask 验证未知任务返回错误。
func TestSendMessageToUnknownTask(t *testing.T) {
	dir := t.TempDir()
	mgr := newTestManager(t, dir, dir, newFakeProvider(), permission.ModeBypassPermissions)
	tl := NewSendMessageTool(mgr)
	res, err := tl.Run(context.Background(),
		json.RawMessage(`{"to":"subagent_nope","summary":"x","message":"y"}`), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "not found") {
		t.Fatalf("result = %+v", res)
	}
}

// TestSendMessageToolContract 验证工具契约与输入校验。
func TestSendMessageToolContract(t *testing.T) {
	dir := t.TempDir()
	mgr := newTestManager(t, dir, dir, newFakeProvider(), permission.ModeBypassPermissions)
	tl := NewSendMessageTool(mgr)
	if tl.Name() != SendMessageToolName {
		t.Fatalf("name = %q", tl.Name())
	}
	if !tl.ReadOnly(nil) || !tl.ConcurrencySafe(nil) {
		t.Fatal("SendMessage must be read-only and concurrency safe")
	}
	aa, ok := tl.(interface{ AlwaysAllow() bool })
	if !ok || !aa.AlwaysAllow() {
		t.Fatal("SendMessage should be always-allow")
	}
	res, err := tl.Run(context.Background(),
		json.RawMessage(`{"to":"x","summary":"","message":"m"}`), nil)
	if err == nil {
		t.Fatalf("empty summary should fail, got %+v", res)
	}
}

// 编译期断言：Manager 仍是 SubagentRunner。
var _ agent.SubagentRunner = (*Manager)(nil)
