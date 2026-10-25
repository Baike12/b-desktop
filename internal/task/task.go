// Package task 实现后台任务的统一注册表：Bash 后台进程与子 Agent 任务共用
// 一张任务表，TaskOutput / TaskStop / TaskList 工具据此查询与终止。
//
// 任务完成时通过 OnComplete 回调把 <task-notification> 交给装配层，由它决定
// 注入运行中会话的通知队列或唤醒空闲会话；回调节度在独立 goroutine 上执行。
package task

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 模块错误哨兵。调用方可用 errors.Is 区分。
var (
	// ErrNotFound 表示任务 ID 不存在。
	ErrNotFound = errors.New("task: not found")
	// ErrClosed 表示 Registry 已关闭，不再接受新任务。
	ErrClosed = errors.New("task: registry closed")
	// ErrOutputUnavailable 表示该任务没有可读的输出（尚未开始或无输出文件）。
	ErrOutputUnavailable = errors.New("task: output unavailable")
)

// Kind 是任务类型。
type Kind string

// 任务类型常量。
const (
	KindBash  Kind = "local_bash"  // KindBash 是后台命令任务。
	KindAgent Kind = "local_agent" // KindAgent 是后台子 Agent 任务。
)

// Status 是任务状态。
type Status string

// 任务状态常量。
const (
	StatusRunning   Status = "running"   // StatusRunning 表示任务正在执行。
	StatusCompleted Status = "completed" // StatusCompleted 表示任务正常结束。
	StatusFailed    Status = "failed"    // StatusFailed 表示任务以错误结束。
	StatusStopped   Status = "stopped"   // StatusStopped 表示任务被 TaskStop 或关闭终止。
)

// Terminal 报告任务是否处于终态。
func (s Status) Terminal() bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusStopped
}

// Task 是一个任务的快照。字段按 Kind 取用：Bash 用 Command/ExitCode/OutputFile，
// Agent 用 AgentType/Result；Description/Status/Error 通用。
type Task struct {
	ID          string
	Kind        Kind
	SessionID   string
	Description string
	Command     string
	AgentType   string
	Status      Status
	StartedAt   time.Time
	FinishedAt  time.Time
	ExitCode    int
	Error       string
	Result      string
	OutputFile  string
}

// Config 是 Registry 的构造参数。
type Config struct {
	// OutputDir 是后台命令输出文件的存放目录，必须非空。
	OutputDir string
	// OnComplete 在任务进入终态且未被抑制通知时调用，交给装配层做通知分发。
	// 实现必须快速返回；Registry 在独立 goroutine 上调用它。
	OnComplete func(Task)
	// Now 是时间源，nil 时使用 time.Now。
	Now func() time.Time
}

// entry 是任务的可变内部状态。
type entry struct {
	Task
	mu             sync.Mutex
	cancel         context.CancelFunc
	done           chan struct{}
	suppressNotify bool
	progress       []string // agent 任务的进度行（环形，最新在尾部）
}

// Registry 管理后台任务的生命周期。所有方法并发安全。
type Registry struct {
	cfg    Config
	rootMu sync.Mutex
	root   context.Context
	cancel context.CancelFunc

	mu    sync.Mutex
	tasks map[string]*entry
	seq   []string
}

// NewRegistry 创建注册表并确保 OutputDir 存在。
func NewRegistry(cfg Config) (*Registry, error) {
	if strings.TrimSpace(cfg.OutputDir) == "" {
		return nil, fmt.Errorf("task: output dir is required")
	}
	if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
		return nil, fmt.Errorf("task: create output dir %s: %w", cfg.OutputDir, err)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Registry{
		cfg:    cfg,
		root:   ctx,
		cancel: cancel,
		tasks:  make(map[string]*entry),
	}, nil
}

// register 登记一个任务并返回它的 entry；调用方负责填充 cancel。
func (r *Registry) register(t Task) *entry {
	e := &entry{Task: t, done: make(chan struct{})}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tasks[t.ID] = e
	r.seq = append(r.seq, t.ID)
	return e
}

