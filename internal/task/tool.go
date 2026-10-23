package task

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Baike12/b-desktop/internal/tool"
)

// maxScheduledTasks 是允许同时存在的任务上限，防止模型无限堆积任务。
const maxScheduledTasks = 50

// scheduleCronSchema 是 ScheduleCron 的输入 schema。
var scheduleCronSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "cron": {
      "type": "string",
      "description": "Standard 5-field cron expression in local time: \"minute hour day-of-month month day-of-week\". Examples: \"*/5 * * * *\" every 5 minutes, \"30 14 * * 1-5\" weekdays at 14:30."
    },
    "prompt": {
      "type": "string",
      "description": "Prompt to run each time the schedule fires."
    },
    "workDir": {
      "type": "string",
      "description": "Absolute working directory for the task. Optional; omit to use the current session directory."
    }
  },
  "required": ["cron", "prompt"]
}`)

// scheduleCronInput 是 ScheduleCron 的输入。
type scheduleCronInput struct {
	Cron    string `json:"cron"`
	Prompt  string `json:"prompt"`
	WorkDir string `json:"workDir"`
}

// scheduleCronTool 把模型请求的定时任务写入 Scheduler 的存储。
type scheduleCronTool struct {
	sched *Scheduler
}

// newScheduleCronTool 创建 ScheduleCron 工具。
func newScheduleCronTool(sched *Scheduler) tool.Tool {
	return &scheduleCronTool{sched: sched}
}

// Name 返回工具名。
func (t *scheduleCronTool) Name() string { return "ScheduleCron" }

// Description 返回给模型看的说明。
func (t *scheduleCronTool) Description() string {
	return "Schedule a recurring prompt on a 5-field cron expression (minute hour day-of-month month day-of-week) " +
		"interpreted in local time. The task is persisted to disk and fires on every match until it is deleted " +
		"or disabled. Returns the task id and the next run time. Use a one-off tight cron only for tasks that " +
		"genuinely repeat; do not schedule work that blocks the current turn."
}

// InputSchema 返回 JSON Schema。
func (t *scheduleCronTool) InputSchema() json.RawMessage { return scheduleCronSchema }

// ReadOnly 报告该工具会写入任务文件，恒为 false。
func (t *scheduleCronTool) ReadOnly(json.RawMessage) bool { return false }

// ConcurrencySafe 报告并发安全。存储层有互斥锁，可与其它调用并行。
func (t *scheduleCronTool) ConcurrencySafe(json.RawMessage) bool { return true }

// Run 校验输入并创建任务。
func (t *scheduleCronTool) Run(_ context.Context, input json.RawMessage, _ tool.ProgressFunc) (tool.Result, error) {
	var in scheduleCronInput
	if err := json.Unmarshal(input, &in); err != nil {
		return tool.Result{IsError: true, Content: "invalid input: " + err.Error()}, nil
	}
	cronExpr := strings.TrimSpace(in.Cron)
	if cronExpr == "" {
		return tool.Result{IsError: true, Content: "cron is required"}, nil
	}
	cron, err := ParseCron(cronExpr)
	if err != nil {
		return tool.Result{IsError: true, Content: fmt.Sprintf("invalid cron %q: %v", in.Cron, err)}, nil
	}
	if _, ok := cron.Next(time.Now()); !ok {
		return tool.Result{IsError: true, Content: fmt.Sprintf("cron %q never matches a calendar date", in.Cron)}, nil
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return tool.Result{IsError: true, Content: "prompt is required"}, nil
	}
	workDir := strings.TrimSpace(in.WorkDir)
	if workDir != "" {
		abs, err := filepath.Abs(workDir)
		if err != nil {
			return tool.Result{IsError: true, Content: fmt.Sprintf("resolve workDir: %v", err)}, nil
		}
		info, err := os.Stat(abs)
		if err != nil {
			return tool.Result{IsError: true, Content: fmt.Sprintf("workDir %q: %v", abs, err)}, nil
		}
		if !info.IsDir() {
			return tool.Result{IsError: true, Content: fmt.Sprintf("workDir %q is not a directory", abs)}, nil
		}
		workDir = abs
	}
	if len(t.sched.store.List()) >= maxScheduledTasks {
		return tool.Result{
			IsError: true,
			Content: fmt.Sprintf("too many scheduled tasks (max %d); delete one first", maxScheduledTasks),
		}, nil
	}

	view, err := t.sched.Create(NewTaskInput{
		Cron:    cronExpr,
		Prompt:  in.Prompt,
		WorkDir: workDir,
	})
	if err != nil {
		return tool.Result{IsError: true, Content: "schedule failed: " + err.Error()}, nil
	}
	next := "not scheduled"
	if view.NextRunAtMS > 0 {
		next = time.UnixMilli(view.NextRunAtMS).Format(time.RFC3339)
	}
	content := fmt.Sprintf("Scheduled task %s with cron %q. Next run: %s.", view.ID, view.Cron, next)
	return tool.Result{
		Content: content,
		Meta:    map[string]string{"taskId": view.ID, "cron": view.Cron},
	}, nil
}
