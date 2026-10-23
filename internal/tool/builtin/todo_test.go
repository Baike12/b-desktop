package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"anc-dsk/internal/tool"
)

// runTodo 执行一次 TodoWrite 调用。
func runTodo(t *testing.T, tw tool.Tool, todos []map[string]any) tool.Result {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"todos": todos})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	res, err := tw.Run(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func TestTodoWriteAdd(t *testing.T) {
	tw := NewTodoWrite()
	res := runTodo(t, tw, []map[string]any{
		{"content": "write tests", "status": "pending"},
		{"content": "implement bash", "status": "in_progress", "activeForm": "implementing bash"},
		{"content": "read docs", "status": "completed"},
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	for _, want := range []string{
		"3 total, 1 pending, 1 in progress, 1 completed",
		"1. [ ] write tests",
		"2. [~] implement bash (in progress: implementing bash)",
		"3. [x] read docs",
	} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("content missing %q:\n%s", want, res.Content)
		}
	}
	if res.Meta["total"] != "3" || res.Meta["pending"] != "1" ||
		res.Meta["in_progress"] != "1" || res.Meta["completed"] != "1" {
		t.Fatalf("unexpected meta: %+v", res.Meta)
	}
}

func TestTodoWriteUpdateStatus(t *testing.T) {
	tw := NewTodoWrite()
	runTodo(t, tw, []map[string]any{
		{"content": "task a", "status": "pending"},
		{"content": "task b", "status": "pending"},
	})

	res := runTodo(t, tw, []map[string]any{
		{"content": "task a", "status": "completed"},
		{"content": "task b", "status": "in_progress", "activeForm": "doing b"},
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "1. [x] task a") {
		t.Fatalf("status not updated:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "2. [~] task b (in progress: doing b)") {
		t.Fatalf("status not updated:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "2 total, 2 pending") {
		t.Fatalf("list was not replaced:\n%s", res.Content)
	}
}

func TestTodoWriteDelete(t *testing.T) {
	tw := NewTodoWrite()
	runTodo(t, tw, []map[string]any{
		{"content": "keep", "status": "pending"},
		{"content": "remove me", "status": "pending"},
		{"content": "keep too", "status": "completed"},
	})

	res := runTodo(t, tw, []map[string]any{
		{"content": "keep", "status": "pending"},
		{"content": "keep too", "status": "completed"},
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if strings.Contains(res.Content, "remove me") {
		t.Fatalf("deleted item still present:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "2 total") {
		t.Fatalf("item count not updated:\n%s", res.Content)
	}
}

func TestTodoWriteClear(t *testing.T) {
	tw := NewTodoWrite()
	runTodo(t, tw, []map[string]any{{"content": "one", "status": "pending"}})

	res := runTodo(t, tw, []map[string]any{})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "the list is empty") {
		t.Fatalf("empty list not rendered:\n%s", res.Content)
	}
	if res.Meta["total"] != "0" {
		t.Fatalf("total = %q, want 0", res.Meta["total"])
	}
}

func TestTodoWriteRejectsInvalidInput(t *testing.T) {
	tw := NewTodoWrite()
	cases := map[string][]map[string]any{
		"unknown status": {
			{"content": "a", "status": "doing"},
		},
		"empty content": {
			{"content": "   ", "status": "pending"},
		},
		"two in progress": {
			{"content": "a", "status": "in_progress"},
			{"content": "b", "status": "in_progress"},
		},
	}
	for name, todos := range cases {
		res := runTodo(t, tw, todos)
		if !res.IsError {
			t.Fatalf("%s: expected error result, got %+v", name, res)
		}
	}

	// 非法输入不得污染已有清单。
	runTodo(t, tw, []map[string]any{{"content": "valid", "status": "pending"}})
	_ = runTodo(t, tw, cases["two in progress"])
	res := runTodo(t, tw, []map[string]any{{"content": "valid", "status": "pending"}})
	if res.Meta["total"] != "1" {
		t.Fatalf("invalid write changed state: %+v", res.Meta)
	}
}

func TestTodoWriteEmptyStatusDefaultsToPending(t *testing.T) {
	res := runTodo(t, NewTodoWrite(), []map[string]any{{"content": "default status"}})
	if res.IsError || !strings.Contains(res.Content, "[ ] default status") {
		t.Fatalf("empty status not normalized: %+v", res)
	}
}

func TestTodoWriteFlags(t *testing.T) {
	tw := NewTodoWrite()
	if tw.Name() != "TodoWrite" {
		t.Fatalf("Name = %q", tw.Name())
	}
	if !tw.ReadOnly(nil) {
		t.Fatal("TodoWrite must be read-only")
	}
	if !tw.ConcurrencySafe(nil) {
		t.Fatal("TodoWrite must be concurrency safe")
	}
	aa, ok := tw.(tool.AlwaysAllow)
	if !ok || !aa.AlwaysAllow() {
		t.Fatal("TodoWrite must implement tool.AlwaysAllow and return true")
	}
	if !json.Valid(tw.InputSchema()) {
		t.Fatal("InputSchema is not valid JSON")
	}
}

func TestTodoWriteConcurrentCalls(t *testing.T) {
	tw := NewTodoWrite()
	var wg sync.WaitGroup
	errs := make(chan string, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			raw, err := json.Marshal(map[string]any{"todos": []map[string]any{
				{"content": fmt.Sprintf("task %d", i), "status": "pending"},
			}})
			if err != nil {
				errs <- err.Error()
				return
			}
			res, err := tw.Run(context.Background(), raw, nil)
			if err != nil {
				errs <- err.Error()
				return
			}
			if res.IsError {
				errs <- res.Content
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Fatalf("concurrent call failed: %s", msg)
	}

	res := runTodo(t, tw, []map[string]any{{"content": "final", "status": "completed"}})
	if res.Meta["total"] != "1" || !strings.Contains(res.Content, "1 total, 0 pending, 0 in progress, 1 completed") {
		t.Fatalf("state inconsistent after concurrent writes: %+v\n%s", res.Meta, res.Content)
	}
}
