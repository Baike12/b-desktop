package app

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Baike12/b-desktop/internal/agent"
	"github.com/Baike12/b-desktop/internal/task"
)

// taskStoreKey 是扩展组件缓存中任务注册表的 key。
const taskStoreKey = "tasks"

// 绑定层错误哨兵。
var (
	// ErrTaskNotFound 表示后台任务 ID 不存在。
	ErrTaskNotFound = fmt.Errorf("app: task not found")
	// ErrTaskIDRequired 表示操作后台任务时未提供任务 ID。
	ErrTaskIDRequired = fmt.Errorf("app: task id required")
)

// BackgroundTaskDTO 是一个后台任务的绑定视图。
type BackgroundTaskDTO struct {
	// ID 是任务标识。
	ID string `json:"id"`
	// Kind 取值 local_bash、local_agent。
	Kind string `json:"kind"`
	// SessionID 是发起任务的会话。
	SessionID string `json:"sessionId"`
	// Description 是任务描述。
	Description string `json:"description"`
	// Command 是后台命令（local_bash）。
	Command string `json:"command,omitempty"`
	// AgentType 是子 Agent 类型（local_agent）。
	AgentType string `json:"agentType,omitempty"`
	// Status 取值 running、completed、failed、stopped。
	Status string `json:"status"`
	// StartedAt 与 FinishedAt 是 RFC3339Nano 时间；运行中 FinishedAt 为空。
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt,omitempty"`
	// ExitCode 是命令退出码（local_bash）。
	ExitCode int `json:"exitCode,omitempty"`
	// Error 是失败原因。
	Error string `json:"error,omitempty"`
	// Result 是子 Agent 的最终文本（local_agent）。
	Result string `json:"result,omitempty"`
	// OutputFile 是后台输出文件路径（local_bash）。
	OutputFile string `json:"outputFile,omitempty"`
}

// taskRegistryComponent 返回进程级后台任务注册表（惰性创建并复用）。
// 任务完成时回调 onTaskCompleted：推前端事件并唤醒空闲会话。
func (a *App) taskRegistryComponent() (*task.Registry, error) {
	value, err := a.extensionStore(taskStoreKey, func() (any, error) {
		dir, err := a.settingsDir()
		if err != nil {
			return nil, err
		}
		reg, err := task.NewRegistry(task.Config{
			OutputDir:  filepath.Join(dir, "tasks"),
			OnComplete: a.onTaskCompleted,
			Now:        time.Now,
		})
		if err != nil {
			return nil, err
		}
		return reg, nil
	})
	if err != nil {
		return nil, fmt.Errorf("app: task registry: %w", err)
	}
	reg, ok := value.(*task.Registry)
	if !ok {
		return nil, fmt.Errorf("app: task registry: unexpected type %T", value)
	}
	return reg, nil
}

// onTaskCompleted 是任务注册表的完成回调：推前端 task_update 事件，
// 并把 <task-notification> 投递给发起会话——运行中走引擎通知队列
// （循环在工具边界注入），空闲则自动唤醒续跑。
func (a *App) onTaskCompleted(t task.Task) {
	if a.isClosed() {
		return
	}
	text := task.FormatBashNotification(t)
	if t.Kind == task.KindAgent {
		text = formatAgentTaskNotification(t)
	}
	a.emitTaskUpdate(t, summaryOfNotification(text))

	rt, err := a.runtimeOf(t.SessionID)
	if err != nil {
		// 发起会话未知（如应用重启后）：通知无处投递，只保留前端事件。
		return
	}
	rt.mu.Lock()
	running := rt.running
	rt.mu.Unlock()
	if running {
		rt.notifs.Push(text)
		return
	}
	if _, err := a.SendMessage(t.SessionID, text, nil); err != nil {
		// 会话刚被用户或另一条消息占用：转入队列，由运行中的轮次注入。
		rt.notifs.Push(text)
	}
}

