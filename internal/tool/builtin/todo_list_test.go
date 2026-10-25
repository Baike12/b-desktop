package builtin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Baike12/b-desktop/internal/tool"
)

// TestTodoWriteTodoList TodoList 返回最近一次提交的完整清单（tool.TodoProvider 契约）。
func TestTodoWriteTodoList(t *testing.T) {
	todo, ok := NewTodoWrite().(tool.TodoProvider)
	if !ok {
		t.Fatal("TodoWrite must implement tool.TodoProvider")
	}
	if items := todo.TodoList(); len(items) != 0 {
		t.Fatalf("initial list = %+v, want empty", items)
	}
	input, err := json.Marshal(map[string]any{
		"todos": []map[string]string{
			{"content": "第一步", "status": "completed"},
			{"content": "第二步", "status": "in_progress", "activeForm": "进行中"},
			{"content": "第三步", "status": ""},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, runErr := todo.(tool.Tool).Run(context.Background(), input, nil)
	if runErr != nil || res.IsError {
		t.Fatalf("Run: %v / %+v", runErr, res)
	}
	items := todo.TodoList()
	if len(items) != 3 {
		t.Fatalf("items = %+v", items)
	}
	if items[0].Content != "第一步" || items[0].Status != "completed" {
		t.Fatalf("items[0] = %+v", items[0])
	}
	if items[1].ActiveForm != "进行中" {
		t.Fatalf("items[1] = %+v", items[1])
	}
	// 空状态按 pending 归一。
	if items[2].Status != "pending" {
		t.Fatalf("items[2] = %+v", items[2])
	}
}
