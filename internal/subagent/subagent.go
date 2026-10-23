// Package subagent 实现子 Agent 执行器：独立会话、独立工具池与独立权限范围，
// 同步运行并汇总最终文本，或以后台任务运行并留存结果供查询。
//
// 一个子 Agent 看不到父会话的历史：每次 Run 都用 agent.Wire + agent.NewEngine
// 构建一个新引擎，SessionID、消息历史、transcript 与权限引擎全部独立。
// 子 Agent 无法与用户交互，权限判定为 ask 时一律按拒绝处理（不转发审批事件）。
package subagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Baike12/b-desktop/internal/agent"
	"github.com/Baike12/b-desktop/internal/llm"
	"github.com/Baike12/b-desktop/internal/permission"
	"github.com/Baike12/b-desktop/internal/session"
	"github.com/Baike12/b-desktop/internal/tool"
)

// 默认值。
const (
	// defaultAgentsDirName 是用户级子 Agent 定义目录名，位于 ~/.b-desktop 下。
	defaultAgentsDirName = "agents"
	// defaultMaxToolConcurrency 与主 Agent 的默认工具并发上限一致。
	defaultMaxToolConcurrency = 10
)

// 模块错误哨兵。调用方可用 errors.Is 区分。
var (
	// ErrInvalidConfig 表示 Config 缺少必填项或 Runtime 返回了非法配置。
	ErrInvalidConfig = errors.New("subagent: invalid config")
	// ErrEmptyPrompt 表示子 Agent 的 prompt 为空。
	ErrEmptyPrompt = errors.New("subagent: empty prompt")
	// ErrEmptyDescription 表示子 Agent 的 description 为空。
	ErrEmptyDescription = errors.New("subagent: empty description")
	// ErrUnknownAgentType 表示 agent_type 不在已加载的类型列表中。
	ErrUnknownAgentType = errors.New("subagent: unknown agent type")
	// ErrClosed 表示 Manager 已关闭，不再接受新任务。
	ErrClosed = errors.New("subagent: manager closed")
	// ErrTaskNotFound 表示任务 ID 不存在（或已随 Manager 释放）。
	ErrTaskNotFound = errors.New("subagent: task not found")
)

// TaskStatus 是子 Agent 任务的运行状态。
type TaskStatus string

// 任务状态常量。
const (
	TaskRunning   TaskStatus = "running"   // TaskRunning 表示任务正在执行。
	TaskCompleted TaskStatus = "completed" // TaskCompleted 表示子 Agent 正常结束。
	TaskFailed    TaskStatus = "failed"    // TaskFailed 表示子 Agent 以错误结束。
	TaskCanceled  TaskStatus = "canceled"  // TaskCanceled 表示任务被取消。
)

// Runtime 是一次子 Agent 运行所需的动态配置。Manager 每次 Run 都重新解析它，
// 因此主会话切换模型、密钥或权限模式后，后续子 Agent 立即生效。
type Runtime struct {
	// Provider 是子 Agent 使用的模型客户端，必填。
	Provider llm.Provider
	// Model 是模型名。
	Model string
	// WorkDir 是子 Agent 的工作目录；请求自带 WorkDir 时以请求为准。
	WorkDir string
	// PermissionMode 是子 Agent 的初始权限模式，通常继承父会话。
	PermissionMode permission.Mode
	// ContextWindow 与 MaxOutputTokens 用于压缩预算。
	ContextWindow   int
	MaxOutputTokens int
	// MaxTurns 是单轮最大模型调用次数，0 表示不限。
	MaxTurns int
	// MaxToolConcurrency 是只读工具并发上限，0 取 10。
	MaxToolConcurrency int
}

// Config 是 Manager 的构造参数。
type Config struct {
	// WorkDir 是请求未指定工作目录时的默认值。
	WorkDir string
	// AgentsDir 是子 Agent 类型定义目录，默认 ~/.b-desktop/agents。
	AgentsDir string
	// Runtime 在每次运行前解析动态配置，必填。
	Runtime func(sessionID string) (Runtime, error)
	// Now 是时间源，nil 时使用 time.Now。
	Now func() time.Time
	// NewID 生成任务 ID，nil 时使用随机十六进制。
	NewID func(prefix string) string
}

// AgentType 是一个从 Markdown 定义加载的子 Agent 类型。
// 正文部分作为子 Agent 系统提示的附加文本（SystemExtra）。
type AgentType struct {
	// Name 是 frontmatter 的 name，也是 Agent 工具的 agent_type 取值。
	Name string
	// Description 是 frontmatter 的 description，告诉模型何时使用该类型。
	Description string
	// SystemPrompt 是 frontmatter 之后的正文。
	SystemPrompt string
	// Path 是定义文件路径。
	Path string
}