// emitTaskUpdate 向发起会话推送一条 task_update 事件。
func (a *App) emitTaskUpdate(t task.Task, summary string) {
	base := a.lifeCtx()
	a.emitter.Emit(base, agent.Event{
		Type:      agent.EventTaskUpdate,
		SessionID: t.SessionID,
		TimeMS:    time.Now().UnixMilli(),
		Task: &agent.TaskView{
			TaskID:      t.ID,
			Kind:        string(t.Kind),
			Description: t.Description,
			Command:     t.Command,
			AgentType:   t.AgentType,
			Status:      string(t.Status),
			ExitCode:    t.ExitCode,
			Error:       t.Error,
			Result:      t.Result,
			OutputFile:  t.OutputFile,
			Summary:     summary,
		},
	})
}

// formatAgentTaskNotification 渲染子 Agent 任务的 <task-notification> XML。
func formatAgentTaskNotification(t task.Task) string {
	var b strings.Builder
	b.WriteString("<task-notification>\n")
	fmt.Fprintf(&b, "<task_id>%s</task_id>\n", t.ID)
	fmt.Fprintf(&b, "<status>%s</status>\n", mapTaskStatus(t.Status))
	fmt.Fprintf(&b, "<summary>Agent %q %s</summary>\n", t.Description, agentSummaryVerb(t.Status))
	if strings.TrimSpace(t.Result) != "" {
		fmt.Fprintf(&b, "<result>\n%s\n</result>\n", truncateRunes(t.Result, 4000))
	}
	if t.Error != "" {
		fmt.Fprintf(&b, "<error>%s</error>\n", t.Error)
	}
	b.WriteString("</task-notification>")
	return b.String()
}

// agentSummaryVerb 把任务状态映射成通知摘要动词。
func agentSummaryVerb(s task.Status) string {
	switch s {
	case task.StatusCompleted:
		return "completed"
	case task.StatusFailed:
		return "failed"
	default:
		return "was stopped"
	}
}

// mapTaskStatus 把注册表状态映射成通知 status 字段。
func mapTaskStatus(s task.Status) string {
	switch s {
	case task.StatusStopped:
		return "stopped"
	default:
		return string(s)
	}
}

// summaryOfNotification 从通知 XML 中提取 <summary> 文本。
func summaryOfNotification(text string) string {
	start := strings.Index(text, "<summary>")
	end := strings.Index(text, "</summary>")
	if start < 0 || end < 0 || end <= start {
		return ""
	}
	return text[start+len("<summary>") : end]
}

// truncateRunes 把文本截到 max 字符（不切断 UTF-8），超长保头。
func truncateRunes(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "\n…[truncated]"
}

// ListBackgroundTasks 返回全部后台任务（后台命令与后台子 Agent），按开始时间倒序。
func (a *App) ListBackgroundTasks() ([]BackgroundTaskDTO, error) {
	if a.isClosed() {
		return nil, ErrClosed
	}
	reg, err := a.taskRegistryComponent()
	if err != nil {
		return nil, err
	}
	tasks := reg.List()
	out := make([]BackgroundTaskDTO, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, toBackgroundTaskDTO(t))
	}
	return out, nil
}

// StopBackgroundTask 终止一个后台任务；任务已结束时视为成功。
func (a *App) StopBackgroundTask(taskID string) error {
	id := strings.TrimSpace(taskID)
	if id == "" {
		return ErrTaskIDRequired
	}
	if a.isClosed() {
		return ErrClosed
	}
	reg, err := a.taskRegistryComponent()
	if err != nil {
		return err
	}
	if _, err := reg.Stop(id); err != nil {
		return fmt.Errorf("%w: %s", ErrTaskNotFound, id)
	}
	return nil
}

// toBackgroundTaskDTO 把领域任务转成绑定层视图。
func toBackgroundTaskDTO(t task.Task) BackgroundTaskDTO {
	return BackgroundTaskDTO{
		ID:          t.ID,
		Kind:        string(t.Kind),
		SessionID:   t.SessionID,
		Description: t.Description,
		Command:     t.Command,
		AgentType:   t.AgentType,
		Status:      string(t.Status),
		StartedAt:   formatSubagentTime(t.StartedAt),
		FinishedAt:  formatSubagentTime(t.FinishedAt),
		ExitCode:    t.ExitCode,
		Error:       t.Error,
		Result:      t.Result,
		OutputFile:  t.OutputFile,
	}
}
