package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/Baike12/b-desktop/internal/tool"
)

// Todo 状态取值。
const (
	todoPending    = "pending"
	todoInProgress = "in_progress"
	todoCompleted  = "completed"
)

// maxTodos 是一次写入允许的条目上限，防止模型把清单撑爆上下文。
const maxTodos = 200

// todoSchema 是 TodoWrite 的输入 schema。
var todoSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "todos": {
      "type": "array",
      "maxItems": 200,
      "description": "The complete todo list. This replaces the previous list: omit an item to delete it.",
      "items": {
        "type": "object",
        "properties": {
          "content": {
            "type": "string",
            "description": "Imperative description of the task."
          },
          "status": {
            "type": "string",
            "enum": ["pending", "in_progress", "completed"]
          },
          "activeForm": {
            "type": "string",
            "description": "Present continuous wording shown while the task is in progress."
          }
        },
        "required": ["content", "status"]
      }
    }
  },
  "required": ["todos"]
}`)

// TodoWrite 维护当前会话的待办清单。
//
// 模型每次提交完整清单，工具整体替换旧状态：新增是追加、修改是改动同一条目、
// 删除是省略该条目。清单只存在内存里，不写文件系统，因此在任何权限模式下都默认放行。
type TodoWrite struct {
	mu    sync.Mutex
	items []todoItem
}

// todoItem 是一条待办。
type todoItem struct {
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"activeForm,omitempty"`
}

// todoInput 是 TodoWrite 的输入。
type todoInput struct {
	Todos []todoItem `json:"todos"`
}

// NewTodoWrite 创建空清单的 TodoWrite 工具。一个会话一个实例，状态互不共享。
func NewTodoWrite() tool.Tool {
	return &TodoWrite{}
}

// Name 返回工具名。
func (t *TodoWrite) Name() string { return "TodoWrite" }

// Description 返回给模型看的说明。
func (t *TodoWrite) Description() string {
	return "Replace the session todo list. Pass the complete list every time: " +
		"add items by including them, update an item by resending it with a new status, " +
		"delete an item by leaving it out. Keep at most one item in_progress. " +
		"The rendered list is returned as the tool result."
}

// InputSchema 返回 JSON Schema。
func (t *TodoWrite) InputSchema() json.RawMessage { return todoSchema }

// ReadOnly 报告该调用是否只读。清单只存在内存中，不触碰文件系统，恒为 true。
func (t *TodoWrite) ReadOnly(json.RawMessage) bool { return true }

// ConcurrencySafe 报告并发安全。内部有互斥锁，可与任何调用并行。
func (t *TodoWrite) ConcurrencySafe(json.RawMessage) bool { return true }

// AlwaysAllow 报告该工具在任何权限模式下都默认放行（无文件、无进程副作用）。
func (t *TodoWrite) AlwaysAllow() bool { return true }

// TodoList 返回当前完整清单（tool.TodoProvider 契约）。
func (t *TodoWrite) TodoList() []tool.TodoEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]tool.TodoEntry, len(t.items))
	for i, item := range t.items {
		out[i] = tool.TodoEntry{Content: item.Content, Status: item.Status, ActiveForm: item.ActiveForm}
	}
	return out
}

// Run 校验并替换清单，返回渲染后的文本。
func (t *TodoWrite) Run(_ context.Context, input json.RawMessage, _ tool.ProgressFunc) (tool.Result, error) {
	var in todoInput
	if err := json.Unmarshal(input, &in); err != nil {
		return tool.Result{IsError: true, Content: "invalid input: " + err.Error()}, nil
	}
	items, err := normalizeTodos(in.Todos)
	if err != nil {
		return tool.Result{IsError: true, Content: err.Error()}, nil
	}

	t.mu.Lock()
	t.items = items
	rendered, counts := renderTodos(items)
	t.mu.Unlock()

	return tool.Result{
		Content: rendered,
		Display: rendered,
		Meta: map[string]string{
			"total":       fmt.Sprint(counts.total),
			"pending":     fmt.Sprint(counts.pending),
			"in_progress": fmt.Sprint(counts.inProgress),
			"completed":   fmt.Sprint(counts.completed),
		},
	}, nil
}

// normalizeTodos 校验条目并返回副本；状态为空时按 pending 处理。
func normalizeTodos(in []todoItem) ([]todoItem, error) {
	if len(in) > maxTodos {
		return nil, fmt.Errorf("invalid input: too many todos (%d > %d)", len(in), maxTodos)
	}
	out := make([]todoItem, 0, len(in))
	inProgress := 0
	for i, item := range in {
		content := strings.TrimSpace(item.Content)
		if content == "" {
			return nil, fmt.Errorf("invalid input: todos[%d].content is required", i)
		}
		status := item.Status
		if status == "" {
			status = todoPending
		}
		switch status {
		case todoPending, todoCompleted:
		case todoInProgress:
			inProgress++
		default:
			return nil, fmt.Errorf("invalid input: todos[%d].status %q is not one of pending, in_progress, completed", i, item.Status)
		}
		out = append(out, todoItem{Content: content, Status: status, ActiveForm: strings.TrimSpace(item.ActiveForm)})
	}
	if inProgress > 1 {
		return nil, fmt.Errorf("invalid input: at most one todo may be in_progress, got %d", inProgress)
	}
	return out, nil
}

// todoCounts 是渲染用的汇总。
type todoCounts struct {
	total      int
	pending    int
	inProgress int
	completed  int
}

// renderTodos 把清单渲染成给模型看的文本。
func renderTodos(items []todoItem) (string, todoCounts) {
	counts := todoCounts{total: len(items)}
	for _, item := range items {
		switch item.Status {
		case todoPending:
			counts.pending++
		case todoInProgress:
			counts.inProgress++
		case todoCompleted:
			counts.completed++
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "todos: %d total, %d pending, %d in progress, %d completed\n",
		counts.total, counts.pending, counts.inProgress, counts.completed)
	if counts.total == 0 {
		sb.WriteString("(the list is empty)")
		return sb.String(), counts
	}
	for i, item := range items {
		switch item.Status {
		case todoCompleted:
			fmt.Fprintf(&sb, "%d. [x] %s\n", i+1, item.Content)
		case todoInProgress:
			fmt.Fprintf(&sb, "%d. [~] %s", i+1, item.Content)
			if item.ActiveForm != "" {
				fmt.Fprintf(&sb, " (in progress: %s)", item.ActiveForm)
			}
			sb.WriteByte('\n')
		default:
			fmt.Fprintf(&sb, "%d. [ ] %s\n", i+1, item.Content)
		}
	}
	return sb.String(), counts
}
