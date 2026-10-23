package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Baike12/b-desktop/internal/task"
)

// EventNameTaskFire 是定时任务触发时推送的 Wails 事件名，payload 为 TaskFireDTO。
const EventNameTaskFire = "task:fire"

// 定时任务绑定层的错误哨兵。
var (
	// ErrTasksUnavailable 表示调度器无法初始化或类型异常。
	ErrTasksUnavailable = errors.New("app: tasks unavailable")
	// ErrTaskIDRequired 表示任务 ID 为空。
	ErrTaskIDRequired = errors.New("app: task id required")
	// ErrTaskNotFound 表示指定 ID 的任务不存在。
	ErrTaskNotFound = errors.New("app: task not found")
	// ErrTaskInvalid 表示任务字段非法（cron 表达式或提示词不合法）。
	ErrTaskInvalid = errors.New("app: task invalid")
)

// TaskDTO 是定时任务视图，时间为 Unix 毫秒。
type TaskDTO struct {
	ID      string `json:"id"`
	Cron    string `json:"cron"`
	Prompt  string `json:"prompt"`
	WorkDir string `json:"workDir,omitempty"`
	Enabled bool   `json:"enabled"`
	// CreatedAtMS 是创建时间，也是首次计算触发时间的锚点。
	CreatedAtMS int64 `json:"createdAtMs"`
	// LastRunAtMS 是最近一次触发时间；0 表示尚未触发。
	LastRunAtMS int64 `json:"lastRunAtMs,omitempty"`
	// NextRunAtMS 是下次触发时间；0 表示不会触发（已禁用或一年内无匹配）。
	// 早于当前时间表示任务已到点、等待下一次检查补触发。
	NextRunAtMS int64 `json:"nextRunAtMs,omitempty"`
}

// CreateTaskInput 是 CreateTask 的入参。Enabled 省略时默认启用；
// WorkDir 为空时使用应用默认工作目录。
type CreateTaskInput struct {
	Cron    string `json:"cron"`
	Prompt  string `json:"prompt"`
	WorkDir string `json:"workDir,omitempty"`
	Enabled *bool  `json:"enabled,omitempty"`
}

// TaskFireDTO 是一次任务触发事件的 payload。
type TaskFireDTO struct {
	Task      TaskDTO `json:"task"`
	FiredAtMS int64   `json:"firedAtMs"`
}

// taskComponent 返回进程内唯一的定时任务调度器。
// 任务持久化在 ~/.b-desktop/tasks.json；触发回调通过事件推送给前端。
func (a *App) taskComponent() (*task.Scheduler, error) {
	value, err := a.extensionStore("task", func() (any, error) {
		settings, err := a.settingsDir()
		if err != nil {
			return nil, err
		}
		store, err := task.NewStore(filepath.Join(settings, "tasks.json"))
		if err != nil {
			return nil, err
		}
		// sched 在回调执行前完成赋值：Fire 只会在 Start 之后被调用，
		// 而 Start 由集成方在组件构造成功后触发。
		var sched *task.Scheduler
		s, err := task.NewScheduler(task.SchedulerConfig{
			Store: store,
			Fire: func(ctx context.Context, t task.Task) {
				next := int64(0)
				if sched != nil {
					if at, ok := sched.NextRun(t); ok {
						next = at.UnixMilli()
					}
				}
				a.emitTaskFire(ctx, t, next)
			},
		})
		if err != nil {
			return nil, err
		}
		sched = s
		return s, nil
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrTasksUnavailable, err)
	}
	scheduler, ok := value.(*task.Scheduler)
	if !ok {
		return nil, fmt.Errorf("%w: unexpected component type %T", ErrTasksUnavailable, value)
	}
	return scheduler, nil
}

// ListTasks 返回全部定时任务及其下次触发时间。
func (a *App) ListTasks() ([]TaskDTO, error) {
	scheduler, err := a.taskComponent()
	if err != nil {
		return nil, err
	}
	views := scheduler.List()
	out := make([]TaskDTO, 0, len(views))
	for _, view := range views {
		out = append(out, toTaskDTO(view))
	}
	return out, nil
}

