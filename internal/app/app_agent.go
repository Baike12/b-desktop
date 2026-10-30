package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Baike12/b-desktop/internal/acp"
	"github.com/Baike12/b-desktop/internal/acp/claude"
	"github.com/Baike12/b-desktop/internal/mcp"
	"github.com/Baike12/b-desktop/internal/permission"
	"github.com/Baike12/b-desktop/internal/session"
)

// 本地 agent 在 agent 选择器中的 ID。benben 不走 ACP：它由 internal/agent
// 引擎直接驱动；注册表只收 ACP 风格的外部后端。
const benbenAgentID = "benben"

// acp 绑定层错误哨兵。
var (
	// ErrAgentUnknown 表示请求的 agent 不存在。
	ErrAgentUnknown = errors.New("app: unknown agent")
	// ErrAgentUnavailable 表示请求的 agent 当前不可用（如未安装 CLI）。
	ErrAgentUnavailable = errors.New("app: agent unavailable")
	// ErrAgentSwitchRunning 表示运行中不能切换 agent。
	ErrAgentSwitchRunning = errors.New("app: cannot switch agent while turn running")
	// ErrAgentUnsupportedOp 表示该操作不适用于外部 agent。
	ErrAgentUnsupportedOp = errors.New("app: operation not supported by external agent")
)

// AgentDTO 是 agent 选择器的一个条目。
type AgentDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Available 为 false 时 Detail 说明原因，前端禁用该选项。
	Available bool   `json:"available"`
	Detail    string `json:"detail,omitempty"`
}

// ListAgents 返回可选的 agent 清单：本地 benben 恒在首位，其后是注册表里的
// ACP 后端（含可用性探测）。
func (a *App) ListAgents() ([]AgentDTO, error) {
	out := []AgentDTO{{
		ID:          benbenAgentID,
		Name:        "Benben Agent",
		Description: "本地内置 agent：直连模型 API，使用本应用的工具、权限与记忆体系",
		Available:   true,
	}}
	registry := a.agentRegistry()
	ctx, cancel := context.WithTimeout(a.lifeCtx(), 10*time.Second)
	defer cancel()
	for _, item := range registry.List(ctx) {
		out = append(out, AgentDTO{
			ID:          item.ID,
			Name:        item.Name,
			Description: item.Description,
			Available:   item.Available,
			Detail:      item.Detail,
		})
	}
	return out, nil
}

// SetSessionAgent 切换一个会话使用的 agent。运行中不允许切换；
// 切换立即写 agent_state 记录，恢复会话时沿用。
func (a *App) SetSessionAgent(sessionID, agentID string) error {
	if err := session.ValidateID(sessionID); err != nil {
		return fmt.Errorf("app: set session agent: %w", err)
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		agentID = benbenAgentID
	}
	rt, err := a.runtimeForSend(sessionID)
	if err != nil {
		return err
	}

	rt.mu.Lock()
	if rt.closed {
		rt.mu.Unlock()
		return ErrSessionClosed
	}
	if rt.running {
		rt.mu.Unlock()
		return ErrAgentSwitchRunning
	}
	current := rt.agent
	if current == "" {
		current = benbenAgentID
	}
	rt.mu.Unlock()
	if current == agentID {
		return nil
	}
	if agentID != benbenAgentID {
		if err := a.probeAgent(agentID); err != nil {
			return err
		}
	}

	// 键随 agent 记录写入：跨 agent 切换保留各自的会话键。
	rt.mu.Lock()
	rt.agent = agentID
	meta := map[string]string{"agent": agentID}
	if key := rt.agentKeys[agentID]; key != "" {
		meta[agentID+".key"] = key
	}
	rt.mu.Unlock()

	store, _, err := a.sessionStore()
	if err != nil {
		return err
	}
	rec := session.Record{
		Type:      session.RecordAgentState,
		SessionID: sessionID,
		Time:      time.Now(),
		Meta:      meta,
	}
	if err := store.Append(a.lifeCtx(), rec); err != nil {
		return fmt.Errorf("app: persist session agent: %w", err)
	}
	return nil
}