// Get 按 ID 返回任务快照。
func (r *Registry) Get(id string) (Task, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.tasks[id]
	if !ok {
		return Task{}, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.Task, true
}

// List 按开始时间倒序返回全部任务快照。
func (r *Registry) List() []Task {
	r.mu.Lock()
	entries := make([]*entry, 0, len(r.seq))
	for _, id := range r.seq {
		if e, ok := r.tasks[id]; ok {
			entries = append(entries, e)
		}
	}
	r.mu.Unlock()
	out := make([]Task, 0, len(entries))
	for _, e := range entries {
		e.mu.Lock()
		out = append(out, e.Task)
		e.mu.Unlock()
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.After(out[j].StartedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// AppendProgress 给 agent 任务追加一条进度行；只保留最近 50 条。
func (r *Registry) AppendProgress(id, line string) {
	r.mu.Lock()
	e, ok := r.tasks[id]
	r.mu.Unlock()
	if !ok {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.progress = append(e.progress, line)
	if len(e.progress) > 50 {
		e.progress = e.progress[len(e.progress)-50:]
	}
}

// Progress 返回任务的进度行快照。
func (r *Registry) Progress(id string) []string {
	r.mu.Lock()
	e, ok := r.tasks[id]
	r.mu.Unlock()
	if !ok {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.progress))
	copy(out, e.progress)
	return out
}

// Stop 终止一个运行中的任务：Bash 杀进程树，Agent 调用注册时的 cancel。
// 任务不存在返回 ErrNotFound；已终态视为成功。
// Bash 任务被主动停止时抑制完成通知（模型已直接拿到 TaskStop 结果）；
// Agent 任务保留通知，把已产出的部分结果带给模型。
func (r *Registry) Stop(id string) (Task, error) {
	r.mu.Lock()
	e, ok := r.tasks[id]
	r.mu.Unlock()
	if !ok {
		return Task{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	e.mu.Lock()
	if !e.Status.Terminal() {
		if e.Kind == KindBash {
			e.suppressNotify = true
		}
		if e.cancel != nil {
			e.cancel()
		}
	}
	e.mu.Unlock()
	// 等执行侧收尾，保证返回时状态已落定；正常收尾是毫秒级。
	select {
	case <-e.done:
	case <-time.After(5 * time.Second):
	}
	t, _ := r.Get(id)
	return t, nil
}

// WaitTerminal 阻塞等待任务进入终态或超时。返回当前快照与是否已终态。
func (r *Registry) WaitTerminal(id string, timeout time.Duration) (Task, bool) {
	r.mu.Lock()
	e, ok := r.tasks[id]
	r.mu.Unlock()
	if !ok {
		return Task{}, false
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-e.done:
	case <-timer.C:
	}
	t, _ := r.Get(id)
	return t, t.Status.Terminal()
}

// finish 写入终态并按需回调 OnComplete。由 bash 执行侧与 agent handle 调用。
func (r *Registry) finish(e *entry, status Status, apply func(*Task)) {
	e.mu.Lock()
	if e.Status.Terminal() {
		e.mu.Unlock()
		return
	}
	e.Status = status
	e.FinishedAt = r.now()
	if apply != nil {
		apply(&e.Task)
	}
	e.cancel = nil
	suppress := e.suppressNotify
	snapshot := e.Task
	close(e.done)
	e.mu.Unlock()

	if suppress || r.cfg.OnComplete == nil {
		return
	}
	go r.cfg.OnComplete(snapshot)
}

func (r *Registry) now() time.Time {
	if r.cfg.Now != nil {
		return r.cfg.Now()
	}
	return time.Now()
}

// Close 终止全部运行中的任务并抑制它们的完成通知（应用正在退出）。
// 输出文件保留在磁盘上供排查。
func (r *Registry) Close() error {
	r.rootMu.Lock()
	r.cancel()
	r.rootMu.Unlock()

	r.mu.Lock()
	entries := make([]*entry, 0, len(r.tasks))
	for _, e := range r.tasks {
		entries = append(entries, e)
	}
	r.mu.Unlock()
	for _, e := range entries {
		e.mu.Lock()
		if !e.Status.Terminal() {
			e.suppressNotify = true
		}
		if e.cancel != nil {
			e.cancel()
		}
		e.mu.Unlock()
		select {
		case <-e.done:
		case <-time.After(2 * time.Second):
		}
	}
	return nil
}

// OutputDir 返回后台输出文件目录。
func (r *Registry) OutputDir() string { return r.cfg.OutputDir }

// outputPath 返回任务输出文件路径。
func (r *Registry) outputPath(id string) string {
	return filepath.Join(r.cfg.OutputDir, id+".output")
}