// Task 是一个子 Agent 任务的快照。
type Task struct {
	// ID 是任务标识，同时用作子 Agent 的 SessionID。
	ID string
	// SessionID 是发起任务的父会话。
	SessionID string
	// Description 是任务的一句话描述。
	Description string
	// AgentType 是子 Agent 类型名；空表示通用子 Agent。
	AgentType string
	// Background 报告该任务是否在后台运行。
	Background bool
	// Status 是当前状态。
	Status TaskStatus
	// StartedAt 是任务开始时间。
	StartedAt time.Time
	// FinishedAt 是任务结束时间；运行中为零值。
	FinishedAt time.Time
	// Result 是子 Agent 汇总后的最终文本。
	Result string
	// Error 是失败原因；成功时为空。
	Error string
	// Turns 是模型调用次数。
	Turns int
	// Usage 是整个子 Agent 的 token 用量。
	Usage llm.Usage
}

// taskState 是任务的可变内部状态，cancel 用于取消运行中的子 Agent。
type taskState struct {
	Task
	cancel context.CancelFunc
}

// Manager 管理子 Agent 类型、任务表与运行生命周期。
// 所有方法并发安全；Close 之后 Run 返回 ErrClosed。
type Manager struct {
	cfg Config
	tl  tool.Tool

	mu     sync.Mutex
	types  map[string]AgentType
	order  []string
	tasks  map[string]*taskState
	seq    []string
	closed bool

	rootCtx context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// 编译期断言：Manager 实现 agent.SubagentRunner。
var _ agent.SubagentRunner = (*Manager)(nil)

// NewManager 创建 Manager 并加载 AgentsDir 下的子 Agent 类型。
// Config.Runtime 为 nil 时返回 ErrInvalidConfig。
func NewManager(cfg Config) (*Manager, error) {
	if cfg.Runtime == nil {
		return nil, fmt.Errorf("%w: missing Runtime", ErrInvalidConfig)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.NewID == nil {
		cfg.NewID = defaultNewID
	}
	if strings.TrimSpace(cfg.AgentsDir) == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("subagent: resolve home dir: %w", err)
		}
		cfg.AgentsDir = filepath.Join(home, ".b-desktop", defaultAgentsDirName)
	}
	rootCtx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		cfg:     cfg,
		types:   make(map[string]AgentType),
		tasks:   make(map[string]*taskState),
		rootCtx: rootCtx,
		cancel:  cancel,
	}
	if err := m.Reload(); err != nil {
		cancel()
		return nil, err
	}
	m.tl = &agentTool{mgr: m}
	return m, nil
}

// defaultNewID 生成 "prefix_" + 16 字节随机数的十六进制。
func defaultNewID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return prefix + "_" + fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

// Reload 重新扫描 AgentsDir 并替换已加载的类型。目录不存在视为空列表。
// 缺少 name 或 description 的文件被跳过，不影响其他类型。
func (m *Manager) Reload() error {
	entries, err := os.ReadDir(m.cfg.AgentsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			m.replaceTypes(nil)
			return nil
		}
		return fmt.Errorf("subagent: read agents dir %s: %w", m.cfg.AgentsDir, err)
	}
	loaded := make([]AgentType, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			continue
		}
		path := filepath.Join(m.cfg.AgentsDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("subagent: read agent file %s: %w", path, err)
		}
		at, ok := parseAgentMarkdown(path, data)
		if !ok {
			continue
		}
		loaded = append(loaded, at)
	}
	m.replaceTypes(loaded)
	return nil
}

// replaceTypes 用新列表替换类型缓存；重名时保留先加载的定义。
func (m *Manager) replaceTypes(list []AgentType) {
	types := make(map[string]AgentType, len(list))
	order := make([]string, 0, len(list))
	for _, at := range list {
		if _, exists := types[at.Name]; exists {
			continue
		}
		types[at.Name] = at
		order = append(order, at.Name)
	}
	m.mu.Lock()
	m.types = types
	m.order = order
	m.mu.Unlock()
}

// Types 返回按加载顺序排列的子 Agent 类型快照。
func (m *Manager) Types() []AgentType {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]AgentType, 0, len(m.order))
	for _, name := range m.order {
		out = append(out, m.types[name])
	}
	return out
}