// probeAgent 校验 agent 存在且可用。
func (a *App) probeAgent(agentID string) error {
	registry := a.agentRegistry()
	ctx, cancel := context.WithTimeout(a.lifeCtx(), 10*time.Second)
	defer cancel()
	for _, item := range registry.List(ctx) {
		if item.ID == agentID {
			if !item.Available {
				return fmt.Errorf("%w: %s", ErrAgentUnavailable, item.Detail)
			}
			return nil
		}
	}
	return fmt.Errorf("%w: %s", ErrAgentUnknown, agentID)
}

// agentRegistry 返回进程内唯一的 ACP 后端注册表（惰性创建）。
func (a *App) agentRegistry() *acp.Registry {
	if cached, err := a.extensionStore("acp-registry", func() (any, error) {
		registry := acp.NewRegistry()
		registry.Register(acp.AgentInfo{
			ID:          claude.BackendID,
			Name:        "Claude Code",
			Description: "通过内置 ACP 桥接驱动 claude CLI（对齐 zed 的 claude-agent-acp）",
		}, func(ctx context.Context) (acp.Backend, error) {
			if _, err := claude.ResolveCLI(os.Getenv); err != nil {
				return nil, err
			}
			return claude.NewBackend(), nil
		})
		return registry, nil
	}); err == nil {
		return cached.(*acp.Registry)
	}
	// 注册表创建不会失败；走到这里说明 init 出错，退回空注册表。
	return acp.NewRegistry()
}

// agentKeyFromState 从 agent_state 元信息里取指定 agent 的会话键。
func agentKeyFromState(state map[string]string, agentID string) string {
	if state == nil || agentID == "" {
		return ""
	}
	return state[agentID+".key"]
}

// ensureBridgeLocked 返回该会话指定 agent 的 ACP 桥接层；agent 变化时重建。
// 调用方必须持有 rt.mu 且没有进行中的轮次。
func (a *App) ensureBridgeLocked(rt *sessionRuntime, agentID, permissionMode string) (*acp.Bridge, error) {
	if rt.acp != nil && rt.acpAgent == agentID {
		return rt.acp, nil
	}
	if rt.acp != nil {
		_ = rt.acp.Close()
		rt.acp = nil
		rt.acpAgent = ""
	}
	backend, err := a.agentRegistry().Backend(a.lifeCtx(), agentID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAgentUnavailable, err)
	}
	bridge := acp.NewBridge(backend, acp.BridgeOptions{
		SessionID:      rt.id,
		WorkDir:        rt.workDir,
		Approvals:      rt.approvals,
		Store:          a.store,
		ResumeKey:      rt.agentKeys[agentID],
		PermissionMode: permissionMode,
		AgentID:        agentID,
		MCPServers:     a.mcpServersForAgent(),
	})
	rt.acp = bridge
	rt.acpAgent = agentID
	return bridge, nil
}

// mcpServersForAgent 读取用户级 MCP 配置并转成 ACP 参数；读取失败返回 nil
// （MCP 是增强能力，缺配不阻断会话）。
func (a *App) mcpServersForAgent() []acp.MCPServer {
	path, err := a.mcpConfigPath()
	if err != nil {
		return nil
	}
	config, err := mcp.LoadConfig(path)
	if err != nil || len(config.MCPServers) == 0 {
		return nil
	}
	names := make([]string, 0, len(config.MCPServers))
	for name := range config.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]acp.MCPServer, 0, len(names))
	for _, name := range names {
		server := config.MCPServers[name]
		out = append(out, acp.MCPServer{
			Name:    name,
			Type:    "stdio",
			Command: server.Command,
			Args:    server.Args,
			Env:     server.Env,
		})
	}
	return out
}

// modeString 返回会话生效的权限模式字符串。
func (a *App) modeString(rt *sessionRuntime, cfg Config) string {
	mode := cfg.PermissionMode
	if m := rt.permissionMode(); m != "" {
		mode = string(m)
	}
	if _, err := permission.ParseMode(mode); err != nil {
		mode = string(permission.ModeDefault)
	}
	return mode
}
