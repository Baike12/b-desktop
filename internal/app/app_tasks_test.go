package app

import (
	"strings"
	"testing"
	"time"

	"github.com/Baike12/b-desktop/internal/agent"
	"github.com/Baike12/b-desktop/internal/llm"
	"github.com/Baike12/b-desktop/internal/task"
)

// TestTaskCompletionWakesIdleSession 验证后台任务完成自动唤醒空闲会话：
// 通知文本作为新一轮的 prompt 进入历史，模型据此继续。
func TestTaskCompletionWakesIdleSession(t *testing.T) {
	provider := newFakeProvider(textStep("m1", "已收到后台结果，处理完成。"))
	app, collector := newTestApp(t, provider)
	info := createTestSession(t, app)

	reg, err := app.taskRegistryComponent()
	if err != nil {
		t.Fatalf("task registry: %v", err)
	}
	// 注册表在 newTestApp 的 Config 里未注入 DataDir 关联，但 testConfig 的
	// DataDir 指向临时目录，OutputDir 已在创建时落盘。

	handle := reg.StartBash(task.StartBashInput{
		SessionID:   info.ID,
		Description: "run tests",
		Command:     "echo ok",
	})
	if got, ok := reg.WaitTerminal(handle.TaskID(), 5*time.Second); !ok || got.Status != task.StatusCompleted {
		t.Fatalf("task = %+v", got)
	}

	// 完成回调异步执行：等待唤醒的新一轮事件。
	ev := collector.waitFor(t, agent.EventTurnStart, 5*1000*1000*1000)
	if ev.SessionID != info.ID {
		t.Fatalf("wake turn on %s, want %s", ev.SessionID, info.ID)
	}
	collector.waitFor(t, agent.EventTurnEnd, 5*1000*1000*1000)

	// 唤醒轮的 prompt 必须是 <task-notification> 文本。
	requests := provider.requests()
	if len(requests) == 0 {
		t.Fatal("no model calls")
	}
	last := requests[len(requests)-1]
	found := false
	for _, m := range last.Messages {
		if m.Role == llm.RoleUser {
			for _, b := range m.Blocks {
				if strings.Contains(b.Text, "<task-notification>") {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("wake prompt missing <task-notification>")
	}

	// 完成事件推给前端（事件先于唤醒轮发出，waitFor 会丢弃非匹配项，改为扫快照）。
	deadline := time.Now().Add(2 * time.Second)
	var update agent.Event
	for time.Now().Before(deadline) && update.Task == nil {
		for _, ev := range collector.snapshot() {
			if ev.Type == agent.EventTaskUpdate && ev.Task != nil && ev.Task.TaskID == handle.TaskID() {
				update = ev
			}
		}
		if update.Task == nil {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if update.Task == nil {
		t.Fatal("missing task_update event")
	}
	if update.Task.Status != string(task.StatusCompleted) {
		t.Fatalf("status = %s", update.Task.Status)
	}

	waitIdle(t, app, info.ID)
}

// TestTaskCompletionQueuesWhileRunning 验证会话运行中完成的后台任务进入
// 通知队列，由循环在工具边界注入而不是立刻唤醒新轮次。
func TestTaskCompletionQueuesWhileRunning(t *testing.T) {
	provider := newFakeProvider(
		textStep("m1", "第一步完成"),
		textStep("m2", "第二步完成"),
	)
	app, collector := newTestApp(t, provider)
	info := createTestSession(t, app)

	if _, err := app.SendMessage(info.ID, "开始", nil); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	reg, err := app.taskRegistryComponent()
	if err != nil {
		t.Fatalf("task registry: %v", err)
	}
	handle := reg.StartBash(task.StartBashInput{
		SessionID: info.ID,
		Command:   "echo queued",
	})
	if got, ok := reg.WaitTerminal(handle.TaskID(), 5*time.Second); !ok || got.Status != task.StatusCompleted {
		t.Fatalf("task = %+v", got)
	}

	// 等运行中的轮次自然结束后，通知应触发一次自动续跑（第二个 turn_start）。
	first := collector.waitFor(t, agent.EventTurnStart, 3*1000*1000*1000)
	collector.waitFor(t, agent.EventTurnEnd, 3*1000*1000*1000)
	// 续跑轮可能已经开始：等待第二个 turn_start 或空闲。
	deadline := time.Now().Add(5 * 1000 * 1000 * 1000)
	second := agent.Event{}
	for time.Now().Before(deadline) {
		for _, ev := range collector.snapshot() {
			if ev.Type == agent.EventTurnStart && ev.TurnID != first.TurnID {
				second = ev
			}
		}
		if second.TurnID != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if second.TurnID == "" {
		t.Fatal("queued notification did not trigger a continuation turn")
	}
	collector.waitFor(t, agent.EventTurnEnd, 5*1000*1000*1000)
	waitIdle(t, app, info.ID)
}
