package task

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Baike12/b-desktop/internal/tool"
)

// FireFunc 在任务到点时被调用。实现必须快速返回：它运行在调度循环里，
// 耗时工作应当自行转到后台 goroutine。ctx 在 Scheduler 关闭时被取消。
type FireFunc func(ctx context.Context, t Task)

// FireRecord 是一条触发记录，用于诊断与前端展示。
type FireRecord struct {
	// Task 是触发时的任务快照。
	Task Task `json:"task"`
	// At 是触发时间。
	At time.Time `json:"at"`
}

// SchedulerConfig 是 Scheduler 的构造参数。
type SchedulerConfig struct {
	// Store 是任务存储，必填。
	Store *Store
	// Fire 是触发回调；nil 表示只记录触发历史。
	Fire FireFunc
	// Now 是时钟；nil 表示 time.Now。注入时钟用于测试。
	Now func() time.Time
	// Interval 是两次检查之间的固定间隔；0 表示对齐到分钟边界。
	// 正常运行时保持 0；测试可以注入很短的间隔来验证循环。
	Interval time.Duration
}

// TaskView 是带下次触发时间的任务视图。
type TaskView struct {
	Task
	// NextRunAtMS 是下次触发时间（Unix 毫秒）；0 表示不会触发
	// （任务已禁用，或表达式在一年内没有匹配）。
	// 值早于当前时间表示任务已到点，等待下一次检查补触发。
	NextRunAtMS int64 `json:"nextRunAtMs"`
}

// Scheduler 按分钟检查任务并按 cron 触发回调。所有方法并发安全。
// Start 与 Close 可重复调用；Close 之后再 Start 会重新开始调度。
type Scheduler struct {
	store    *Store
	fire     FireFunc
	now      func() time.Time
	interval time.Duration
	tools    []tool.Tool

	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	done    chan struct{}
	fires   []FireRecord
	lastErr error
}

// maxFireRecords 是保留的触发记录上限，防止长时间运行后无限增长。
const maxFireRecords = 100

// NewScheduler 创建调度器。
func NewScheduler(cfg SchedulerConfig) (*Scheduler, error) {
	if cfg.Store == nil {
		return nil, errors.New("task: scheduler requires a store")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	s := &Scheduler{
		store:    cfg.Store,
		fire:     cfg.Fire,
		now:      now,
		interval: cfg.Interval,
	}
	s.tools = []tool.Tool{newScheduleCronTool(s)}
	return s, nil
}

// Store 返回底层任务存储。
func (s *Scheduler) Store() *Store { return s.store }

// Now 返回调度器当前使用的时钟时间。
func (s *Scheduler) Now() time.Time { return s.now() }

// Tools 返回调度器提供的工具（当前为 ScheduleCron）。返回值是副本。
func (s *Scheduler) Tools() []tool.Tool {
	out := make([]tool.Tool, len(s.tools))
	copy(out, s.tools)
	return out
}

// List 返回任务视图，附带下次触发时间。
func (s *Scheduler) List() []TaskView {
	tasks := s.store.List()
	out := make([]TaskView, 0, len(tasks))
	for _, t := range tasks {
		view := TaskView{Task: t}
		if next, ok := s.NextRun(t); ok {
			view.NextRunAtMS = next.UnixMilli()
		}
		out = append(out, view)
	}
	return out
}

// NextRun 计算任务的下一次触发时间。任务禁用、cron 非法或一年内无匹配时返回 false。
// 计算锚点与 Check 一致：有上次触发时间用上次触发时间，否则用创建时间。
func (s *Scheduler) NextRun(t Task) (time.Time, bool) {
	if !t.Enabled {
		return time.Time{}, false
	}
	cron, err := ParseCron(t.Cron)
	if err != nil {
		return time.Time{}, false
	}
	anchor := time.UnixMilli(t.CreatedAtMS)
	if t.LastRunAtMS > 0 {
		anchor = time.UnixMilli(t.LastRunAtMS)
	}
	return cron.Next(anchor)
}

// Create 创建任务并返回带下次触发时间的视图。
func (s *Scheduler) Create(in NewTaskInput) (TaskView, error) {
	t, err := s.store.Create(in)
	if err != nil {
		return TaskView{}, err
	}
	view := TaskView{Task: t}
	if next, ok := s.NextRun(t); ok {
		view.NextRunAtMS = next.UnixMilli()
	}
	return view, nil
}

// Delete 删除任务。
func (s *Scheduler) Delete(id string) error { return s.store.Delete(id) }

// SetEnabled 启用或禁用任务。
func (s *Scheduler) SetEnabled(id string, enabled bool) error {
	return s.store.SetEnabled(id, enabled)
}

// Check 用给定时间检查一次全部任务，触发到点且启用的任务并返回它们。
// 每个任务一次检查最多触发一次；触发后把 now 记为上次运行时间，
// 因此进程休眠期间错过的多次触发只会补一次，不逐分钟补齐。
func (s *Scheduler) Check(ctx context.Context, now time.Time) ([]Task, error) {
	tasks := s.store.List()
	var (
		fired []Task
		errs  []error
	)
	for _, t := range tasks {
		if !t.Enabled {
			continue
		}
		cron, err := ParseCron(t.Cron)
		if err != nil {
			errs = append(errs, fmt.Errorf("task: task %s: %w", t.ID, err))
			continue
		}
		anchor := time.UnixMilli(t.CreatedAtMS)
		if t.LastRunAtMS > 0 {
			anchor = time.UnixMilli(t.LastRunAtMS)
		}
		next, ok := cron.Next(anchor)
		if !ok || next.After(now) {
			continue
		}
		if err := s.store.MarkFired(t.ID, now); err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			errs = append(errs, fmt.Errorf("task: mark %s fired: %w", t.ID, err))
			continue
		}
		t.LastRunAtMS = now.UnixMilli()
		fired = append(fired, t)
		s.recordFire(t, now)
		if s.fire != nil {
			if err := s.callFire(ctx, t); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return fired, errors.Join(errs...)
}

// callFire 调用触发回调并把 panic 转成错误，避免调度循环被用户回调打断。
func (s *Scheduler) callFire(ctx context.Context, t Task) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("task: fire %s: panic: %v", t.ID, r)
		}
	}()
	s.fire(ctx, t)
	return nil
}

