package app

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Baike12/b-desktop/internal/permission"
	"github.com/Baike12/b-desktop/internal/subagent"
)

// subagentStoreKeyPrefix 是扩展组件缓存中子 Agent 管理器的 key 前缀。
// 每个工作目录一个 Manager，避免不同项目的子 Agent 共用默认目录。
const subagentStoreKeyPrefix = "subagent:"

// 绑定层错误哨兵。
var (
	// ErrSubagentTaskNotFound 表示任务 ID 不存在或已随管理器释放。
	ErrSubagentTaskNotFound = errors.New("app: subagent task not found")
	// ErrSubagentTaskIDRequired 表示取消任务时未提供任务 ID。
	ErrSubagentTaskIDRequired = errors.New("app: subagent task id required")
)

// SubagentTypeDTO 是子 Agent 类型的绑定视图。
type SubagentTypeDTO struct {
	// Name 是类型名，对应 Agent 工具的 agent_type。
	Name string `json:"name"`
	// Description 描述何时使用该类型。
	Description string `json:"description"`
	// Path 是定义文件路径。
	Path string `json:"path,omitempty"`
}

// SubagentTaskDTO 是子 Agent 任务的绑定视图。
type SubagentTaskDTO struct {
	// ID 是任务标识。
	ID string `json:"id"`
	// SessionID 是发起任务的父会话。
	SessionID string `json:"sessionId"`
	// Description 是任务描述。
	Description string `json:"description"`
	// AgentType 是子 Agent 类型名，空表示通用子 Agent。
	AgentType string `json:"agentType"`
	// Status 取值 running、completed、failed、canceled。
	Status string `json:"status"`
	// Background 报告任务是否在后台运行。
	Background bool `json:"background"`
	// StartedAt 是开始时间（RFC3339Nano）。
	StartedAt string `json:"startedAt"`
	// FinishedAt 是结束时间；运行中为空字符串。
	FinishedAt string `json:"finishedAt"`
	// Result 是子 Agent 的最终文本。
	Result string `json:"result,omitempty"`
	// Error 是失败原因。
	Error string `json:"error,omitempty"`
	// Turns 是模型调用次数。
	Turns int `json:"turns"`
	// InputTokens 与 OutputTokens 是子 Agent 的 token 用量。
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

// subagentComponent 返回指定工作目录的子 Agent 管理器。
// Manager 实现 agent.SubagentRunner，并提供 Tools() 与 Close()。
func (a *App) subagentComponent(workDir string) (*subagent.Manager, error) {
	key := subagentStoreKeyPrefix + workDir
	value, err := a.extensionStore(key, func() (any, error) {
		dir, err := a.settingsDir()
		if err != nil {
			return nil, err
		}
		mgr, err := subagent.NewManager(subagent.Config{
			WorkDir:   workDir,
			AgentsDir: filepath.Join(dir, "agents"),
			Runtime: func(sessionID string) (subagent.Runtime, error) {
				return a.subagentRuntime(sessionID, workDir)
			},
		})
		if err != nil {
			return nil, err
		}
		return mgr, nil
	})
	if err != nil {
		return nil, fmt.Errorf("app: subagent component: %w", err)
	}
	mgr, ok := value.(*subagent.Manager)
	if !ok {
		return nil, fmt.Errorf("app: subagent component: unexpected type %T", value)
	}
	return mgr, nil
}

// subagentRuntime 解析一次子 Agent 运行所需的 Provider、模型与权限模式。
// 会话级权限模式与工作目录优先于全局配置；会话未知时使用 fallbackWorkDir 与全局模式。
// 它只在子 Agent 真正运行时调用，因此可以在 extensionStore 的 init 之外读取 a.mu。
func (a *App) subagentRuntime(sessionID, fallbackWorkDir string) (subagent.Runtime, error) {
	cfg, _ := a.configSnapshot()
	provider, err := a.factory(cfg)
	if err != nil {
		return subagent.Runtime{}, fmt.Errorf("%w: %v", ErrInvalidProviderConfig, err)
	}
	workDir := fallbackWorkDir
	mode := permission.ModeDefault
	if parsed, err := permission.ParseMode(cfg.PermissionMode); err == nil {
		mode = parsed
	}
	if rt, err := a.runtimeOf(sessionID); err == nil {
		if strings.TrimSpace(rt.workDir) != "" {
			workDir = rt.workDir
		}
		if sessionMode := rt.permissionMode(); sessionMode != "" {
			mode = sessionMode
		}
	}
	if strings.TrimSpace(workDir) == "" {
		workDir = cfg.WorkDir
	}
	return subagent.Runtime{
		Provider:           provider,
		Model:              cfg.Model,
		WorkDir:            workDir,
		PermissionMode:     mode,
		ContextWindow:      cfg.ContextWindow,
		MaxOutputTokens:    cfg.MaxOutputTokens,
		MaxTurns:           cfg.MaxTurns,
		MaxToolConcurrency: cfg.MaxToolConcurrency,
	}, nil
}

// subagentManagers 返回已创建的管理器；一个都没有时按当前配置的工作目录创建一个，
// 保证列表接口在应用启动后、首次发送消息前也能读到用户级定义。
func (a *App) subagentManagers() ([]*subagent.Manager, error) {
	if existing := a.cachedSubagentManagers(); len(existing) > 0 {
		return existing, nil
	}
	a.mu.Lock()
	workDir := a.cfg.WorkDir
	a.mu.Unlock()
	mgr, err := a.subagentComponent(workDir)
	if err != nil {
		return nil, err
	}
	return []*subagent.Manager{mgr}, nil
}

// cachedSubagentManagers 按 key 顺序返回已缓存的子 Agent 管理器。
// 它不触发创建，因此不会在 extMu 持有期间回调 extensionStore。
func (a *App) cachedSubagentManagers() []*subagent.Manager {
	a.extMu.Lock()
	keys := make([]string, 0, len(a.extData))
	for key := range a.extData {
		if strings.HasPrefix(key, subagentStoreKeyPrefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	out := make([]*subagent.Manager, 0, len(keys))
	for _, key := range keys {
		if mgr, ok := a.extData[key].(*subagent.Manager); ok {
			out = append(out, mgr)
		}
	}
	a.extMu.Unlock()
	return out
}

// ListSubagentTypes 返回用户级子 Agent 类型（~/.b-desktop/agents/*.md）。
// 多份定义重名时按管理器创建顺序去重，结果按名称排序。
func (a *App) ListSubagentTypes() ([]SubagentTypeDTO, error) {
	managers, err := a.subagentManagers()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	out := make([]SubagentTypeDTO, 0)
	for _, mgr := range managers {
		for _, at := range mgr.Types() {
			if _, ok := seen[at.Name]; ok {
				continue
			}
			seen[at.Name] = struct{}{}
			out = append(out, SubagentTypeDTO{
				Name:        at.Name,
				Description: at.Description,
				Path:        at.Path,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ListSubagentTasks 返回全部子 Agent 任务（同步与后台），按开始时间倒序。
func (a *App) ListSubagentTasks() ([]SubagentTaskDTO, error) {
	managers, err := a.subagentManagers()
	if err != nil {
		return nil, err
	}
	tasks := make([]subagent.Task, 0)
	for _, mgr := range managers {
		tasks = append(tasks, mgr.Tasks()...)
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		if !tasks[i].StartedAt.Equal(tasks[j].StartedAt) {
			return tasks[i].StartedAt.After(tasks[j].StartedAt)
		}
		return tasks[i].ID > tasks[j].ID
	})
	out := make([]SubagentTaskDTO, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, toSubagentTaskDTO(task))
	}
	return out, nil
}

// CancelSubagentTask 取消一个运行中的子 Agent 任务；任务已结束时视为成功。
// 任务 ID 为空返回 ErrSubagentTaskIDRequired，找不到返回 ErrSubagentTaskNotFound。
func (a *App) CancelSubagentTask(taskID string) error {
	id := strings.TrimSpace(taskID)
	if id == "" {
		return fmt.Errorf("%w", ErrSubagentTaskIDRequired)
	}
	managers := a.cachedSubagentManagers()
	for _, mgr := range managers {
		if mgr.CancelTask(id) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s", ErrSubagentTaskNotFound, id)
}

// toSubagentTaskDTO 把领域任务转成绑定层视图。
func toSubagentTaskDTO(task subagent.Task) SubagentTaskDTO {
	return SubagentTaskDTO{
		ID:           task.ID,
		SessionID:    task.SessionID,
		Description:  task.Description,
		AgentType:    task.AgentType,
		Status:       string(task.Status),
		Background:   task.Background,
		StartedAt:    formatSubagentTime(task.StartedAt),
		FinishedAt:   formatSubagentTime(task.FinishedAt),
		Result:       task.Result,
		Error:        task.Error,
		Turns:        task.Turns,
		InputTokens:  task.Usage.InputTokens,
		OutputTokens: task.Usage.OutputTokens,
	}
}

// formatSubagentTime 把时间格式化成 RFC3339Nano；零值返回空字符串。
func formatSubagentTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}