// CreateTask 校验并创建一个定时任务，返回带下次触发时间的视图。
func (a *App) CreateTask(in CreateTaskInput) (TaskDTO, error) {
	scheduler, err := a.taskComponent()
	if err != nil {
		return TaskDTO{}, err
	}
	cfg, _ := a.configSnapshot()
	workDir := strings.TrimSpace(in.WorkDir)
	if workDir == "" {
		workDir = cfg.WorkDir
	}
	view, err := scheduler.Create(task.NewTaskInput{
		Cron:    in.Cron,
		Prompt:  in.Prompt,
		WorkDir: workDir,
		Enabled: in.Enabled,
	})
	if err != nil {
		if errors.Is(err, task.ErrInvalidTask) {
			return TaskDTO{}, fmt.Errorf("%w: %w", ErrTaskInvalid, err)
		}
		return TaskDTO{}, fmt.Errorf("app: create task: %w", err)
	}
	return toTaskDTO(view), nil
}

// DeleteTask 删除指定任务。
func (a *App) DeleteTask(id string) error {
	scheduler, err := a.taskComponent()
	if err != nil {
		return err
	}
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return fmt.Errorf("%w: empty id", ErrTaskIDRequired)
	}
	if err := scheduler.Delete(trimmed); err != nil {
		if errors.Is(err, task.ErrNotFound) {
			return fmt.Errorf("%w: %s", ErrTaskNotFound, trimmed)
		}
		return fmt.Errorf("app: delete task %s: %w", trimmed, err)
	}
	return nil
}

// SetTaskEnabled 启用或禁用指定任务；禁用后调度器跳过该任务。
func (a *App) SetTaskEnabled(id string, enabled bool) error {
	scheduler, err := a.taskComponent()
	if err != nil {
		return err
	}
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return fmt.Errorf("%w: empty id", ErrTaskIDRequired)
	}
	if err := scheduler.SetEnabled(trimmed, enabled); err != nil {
		if errors.Is(err, task.ErrNotFound) {
			return fmt.Errorf("%w: %s", ErrTaskNotFound, trimmed)
		}
		return fmt.Errorf("app: set task %s enabled: %w", trimmed, err)
	}
	return nil
}

// emitTaskFire 把一次任务触发推送给前端。
func (a *App) emitTaskFire(ctx context.Context, t task.Task, nextRunAtMS int64) {
	if a == nil || a.emitter == nil {
		return
	}
	a.emitter.emitEvent(ctx, EventNameTaskFire, TaskFireDTO{
		Task: TaskDTO{
			ID:          t.ID,
			Cron:        t.Cron,
			Prompt:      t.Prompt,
			WorkDir:     t.WorkDir,
			Enabled:     t.Enabled,
			CreatedAtMS: t.CreatedAtMS,
			LastRunAtMS: t.LastRunAtMS,
			NextRunAtMS: nextRunAtMS,
		},
		FiredAtMS: t.LastRunAtMS,
	})
}

// emitEvent 发送一个不属于 Agent 事件流的事件。payload 必须可 JSON 序列化。
// 与 Emit 共用同一把锁，保证同一发射器上的事件顺序稳定。
func (e *Emitter) emitEvent(ctx context.Context, name string, payload any) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.snd != nil {
		e.snd(ctx, name, payload)
	}
}

// toTaskDTO 把领域视图转换为绑定层 DTO。
func toTaskDTO(view task.TaskView) TaskDTO {
	return TaskDTO{
		ID:          view.ID,
		Cron:        view.Cron,
		Prompt:      view.Prompt,
		WorkDir:     view.WorkDir,
		Enabled:     view.Enabled,
		CreatedAtMS: view.CreatedAtMS,
		LastRunAtMS: view.LastRunAtMS,
		NextRunAtMS: view.NextRunAtMS,
	}
}