// lookupType 按名称查找类型；空名称返回通用类型（空附加提示）。
func (m *Manager) lookupType(name string) (AgentType, error) {
	if strings.TrimSpace(name) == "" {
		return AgentType{}, nil
	}
	m.mu.Lock()
	at, ok := m.types[name]
	m.mu.Unlock()
	if !ok {
		return AgentType{}, fmt.Errorf("%w: %q", ErrUnknownAgentType, name)
	}
	return at, nil
}

// Run 执行一次子 Agent。同步模式下 Run 返回时子 Agent 已结束；
// Background 为 true 时立即返回带 ID 的结果，任务在后台运行并通过 Tasks/Task 查询。
// 后台任务的事件在 Run 返回后仍可能到达 emit，因此 emit 必须是并发安全的实现。
//
// 返回 error 只用于请求或环境非法；子 Agent 自身的模型、工具与权限失败
// 通过 SubagentResult.Error 与任务状态表达。
func (m *Manager) Run(ctx context.Context, req agent.SubagentRequest, emit agent.EventSink) (agent.SubagentResult, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return agent.SubagentResult{}, fmt.Errorf("%w", ErrEmptyPrompt)
	}
	if strings.TrimSpace(req.Description) == "" {
		return agent.SubagentResult{}, fmt.Errorf("%w", ErrEmptyDescription)
	}
	at, err := m.lookupType(req.AgentType)
	if err != nil {
		return agent.SubagentResult{}, err
	}
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return agent.SubagentResult{}, fmt.Errorf("%w", ErrClosed)
	}
	rt, err := m.cfg.Runtime(req.SessionID)
	if err != nil {
		return agent.SubagentResult{}, fmt.Errorf("subagent: resolve runtime: %w", err)
	}
	if rt.Provider == nil {
		return agent.SubagentResult{}, fmt.Errorf("%w: Runtime.Provider is nil", ErrInvalidConfig)
	}
	rt.WorkDir = firstNonEmpty(strings.TrimSpace(req.WorkDir), strings.TrimSpace(rt.WorkDir), m.cfg.WorkDir)
	if rt.PermissionMode == "" {
		rt.PermissionMode = permission.ModeDefault
	}
	if rt.MaxToolConcurrency <= 0 {
		rt.MaxToolConcurrency = defaultMaxToolConcurrency
	}
	if emit == nil {
		emit = func(agent.Event) {}
	}

	id := m.cfg.NewID("subagent")
	if req.Background {
		runCtx, cancel := context.WithCancel(m.rootCtx)
		if err := m.register(id, req, cancel); err != nil {
			cancel()
			return agent.SubagentResult{}, err
		}
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			defer cancel()
			m.finish(id, m.execute(runCtx, id, req, at, rt, emit))
		}()
		return agent.SubagentResult{ID: id}, nil
	}

	runCtx, cancel := context.WithCancel(ctx)
	if err := m.register(id, req, cancel); err != nil {
		cancel()
		return agent.SubagentResult{}, err
	}
	defer cancel()
	out := m.execute(runCtx, id, req, at, rt, emit)
	m.finish(id, out)
	return out.result, nil
}

// runOutcome 是一次执行的内部结果。
type runOutcome struct {
	result agent.SubagentResult
	status TaskStatus
}

// execute 构建独立引擎并运行子 Agent。它不修改任务表，由调用方写入结果。
func (m *Manager) execute(ctx context.Context, id string, req agent.SubagentRequest, at AgentType, rt Runtime, emit agent.EventSink) runOutcome {
	fail := func(err error) runOutcome {
		msg := err.Error()
		return runOutcome{
			result: agent.SubagentResult{ID: id, Error: msg},
			status: statusForError(err),
		}
	}

	components, err := agent.Wire(agent.WireOptions{
		WorkDir:         rt.WorkDir,
		Model:           rt.Model,
		Provider:        rt.Provider,
		PermissionMode:  rt.PermissionMode,
		ContextWindow:   rt.ContextWindow,
		MaxOutputTokens: rt.MaxOutputTokens,
		SystemExtra:     at.SystemPrompt,
		Now:             m.cfg.Now,
	})
	if err != nil {
		return fail(fmt.Errorf("subagent: wire: %w", err))
	}

	store := session.NewMemStore()
	if err := store.CreateSession(ctx, session.Meta{
		ID:        id,
		Title:     req.Description,
		WorkDir:   rt.WorkDir,
		Model:     rt.Model,
		CreatedAt: m.cfg.Now(),
	}); err != nil {
		return fail(fmt.Errorf("subagent: create child session: %w", err))
	}

	approvals := agent.NewApprovals()
	engine, err := agent.NewEngine(agent.Config{
		SessionID:          id,
		WorkDir:            rt.WorkDir,
		Provider:           rt.Provider,
		Model:              rt.Model,
		MaxTurns:           rt.MaxTurns,
		MaxToolConcurrency: rt.MaxToolConcurrency,
		Tools:              components.Tools,
		Permissions:        components.Permissions,
		Approvals:          approvals,
		Builder:            components.Builder,
		Compactor:          components.Compactor,
		Store:              store,
		Now:                m.cfg.Now,
	})
	if err != nil {
		return fail(fmt.Errorf("subagent: new engine: %w", err))
	}
	defer func() { _ = engine.Close() }()

	sink := m.eventSink(approvals, emit)
	res, err := engine.Run(ctx, agent.Input{Prompt: req.Prompt}, sink)
	if err != nil {
		return fail(fmt.Errorf("subagent: run: %w", err))
	}

	text := res.Text
	if strings.TrimSpace(text) == "" {
		text = lastAssistantText(engine.History())
	}
	out := agent.SubagentResult{
		ID:    id,
		Text:  text,
		Error: res.Err,
		Usage: res.Usage,
		Turns: res.Turns,
	}
	status := TaskCompleted
	switch {
	case res.StopReason == agent.StopCanceled:
		status = TaskCanceled
	case res.Err != "" || res.StopReason == agent.StopError:
		status = TaskFailed
	}
	return runOutcome{result: out, status: status}
}

