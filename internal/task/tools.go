package task

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Baike12/b-desktop/internal/tool"
)

// TaskOutput / TaskStop / TaskList 的默认参数（与参考实现对齐）。
const (
	// defaultOutputChars 是 TaskOutput 返回输出的字符上限（保尾截断）。
	defaultOutputChars = 32000
	// maxWaitTimeout 是 block=true 允许的最大等待时长。
	maxWaitTimeout = 600 * time.Second
	// resultPreviewChars 是完成通知里 <result> 的预览长度。
	resultPreviewChars = 2000
)

// NewTaskOutput 创建 TaskOutput 工具。它查询后台任务的输出与状态，
// block=true 时阻塞等待任务进入终态或超时。
func NewTaskOutput(reg *Registry) tool.Tool { return &taskOutputTool{reg: reg} }

// NewTaskStop 创建 TaskStop 工具。
func NewTaskStop(reg *Registry) tool.Tool { return &taskStopTool{reg: reg} }

// NewTaskList 创建 TaskList 工具。
func NewTaskList(reg *Registry) tool.Tool { return &taskListTool{reg: reg} }

// taskOutputTool 实现 TaskOutput。
type taskOutputTool struct {
	reg *Registry
}

var _ tool.Tool = (*taskOutputTool)(nil)
var _ tool.AlwaysAllow = (*taskOutputTool)(nil)

func (t *taskOutputTool) Name() string { return "TaskOutput" }

func (t *taskOutputTool) Description() string {
	return "Retrieve output from a running or completed background task (background Bash command or background sub-agent). " +
		"Background tasks are started with Bash run_in_background=true or Agent run_in_background=true; " +
		"their completion is also pushed to you automatically as a <task-notification> message, so you usually only need " +
		"this tool when you want to poll progress before completion. " +
		"block=false returns immediately (retrieval_status=not_ready while still running). " +
		"block=true (default) waits up to timeout_ms (default 30000, max 600000) for the task to finish."
}

func (t *taskOutputTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "task_id": {"type": "string", "description": "The task ID returned when the background task was launched."},
    "block": {"type": "boolean", "default": true, "description": "Wait for the task to finish before returning. Default true."},
    "timeout_ms": {"type": "integer", "minimum": 0, "maximum": 600000, "default": 30000, "description": "Max wait in milliseconds when block=true. Default 30000, capped at 600000."}
  },
  "required": ["task_id"],
  "additionalProperties": false
}`)
}

func (t *taskOutputTool) ReadOnly(json.RawMessage) bool { return true }

func (t *taskOutputTool) ConcurrencySafe(json.RawMessage) bool { return true }

func (t *taskOutputTool) AlwaysAllow() bool { return true }

type taskOutputInput struct {
	TaskID    string `json:"task_id"`
	Block     *bool  `json:"block"`
	TimeoutMS int    `json:"timeout_ms"`
}

func (t *taskOutputTool) Run(ctx context.Context, input json.RawMessage, progress tool.ProgressFunc) (tool.Result, error) {
	var in taskOutputInput
	if err := json.Unmarshal(input, &in); err != nil {
		return tool.Result{}, fmt.Errorf("TaskOutput: decode input: %w", err)
	}
	if strings.TrimSpace(in.TaskID) == "" {
		return tool.Result{}, fmt.Errorf("TaskOutput: task_id is required")
	}
	block := in.Block == nil || *in.Block
	timeout := 30 * time.Second
	if in.TimeoutMS > 0 {
		timeout = time.Duration(in.TimeoutMS) * time.Millisecond
	}
	if timeout > maxWaitTimeout {
		timeout = maxWaitTimeout
	}

	snapshot, ok := t.reg.Get(in.TaskID)
	if !ok {
		return tool.Result{IsError: true, Content: "No task found with ID " + in.TaskID + ". Use TaskList to see active tasks."}, nil
	}

	retrievalStatus := "success"
	if block && !snapshot.Status.Terminal() {
		snapshot, _ = t.reg.WaitTerminal(in.TaskID, timeout)
		if !snapshot.Status.Terminal() {
			retrievalStatus = "timeout"
		}
	} else if !snapshot.Status.Terminal() {
		retrievalStatus = "not_ready"
	}
	if ctx.Err() != nil {
		return tool.Result{}, fmt.Errorf("TaskOutput: %w", ctx.Err())
	}

	var b strings.Builder
	fmt.Fprintf(&b, "<retrieval_status>%s</retrieval_status>\n", retrievalStatus)
	fmt.Fprintf(&b, "<task_id>%s</task_id>\n", snapshot.ID)
	fmt.Fprintf(&b, "<task_type>%s</task_type>\n", snapshot.Kind)
	fmt.Fprintf(&b, "<status>%s</status>\n", snapshot.Status)
	switch snapshot.Kind {
	case KindAgent:
		if snapshot.Status.Terminal() {
			if snapshot.Error != "" {
				fmt.Fprintf(&b, "<error>%s</error>\n", snapshot.Error)
			}
			fmt.Fprintf(&b, "<result>\n%s\n</result>\n", clampChars(snapshot.Result, defaultOutputChars))
		} else if lines := t.reg.Progress(snapshot.ID); len(lines) > 0 {
			fmt.Fprintf(&b, "<progress>\n%s\n</progress>\n", strings.Join(lines, "\n"))
		}
	default:
		if snapshot.Status.Terminal() {
			fmt.Fprintf(&b, "<exit_code>%d</exit_code>\n", snapshot.ExitCode)
			if snapshot.Error != "" {
				fmt.Fprintf(&b, "<error>%s</error>\n", snapshot.Error)
			}
		}
		text, truncated, total, err := t.reg.OutputTail(snapshot.ID, defaultOutputChars)
		switch {
		case err == nil && strings.TrimSpace(text) != "":
			fmt.Fprintf(&b, "<output>\n%s\n</output>\n", text)
			if truncated {
				fmt.Fprintf(&b, "[output truncated: showing last %d of %d bytes. Full output file: %s]\n",
					len(text), total, snapshot.OutputFile)
			}
		case err == nil:
			b.WriteString("<output>(no output)</output>\n")
		default:
			fmt.Fprintf(&b, "<output>(output unavailable: %v)</output>\n", err)
		}
	}
	content := strings.TrimRight(b.String(), "\n")
	return tool.Result{Content: content, Display: content}, nil
}

// taskStopTool 实现 TaskStop。
type taskStopTool struct {
	reg *Registry
}

var _ tool.Tool = (*taskStopTool)(nil)
var _ tool.AlwaysAllow = (*taskStopTool)(nil)

func (t *taskStopTool) Name() string { return "TaskStop" }

func (t *taskStopTool) Description() string {
	return "Stop a running background task by ID: a background Bash command or a background sub-agent. " +
		"Returns immediately with the final task status. Stopping an already-finished task succeeds without effect."
}

func (t *taskStopTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "task_id": {"type": "string", "description": "The ID of the task to stop."}
  },
  "required": ["task_id"],
  "additionalProperties": false
}`)
}

