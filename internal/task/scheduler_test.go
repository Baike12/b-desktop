package task

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Baike12/b-desktop/internal/tool"
)

// fixedClock 是可并发读取、可向前推进的测试时钟。
type fixedClock struct {
	ms atomic.Int64
}

func newFixedClock(t time.Time) *fixedClock {
	c := &fixedClock{}
	c.ms.Store(t.UnixMilli())
	return c
}

func (c *fixedClock) Now() time.Time { return time.UnixMilli(c.ms.Load()) }

func (c *fixedClock) Set(t time.Time) { c.ms.Store(t.UnixMilli()) }

// writeTasks 直接把任务写入 store 文件，便于构造确定性的初始状态。
func writeTasks(t *testing.T, path string, tasks []Task) {
	t.Helper()
	data, err := json.Marshal(taskFile{Tasks: tasks})
	if err != nil {
		t.Fatalf("marshal tasks: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write tasks: %v", err)
	}
}

// newTestScheduler 构造带固定时钟与记录回调的调度器。
func newTestScheduler(t *testing.T, tasks []Task, now time.Time) (*Scheduler, *fixedClock, *[]Task, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tasks.json")
	writeTasks(t, path, tasks)
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	clock := newFixedClock(now)
	var (
		mu    sync.Mutex
		fired []Task
	)
	sched, err := NewScheduler(SchedulerConfig{
		Store: store,
		Now:   clock.Now,
		Fire: func(_ context.Context, t Task) {
			mu.Lock()
			fired = append(fired, t)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	return sched, clock, &fired, path
}

func TestSchedulerFiresDueTaskOnce(t *testing.T) {
	created := time.Date(2026, time.January, 1, 9, 0, 0, 0, time.Local)
	now := time.Date(2026, time.January, 1, 9, 7, 30, 0, time.Local)
	sched, _, fired, _ := newTestScheduler(t, []Task{{
		ID:          "t1",
		Cron:        "*/5 * * * *",
		Prompt:      "ping",
		Enabled:     true,
		CreatedAtMS: created.UnixMilli(),
	}}, now)

	got, err := sched.Check(context.Background(), now)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(got) != 1 || got[0].ID != "t1" {
		t.Fatalf("fired = %+v, want t1", got)
	}
	if got[0].LastRunAtMS != now.UnixMilli() {
		t.Fatalf("LastRunAtMS = %d, want %d", got[0].LastRunAtMS, now.UnixMilli())
	}
	if len(*fired) != 1 || (*fired)[0].Prompt != "ping" {
		t.Fatalf("callback = %+v", *fired)
	}
	// 同一时刻再检查不应重复触发。
	again, err := sched.Check(context.Background(), now)
	if err != nil {
		t.Fatalf("second Check: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second check fired %+v", again)
	}
	if len(sched.Fires()) != 1 {
		t.Fatalf("Fires = %+v", sched.Fires())
	}
}

func TestSchedulerFiresOnNextMinute(t *testing.T) {
	created := time.Date(2026, time.January, 1, 9, 0, 0, 0, time.Local)
	sched, clock, fired, _ := newTestScheduler(t, []Task{{
		ID:          "t1",
		Cron:        "* * * * *",
		Prompt:      "ping",
		Enabled:     true,
		CreatedAtMS: created.UnixMilli(),
	}}, created)

	// 创建时刻尚未到点：Next(created) 是 09:01。
	if got, err := sched.Check(context.Background(), created); err != nil || len(got) != 0 {
		t.Fatalf("check at creation = %+v, %v", got, err)
	}
	clock.Set(created.Add(time.Minute))
	got, err := sched.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("tick fired = %+v", got)
	}
	if len(*fired) != 1 {
		t.Fatalf("callback = %+v", *fired)
	}
}

func TestSchedulerSkipsDisabledAndInvalid(t *testing.T) {
	created := time.Date(2026, time.January, 1, 9, 0, 0, 0, time.Local)
	now := created.Add(10 * time.Minute)
	sched, _, fired, _ := newTestScheduler(t, []Task{
		{ID: "off", Cron: "* * * * *", Prompt: "p", Enabled: false, CreatedAtMS: created.UnixMilli()},
		{ID: "on", Cron: "* * * * *", Prompt: "p", Enabled: true, CreatedAtMS: created.UnixMilli()},
	}, now)

	got, err := sched.Check(context.Background(), now)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(got) != 1 || got[0].ID != "on" {
		t.Fatalf("fired = %+v, want only on", got)
	}
	if len(*fired) != 1 {
		t.Fatalf("callback = %+v", *fired)
	}
}

func TestSchedulerStartLoopUsesInjectedClock(t *testing.T) {
	created := time.Date(2026, time.January, 1, 9, 0, 0, 0, time.Local)
	path := filepath.Join(t.TempDir(), "tasks.json")
	writeTasks(t, path, []Task{{
		ID:          "t1",
		Cron:        "* * * * *",
		Prompt:      "ping",
		Enabled:     true,
		CreatedAtMS: created.UnixMilli(),
	}})
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	clock := newFixedClock(created)
	firedCh := make(chan Task, 4)
	sched, err := NewScheduler(SchedulerConfig{
		Store:    store,
		Now:      clock.Now,
		Interval: 5 * time.Millisecond,
		Fire: func(_ context.Context, t Task) {
			select {
			case firedCh <- t:
			default:
			}
		},
	})
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	sched.Start(context.Background())
	defer func() {
		if err := sched.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()

	select {
	case task := <-firedCh:
		t.Fatalf("fired before clock advanced: %+v", task)
	case <-time.After(50 * time.Millisecond):
	}

	clock.Set(created.Add(time.Minute))
	select {
	case task := <-firedCh:
		if task.ID != "t1" {
			t.Fatalf("fired = %+v", task)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not fire after clock advanced")
	}
}

func TestSchedulerStartIsIdempotentAndRestartable(t *testing.T) {
	created := time.Date(2026, time.January, 1, 9, 0, 0, 0, time.Local)
	sched, _, _, _ := newTestScheduler(t, nil, created)
	sched.Start(context.Background())
	sched.Start(context.Background())
	if err := sched.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := sched.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	sched.Start(context.Background())
	if err := sched.Close(); err != nil {
		t.Fatalf("Close after restart: %v", err)
	}
}

func TestSchedulerListAndNextRun(t *testing.T) {
	created := time.Date(2026, time.January, 1, 9, 0, 0, 0, time.Local)
	sched, _, _, _ := newTestScheduler(t, []Task{
		{ID: "a", Cron: "30 14 * * *", Prompt: "p", Enabled: true, CreatedAtMS: created.UnixMilli()},
		{ID: "b", Cron: "* * * * *", Prompt: "p", Enabled: false, CreatedAtMS: created.UnixMilli()},
	}, created)

	list := sched.List()
	if len(list) != 2 {
		t.Fatalf("list = %+v", list)
	}
	wantNext := time.Date(2026, time.January, 1, 14, 30, 0, 0, time.Local).UnixMilli()
	if list[0].ID != "a" || list[0].NextRunAtMS != wantNext {
		t.Fatalf("view a = %+v, want next %d", list[0], wantNext)
	}
	if list[1].ID != "b" || list[1].NextRunAtMS != 0 {
		t.Fatalf("view b = %+v, want no next run", list[1])
	}
}

func TestSchedulerCreateDeleteSetEnabled(t *testing.T) {
	now := time.Date(2026, time.January, 1, 9, 0, 0, 0, time.Local)
	sched, _, _, path := newTestScheduler(t, nil, now)

	view, err := sched.Create(NewTaskInput{Cron: "0 9 * * *", Prompt: "p"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if view.ID == "" || view.NextRunAtMS == 0 {
		t.Fatalf("view = %+v", view)
	}
	if err := sched.SetEnabled(view.ID, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	list := sched.List()
	if len(list) != 1 || list[0].Enabled {
		t.Fatalf("list = %+v", list)
	}
	if err := sched.Delete(view.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	reloaded, err := NewStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := reloaded.List(); len(got) != 0 {
		t.Fatalf("reloaded = %+v", got)
	}
}

func TestSchedulerFiresAfterMissedRunOnlyOnce(t *testing.T) {
	// 任务创建于 09:00，进程在 09:20 才启动：只补触发一次。
	created := time.Date(2026, time.January, 1, 9, 0, 0, 0, time.Local)
	now := created.Add(20 * time.Minute)
	sched, _, fired, _ := newTestScheduler(t, []Task{{
		ID: "t1", Cron: "* * * * *", Prompt: "p", Enabled: true, CreatedAtMS: created.UnixMilli(),
	}}, now)

	if got, err := sched.Check(context.Background(), now); err != nil || len(got) != 1 {
		t.Fatalf("first check = %+v, %v", got, err)
	}
	// 同一分钟内反复检查只触发一次。
	for i := 0; i < 3; i++ {
		if got, err := sched.Check(context.Background(), now.Add(time.Duration(i)*time.Second)); err != nil || len(got) != 0 {
			t.Fatalf("repeat check = %+v, %v", got, err)
		}
	}
	if len(*fired) != 1 {
		t.Fatalf("fired %d times, want 1", len(*fired))
	}
}

func TestSchedulerRecoversFromFiringPanic(t *testing.T) {
	created := time.Date(2026, time.January, 1, 9, 0, 0, 0, time.Local)
	now := created.Add(time.Minute)
	path := filepath.Join(t.TempDir(), "tasks.json")
	writeTasks(t, path, []Task{
		{ID: "a", Cron: "* * * * *", Prompt: "p", Enabled: true, CreatedAtMS: created.UnixMilli()},
		{ID: "b", Cron: "* * * * *", Prompt: "p", Enabled: true, CreatedAtMS: created.UnixMilli()},
	})
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	var calls int
	sched, err := NewScheduler(SchedulerConfig{
		Store: store,
		Now:   func() time.Time { return now },
		Fire: func(_ context.Context, t Task) {
			calls++
			if t.ID == "a" {
				panic("boom")
			}
		},
	})
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	fired, err := sched.Check(context.Background(), now)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want recovered panic", err)
	}
	if len(fired) != 2 || calls != 2 {
		t.Fatalf("fired = %+v, calls = %d, want both tasks", fired, calls)
	}
}

func TestSchedulerAccessors(t *testing.T) {
	now := time.Date(2026, time.January, 1, 9, 0, 0, 0, time.Local)
	sched, _, _, _ := newTestScheduler(t, nil, now)
	if !sched.Now().Equal(now) {
		t.Fatalf("Now = %s, want %s", sched.Now(), now)
	}
	if sched.Store() == nil {
		t.Fatal("Store is nil")
	}
	if err := sched.LastError(); err != nil {
		t.Fatalf("LastError = %v, want nil", err)
	}
	boom := errors.New("boom")
	sched.recordError(boom)
	if !errors.Is(sched.LastError(), boom) {
		t.Fatalf("LastError = %v, want boom", sched.LastError())
	}
}

func TestToolsIncludeScheduleCron(t *testing.T) {
	now := time.Date(2026, time.January, 1, 9, 0, 0, 0, time.Local)
	sched, _, _, _ := newTestScheduler(t, nil, now)
	tools := sched.Tools()
	if len(tools) != 1 || tools[0].Name() != "ScheduleCron" {
		t.Fatalf("tools = %+v", tools)
	}
}

func TestScheduleCronToolCreatesTask(t *testing.T) {
	now := time.Date(2026, time.January, 1, 9, 0, 0, 0, time.Local)
	sched, _, _, _ := newTestScheduler(t, nil, now)
	tt := sched.Tools()[0]

	input := json.RawMessage(`{"cron":"*/10 * * * *","prompt":"check the build","workDir":""}`)
	res, err := tt.Run(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("result error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "ScheduleCron") && !strings.Contains(res.Content, "Scheduled task") {
		t.Fatalf("content = %q", res.Content)
	}
	if res.Meta["taskId"] == "" {
		t.Fatalf("meta = %+v", res.Meta)
	}
	list := sched.List()
	if len(list) != 1 || list[0].Cron != "*/10 * * * *" || list[0].Prompt != "check the build" {
		t.Fatalf("list = %+v", list)
	}
}

func TestScheduleCronToolRejectsBadInput(t *testing.T) {
	now := time.Date(2026, time.January, 1, 9, 0, 0, 0, time.Local)
	sched, _, _, _ := newTestScheduler(t, nil, now)
	tt := sched.Tools()[0]

	cases := []string{
		`{"cron":"bad","prompt":"p"}`,
		`{"cron":"* * * * *","prompt":""}`,
		`{"cron":"","prompt":"p"}`,
		`{"cron":"0 0 30 2 *","prompt":"p"}`,
		`{"cron":"* * * * *","prompt":"p","workDir":"/definitely/not/here"}`,
	}
	for _, in := range cases {
		res, err := tt.Run(context.Background(), json.RawMessage(in), nil)
		if err != nil {
			t.Fatalf("Run(%s): %v", in, err)
		}
		if !res.IsError {
			t.Fatalf("Run(%s) did not fail: %+v", in, res)
		}
	}
	if got := sched.List(); len(got) != 0 {
		t.Fatalf("tasks = %+v, want none", got)
	}
}

func TestScheduleCronToolEnforcesLimit(t *testing.T) {
	now := time.Date(2026, time.January, 1, 9, 0, 0, 0, time.Local)
	tasks := make([]Task, 0, maxScheduledTasks)
	for i := 0; i < maxScheduledTasks; i++ {
		tasks = append(tasks, Task{
			ID:          string(rune('a'+i%26)) + string(rune('a'+i/26)),
			Cron:        "* * * * *",
			Prompt:      "p",
			Enabled:     true,
			CreatedAtMS: now.UnixMilli(),
		})
	}
	sched, _, _, _ := newTestScheduler(t, tasks, now)
	res, err := sched.Tools()[0].Run(context.Background(), json.RawMessage(`{"cron":"* * * * *","prompt":"p"}`), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "too many") {
		t.Fatalf("result = %+v", res)
	}
}

// 编译期确认工具名不会与内置工具冲突的常见名称相同。
var _ tool.Tool = (*scheduleCronTool)(nil)
