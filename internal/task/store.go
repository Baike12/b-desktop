package task

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 存储层错误哨兵。
var (
	// ErrNotFound 表示任务 ID 不存在。
	ErrNotFound = errors.New("task: not found")
	// ErrInvalidTask 表示任务字段非法（cron 无法解析、prompt 为空等）。
	ErrInvalidTask = errors.New("task: invalid task")
)

// Task 是一条持久化的定时任务。
type Task struct {
	// ID 是任务唯一标识，由 Store 创建时生成。
	ID string `json:"id"`
	// Cron 是 5 字段 cron 表达式，按本地时区解释。
	Cron string `json:"cron"`
	// Prompt 是触发时要执行的提示词。
	Prompt string `json:"prompt"`
	// WorkDir 是执行任务的工作目录；空表示由执行方决定。
	WorkDir string `json:"workDir,omitempty"`
	// Enabled 为 false 时调度器跳过该任务。
	Enabled bool `json:"enabled"`
	// CreatedAtMS 是创建时间（Unix 毫秒），也是首次计算触发时间的锚点。
	CreatedAtMS int64 `json:"createdAtMs"`
	// LastRunAtMS 是最近一次触发时间（Unix 毫秒）；0 表示尚未触发。
	LastRunAtMS int64 `json:"lastRunAtMs,omitempty"`
}

// NewTaskInput 是创建任务的入参。Enabled 为 nil 时默认启用。
type NewTaskInput struct {
	Cron    string
	Prompt  string
	WorkDir string
	Enabled *bool
}

// taskFile 是 tasks.json 的磁盘结构。
type taskFile struct {
	Tasks []Task `json:"tasks"`
}

// Store 把任务持久化到一个 JSON 文件。所有方法并发安全，
// 写入采用「临时文件 + rename」保证文件不会出现半截内容。
type Store struct {
	mu    sync.Mutex
	path  string
	tasks []Task
}

// NewStore 打开或创建任务文件。文件不存在时得到空列表；
// 文件损坏时返回错误；单条非法任务被跳过，不影响其余任务。
func NewStore(path string) (*Store, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil, errors.New("task: store path is required")
	}
	s := &Store{path: trimmed}
	if err := s.reload(); err != nil {
		return nil, err
	}
	return s, nil
}

// Path 返回任务文件路径。
func (s *Store) Path() string { return s.path }

// reload 从磁盘读取任务列表。调用方必须持有锁或处于构造期。
func (s *Store) reload() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.tasks = nil
			return nil
		}
		return fmt.Errorf("task: read store %s: %w", s.path, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		s.tasks = nil
		return nil
	}
	var file taskFile
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("task: parse store %s: %w", s.path, err)
	}
	tasks := make([]Task, 0, len(file.Tasks))
	for _, t := range file.Tasks {
		if strings.TrimSpace(t.ID) == "" || strings.TrimSpace(t.Prompt) == "" {
			continue
		}
		if _, err := ParseCron(t.Cron); err != nil {
			continue
		}
		tasks = append(tasks, t)
	}
	s.tasks = tasks
	return nil
}

// List 返回任务快照，按创建时间升序排列。
func (s *Store) List() []Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Task, len(s.tasks))
	copy(out, s.tasks)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAtMS == out[j].CreatedAtMS {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAtMS < out[j].CreatedAtMS
	})
	return out
}

// Get 按 ID 查找任务。
func (s *Store) Get(id string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tasks {
		if t.ID == id {
			return t, nil
		}
	}
	return Task{}, fmt.Errorf("%w: %s", ErrNotFound, id)
}

// Create 校验并追加一条任务，返回创建结果。
func (s *Store) Create(in NewTaskInput) (Task, error) {
	cronExpr := strings.TrimSpace(in.Cron)
	if cronExpr == "" {
		return Task{}, fmt.Errorf("%w: empty cron", ErrInvalidTask)
	}
	if _, err := ParseCron(cronExpr); err != nil {
		return Task{}, fmt.Errorf("%w: %v", ErrInvalidTask, err)
	}
	prompt := strings.TrimSpace(in.Prompt)
	if prompt == "" {
		return Task{}, fmt.Errorf("%w: empty prompt", ErrInvalidTask)
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := s.newIDLocked()
	if err != nil {
		return Task{}, err
	}
	t := Task{
		ID:          id,
		Cron:        cronExpr,
		Prompt:      prompt,
		WorkDir:     strings.TrimSpace(in.WorkDir),
		Enabled:     enabled,
		CreatedAtMS: time.Now().UnixMilli(),
	}
	s.tasks = append(s.tasks, t)
	if err := s.saveLocked(); err != nil {
		s.tasks = s.tasks[:len(s.tasks)-1]
		return Task{}, err
	}
	return t, nil
}

// Delete 按 ID 删除任务；ID 不存在时返回包装了 ErrNotFound 的错误。
func (s *Store) Delete(id string) error {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return fmt.Errorf("%w: empty id", ErrInvalidTask)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, t := range s.tasks {
		if t.ID != trimmed {
			continue
		}
		previous := s.tasks
		next := make([]Task, 0, len(previous)-1)
		next = append(next, previous[:i]...)
		next = append(next, previous[i+1:]...)
		s.tasks = next
		if err := s.saveLocked(); err != nil {
			s.tasks = previous
			return err
		}
		return nil
	}
	return fmt.Errorf("%w: %s", ErrNotFound, trimmed)
}

// SetEnabled 启用或禁用任务。
func (s *Store) SetEnabled(id string, enabled bool) error {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return fmt.Errorf("%w: empty id", ErrInvalidTask)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.tasks {
		if s.tasks[i].ID != trimmed {
			continue
		}
		previous := s.tasks[i].Enabled
		if previous == enabled {
			return nil
		}
		s.tasks[i].Enabled = enabled
		if err := s.saveLocked(); err != nil {
			s.tasks[i].Enabled = previous
			return err
		}
		return nil
	}
	return fmt.Errorf("%w: %s", ErrNotFound, trimmed)
}

// MarkFired 记录任务最近一次触发时间。任务被删除时返回 ErrNotFound。
func (s *Store) MarkFired(id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.tasks {
		if s.tasks[i].ID != id {
			continue
		}
		previous := s.tasks[i].LastRunAtMS
		s.tasks[i].LastRunAtMS = at.UnixMilli()
		if err := s.saveLocked(); err != nil {
			s.tasks[i].LastRunAtMS = previous
			return err
		}
		return nil
	}
	return fmt.Errorf("%w: %s", ErrNotFound, id)
}

// newIDLocked 生成不与现有任务冲突的随机 ID。调用方必须持有锁。
func (s *Store) newIDLocked() (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		buf := make([]byte, 8)
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("task: generate id: %w", err)
		}
		id := hex.EncodeToString(buf)
		dup := false
		for _, t := range s.tasks {
			if t.ID == id {
				dup = true
				break
			}
		}
		if !dup {
			return id, nil
		}
	}
	return "", errors.New("task: unable to allocate unique id")
}

// saveLocked 原子写回任务文件。调用方必须持有锁。
func (s *Store) saveLocked() error {
	file := taskFile{Tasks: s.tasks}
	if file.Tasks == nil {
		file.Tasks = []Task{}
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("task: marshal store: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("task: create store dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".tasks-*.json")
	if err != nil {
		return fmt.Errorf("task: create temp store: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("task: chmod temp store: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("task: write temp store: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("task: sync temp store: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("task: close temp store: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("task: replace store %s: %w", s.path, err)
	}
	return nil
}