// Tick 用注入的时钟执行一次检查。Start 的循环每次等待结束都会调用它。
func (s *Scheduler) Tick(ctx context.Context) ([]Task, error) {
	return s.Check(ctx, s.now())
}

// Start 启动后台调度循环：立即检查一次，之后每分钟检查一次。
// 已经在运行时调用是空操作。
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.running = true
	s.cancel = cancel
	s.done = done
	s.mu.Unlock()

	go func() {
		defer close(done)
		if _, err := s.Tick(runCtx); err != nil {
			s.recordError(err)
		}
		for {
			now := s.now()
			wait := s.interval
			if wait <= 0 {
				wait = waitUntilNextMinute(now)
			}
			timer := time.NewTimer(wait)
			select {
			case <-runCtx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if _, err := s.Tick(runCtx); err != nil {
				s.recordError(err)
			}
		}
	}()
}

// Close 停止调度循环并等待循环退出。可以重复调用。
// 触发回调内不要同步调用 Close，否则会等待自身。
func (s *Scheduler) Close() error {
	s.mu.Lock()
	cancel := s.cancel
	done := s.done
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}

	s.mu.Lock()
	if s.done == done {
		s.running = false
		s.cancel = nil
		s.done = nil
	}
	s.mu.Unlock()
	return nil
}

// Fires 返回最近的触发记录，最新的在最后，最多保留 maxFireRecords 条。
func (s *Scheduler) Fires() []FireRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]FireRecord, len(s.fires))
	copy(out, s.fires)
	return out
}

// recordFire 追加一条触发记录并裁剪到上限。
func (s *Scheduler) recordFire(t Task, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fires = append(s.fires, FireRecord{Task: t, At: at})
	if len(s.fires) > maxFireRecords {
		s.fires = append([]FireRecord(nil), s.fires[len(s.fires)-maxFireRecords:]...)
	}
}

// LastError 返回最近一次调度循环中记录的错误；没有错误时为 nil。
func (s *Scheduler) LastError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

// recordError 记录调度循环中的错误，供诊断使用。
func (s *Scheduler) recordError(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	s.lastErr = err
	s.mu.Unlock()
}

// waitUntilNextMinute 返回距离下一个整分钟的时长。
func waitUntilNextMinute(now time.Time) time.Duration {
	next := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), now.Minute()+1, 0, 0, now.Location())
	d := next.Sub(now)
	if d <= 0 {
		return time.Minute
	}
	return d
}
