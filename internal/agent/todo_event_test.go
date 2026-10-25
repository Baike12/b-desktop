package agent

import (
	"context"
	"testing"

	"github.com/Baike12/b-desktop/internal/tool"
	"github.com/Baike12/b-desktop/internal/tool/builtin"
)

// TestEmitTodoUpdate 在 TodoWrite 执行结束后推送完整清单快照，
// 事件顺序在对应 tool_result 之后。
func TestEmitTodoUpdate(t *testing.T) {
	h := newHarness(t, harnessOptions{
		tools: []tool.Tool{builtin.NewTodoWrite()},
		steps: []fakeStep{
			toolUseStep("m1", plannedCall{
				id:   "call1",
				name: "TodoWrite",
				input: `{"todos":[{"content":"调研","status":"completed"},` +
					`{"content":"实现","status":"in_progress","activeForm":"实现中"}]}`,
			}),
			textStep("m2", "done"),
		},
	})
	rec := newRecorder()
	res, err := h.engine.Run(context.Background(), Input{TurnID: "t1", Prompt: "hi"}, rec.sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != StopCompleted {
		t.Fatalf("StopReason = %s (%s), want completed", res.StopReason, res.Err)
	}

	events := rec.snapshot()
	resultAt := indexOf(events, EventToolResult)
	updateAt := indexOf(events, EventTodoUpdate)
	if updateAt < 0 {
		t.Fatal("no todo_update event")
	}
	if updateAt < resultAt {
		t.Fatalf("todo_update at %d must follow tool_result at %d", updateAt, resultAt)
	}
	ev := events[updateAt]
	if ev.Todos == nil || len(ev.Todos.Items) != 2 {
		t.Fatalf("todos = %+v, want 2 items", ev.Todos)
	}
	if first := ev.Todos.Items[0]; first.Content != "调研" || first.Status != "completed" {
		t.Fatalf("item[0] = %+v", first)
	}
	if second := ev.Todos.Items[1]; second.Status != "in_progress" || second.ActiveForm != "实现中" {
		t.Fatalf("item[1] = %+v", second)
	}
}

// TestEmitTodoUpdateSkipsOtherTools 普通工具不触发 todo_update。
func TestEmitTodoUpdateSkipsOtherTools(t *testing.T) {
	h := newHarness(t, harnessOptions{
		tools: []tool.Tool{builtin.NewTodoWrite()},
		steps: []fakeStep{textStep("m1", "plain reply")},
	})
	rec := newRecorder()
	if _, err := h.engine.Run(context.Background(), Input{TurnID: "t1", Prompt: "hi"}, rec.sink); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if events := rec.snapshot(); indexOf(events, EventTodoUpdate) >= 0 {
		t.Fatal("unexpected todo_update without TodoWrite call")
	}
}