// eventSink 包装子 Agent 的事件出口：审批请求在内部按拒绝处理，不转发给父会话。
func (m *Manager) eventSink(approvals *agent.Approvals, emit agent.EventSink) agent.EventSink {
	return func(ev agent.Event) {
		switch ev.Type {
		case agent.EventPermissionRequest:
			if ev.Approval != nil {
				approvals.Decide(ev.Approval.RequestID, permission.Response{Option: permission.OptionDenyOnce})
			}
			return
		case agent.EventPermissionResolved:
			return
		}
		emit(ev)
	}
}

// register 登记一个运行中的任务；Manager 已关闭时返回 ErrClosed。
func (m *Manager) register(id string, req agent.SubagentRequest, cancel context.CancelFunc) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return fmt.Errorf("%w", ErrClosed)
	}
	m.tasks[id] = &taskState{
		Task: Task{
			ID:          id,
			SessionID:   req.SessionID,
			Description: req.Description,
			AgentType:   req.AgentType,
			Background:  req.Background,
			Status:      TaskRunning,
			StartedAt:   m.cfg.Now(),
		},
		cancel: cancel,
	}
	m.seq = append(m.seq, id)
	return nil
}

// finish 写入任务终态。
func (m *Manager) finish(id string, out runOutcome) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return
	}
	t.Status = out.status
	t.FinishedAt = m.cfg.Now()
	t.Result = out.result.Text
	t.Error = out.result.Error
	t.Turns = out.result.Turns
	t.Usage = out.result.Usage
	t.cancel = nil
}

// Tasks 返回全部任务快照，按开始时间倒序（同一时刻按 ID 倒序）。
func (m *Manager) Tasks() []Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Task, 0, len(m.seq))
	for _, id := range m.seq {
		if t, ok := m.tasks[id]; ok {
			out = append(out, t.Task)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.After(out[j].StartedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// Task 按 ID 返回任务快照。
func (m *Manager) Task(id string) (Task, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return Task{}, false
	}
	return t.Task, true
}

// CancelTask 取消运行中的任务。未知 ID 返回 false；已结束的任务视为取消成功。
func (m *Manager) CancelTask(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return false
	}
	if t.cancel != nil {
		t.cancel()
	}
	return true
}

// Tools 返回该模块提供给 Agent 的工具列表（当前只有 Agent 工具）。
func (m *Manager) Tools() []tool.Tool {
	return []tool.Tool{m.tl}
}

// Close 取消全部运行中的任务并等待后台任务退出；可重复调用。
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	for _, t := range m.tasks {
		if t.cancel != nil {
			t.cancel()
		}
	}
	m.mu.Unlock()
	m.cancel()
	m.wg.Wait()
	return nil
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// statusForError 把内部错误映射成任务状态。
func statusForError(err error) TaskStatus {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return TaskCanceled
	}
	return TaskFailed
}

// lastAssistantText 返回最后一条包含文本的 assistant 消息文本，用于
// 模型没有产出最终文本时的兜底汇总。
func lastAssistantText(msgs []llm.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != llm.RoleAssistant {
			continue
		}
		var b strings.Builder
		for _, block := range msgs[i].Blocks {
			if block.Type != llm.BlockText || strings.TrimSpace(block.Text) == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(block.Text)
		}
		if b.Len() > 0 {
			return b.String()
		}
	}
	return ""
}