func (t *taskStopTool) ReadOnly(json.RawMessage) bool { return false }

func (t *taskStopTool) ConcurrencySafe(json.RawMessage) bool { return false }

func (t *taskStopTool) AlwaysAllow() bool { return true }

func (t *taskStopTool) Run(ctx context.Context, input json.RawMessage, progress tool.ProgressFunc) (tool.Result, error) {
	var in struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return tool.Result{}, fmt.Errorf("TaskStop: decode input: %w", err)
	}
	if strings.TrimSpace(in.TaskID) == "" {
		return tool.Result{}, fmt.Errorf("TaskStop: task_id is required")
	}
	snapshot, err := t.reg.Stop(in.TaskID)
	if err != nil {
		return tool.Result{IsError: true, Content: "No task found with ID " + in.TaskID + ". Use TaskList to see active tasks."}, nil
	}
	content := fmt.Sprintf("Successfully stopped task: %s (%s). Final status: %s.",
		snapshot.ID, snapshot.Description, snapshot.Status)
	return tool.Result{Content: content, Display: content}, nil
}

// taskListTool 实现 TaskList。
type taskListTool struct {
	reg *Registry
}

var _ tool.Tool = (*taskListTool)(nil)
var _ tool.AlwaysAllow = (*taskListTool)(nil)

func (t *taskListTool) Name() string { return "TaskList" }

func (t *taskListTool) Description() string {
	return "List background tasks (background Bash commands and background sub-agents) with their IDs, status and descriptions. " +
		"Use TaskOutput with a task ID to retrieve output, TaskStop to stop a task."
}

func (t *taskListTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (t *taskListTool) ReadOnly(json.RawMessage) bool { return true }

func (t *taskListTool) ConcurrencySafe(json.RawMessage) bool { return true }

func (t *taskListTool) AlwaysAllow() bool { return true }

func (t *taskListTool) Run(ctx context.Context, input json.RawMessage, progress tool.ProgressFunc) (tool.Result, error) {
	tasks := t.reg.List()
	if len(tasks) == 0 {
		content := "No background tasks."
		return tool.Result{Content: content, Display: content}, nil
	}
	var b strings.Builder
	b.WriteString("task_id | type | status | description\n")
	for _, tk := range tasks {
		desc := tk.Description
		if tk.Kind == KindBash {
			desc = firstNonEmpty(tk.Description, tk.Command)
		}
		fmt.Fprintf(&b, "%s | %s | %s | %s\n", tk.ID, tk.Kind, tk.Status, desc)
	}
	content := strings.TrimRight(b.String(), "\n")
	return tool.Result{Content: content, Display: content}, nil
}

// FormatBashNotification 渲染后台命令的 <task-notification> XML。
func FormatBashNotification(t Task) string {
	summary := fmt.Sprintf("Background command %q", t.Description)
	status := string(t.Status)
	switch t.Status {
	case StatusCompleted:
		summary += fmt.Sprintf(" completed (exit code %d)", t.ExitCode)
	case StatusFailed:
		summary += fmt.Sprintf(" failed (exit code %d)", t.ExitCode)
		if t.Error != "" {
			summary += ": " + t.Error
		}
	case StatusStopped:
		summary += " was stopped"
	}
	var b strings.Builder
	b.WriteString("<task-notification>\n")
	fmt.Fprintf(&b, "<task_id>%s</task_id>\n", t.ID)
	if t.OutputFile != "" {
		fmt.Fprintf(&b, "<output_file>%s</output_file>\n", t.OutputFile)
	}
	fmt.Fprintf(&b, "<status>%s</status>\n", status)
	fmt.Fprintf(&b, "<summary>%s</summary>\n", summary)
	if text, _, _, err := readTail(t.OutputFile, resultPreviewChars); err == nil && strings.TrimSpace(text) != "" {
		fmt.Fprintf(&b, "<result>\n%s\n</result>\n", strings.TrimRight(text, "\n"))
	}
	b.WriteString("</task-notification>")
	return b.String()
}

// clampChars 把文本截到 max 字符（不切断 UTF-8），超长保尾。
func clampChars(text string, max int) string {
	if max <= 0 || len(text) <= max {
		return text
	}
	text = text[len(text)-max:]
	for len(text) > 0 && !isRuneStart(text[0]) {
		text = text[1:]
	}
	return text
}
