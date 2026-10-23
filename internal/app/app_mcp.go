package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Baike12/b-desktop/internal/mcp"
	"github.com/Baike12/b-desktop/internal/tool"
)

// mcpRequestTimeout 是 MCP 绑定方法等待服务器启动与应答的上限，
// 避免某个不响应的服务器把界面调用挂死。
const mcpRequestTimeout = 60 * time.Second

// MCP 绑定层错误哨兵。
var (
	// ErrMCPConfigInvalid 表示 ~/.b-desktop/mcp.json 无法解析或字段非法。
	ErrMCPConfigInvalid = errors.New("app: invalid mcp config")
	// ErrMCPUnavailable 表示 MCP 管理器不可用（应用已关闭或组件类型异常）。
	ErrMCPUnavailable = errors.New("app: mcp unavailable")
)

// MCPServerDTO 是 mcp.json 中一个服务器的配置与状态视图。
// 环境变量只暴露键名，值可能含密钥，不下发前端。
type MCPServerDTO struct {
	Name            string   `json:"name"`
	Command         string   `json:"command"`
	Args            []string `json:"args"`
	EnvKeys         []string `json:"envKeys"`
	State           string   `json:"state"`
	Error           string   `json:"error,omitempty"`
	ToolCount       int      `json:"toolCount"`
	ProtocolVersion string   `json:"protocolVersion,omitempty"`
	ServerName      string   `json:"serverName,omitempty"`
	ServerVersion   string   `json:"serverVersion,omitempty"`
}

// MCPToolDTO 是一个远端工具的前端视图。
//
// InputSchema 使用 any 而不是 json.RawMessage：Wails 的 TypeScript 生成器
// 把 []byte 映射成 number[]，而 schema 在线格式上是 JSON 对象。
type MCPToolDTO struct {
	Name        string `json:"name"`
	Server      string `json:"server"`
	Tool        string `json:"tool"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
	ReadOnly    bool   `json:"readOnly"`
}

// mcpToolMeta 是远端工具包装额外暴露的来源信息。
type mcpToolMeta interface {
	// Server 返回配置中的服务器名。
	Server() string
	// RemoteName 返回服务器侧的原始工具名。
	RemoteName() string
}

// mcpComponent 返回进程内唯一的 MCP 管理器，首次调用时创建。
// 构造只计算配置路径，不读取文件、不启动进程；服务器在首次使用时惰性启动。
func (a *App) mcpComponent() (*mcp.Manager, error) {
	value, err := a.extensionStore("mcp", func() (any, error) {
		dir, err := a.settingsDir()
		if err != nil {
			return nil, fmt.Errorf("app: mcp settings dir: %w", err)
		}
		return mcp.NewManager(filepath.Join(dir, mcp.ConfigFileName), nil), nil
	})
	if err != nil {
		return nil, fmt.Errorf("app: mcp component: %w", err)
	}
	manager, ok := value.(*mcp.Manager)
	if !ok {
		return nil, fmt.Errorf("%w: unexpected component type %T", ErrMCPUnavailable, value)
	}
	return manager, nil
}

// ListMcpServers 返回 mcp.json 中的服务器及其连接状态，服务器名按字典序排列。
// 首次调用会惰性启动服务器并完成 initialize，耗时受 mcpRequestTimeout 限制。
func (a *App) ListMcpServers() ([]MCPServerDTO, error) {
	manager, err := a.mcpComponent()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(a.lifeCtx(), mcpRequestTimeout)
	defer cancel()

	statuses, err := manager.ListServers(ctx)
	if err != nil {
		if errors.Is(err, mcp.ErrManagerClosed) {
			return nil, fmt.Errorf("%w: %v", ErrMCPUnavailable, err)
		}
		return nil, fmt.Errorf("%w: %v", ErrMCPConfigInvalid, err)
	}
	out := make([]MCPServerDTO, 0, len(statuses))
	for _, status := range statuses {
		out = append(out, toMCPServerDTO(status))
	}
	return out, nil
}

// ListMcpTools 返回全部已连接服务器的工具，工具名形如 mcp__<server>__<tool>。
// 首次调用会惰性启动服务器；单个服务器连接失败不影响其它服务器的工具。
func (a *App) ListMcpTools() ([]MCPToolDTO, error) {
	manager, err := a.mcpComponent()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(a.lifeCtx(), mcpRequestTimeout)
	defer cancel()

	tools, err := manager.RefreshTools(ctx)
	if err != nil {
		if errors.Is(err, mcp.ErrManagerClosed) {
			return nil, fmt.Errorf("%w: %v", ErrMCPUnavailable, err)
		}
		return nil, fmt.Errorf("%w: %v", ErrMCPConfigInvalid, err)
	}
	out := make([]MCPToolDTO, 0, len(tools))
	for _, item := range tools {
		out = append(out, toMCPToolDTO(item))
	}
	return out, nil
}

// ReloadMcp 重新读取 mcp.json、关闭旧连接并重连所有服务器。
// 返回错误包含配置错误与各服务器的连接错误；单个服务器失败不影响其它服务器。
func (a *App) ReloadMcp() error {
	manager, err := a.mcpComponent()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(a.lifeCtx(), mcpRequestTimeout)
	defer cancel()

	if err := manager.Reload(ctx); err != nil {
		if errors.Is(err, mcp.ErrManagerClosed) {
			return fmt.Errorf("%w: %v", ErrMCPUnavailable, err)
		}
		return fmt.Errorf("app: reload mcp: %w", err)
	}
	return nil
}

// MCPServerInput 是新增或修改 MCP 服务器的入参。
// Env 中值为空的键表示保留磁盘上的原值，前端因此不需要回传密钥明文。
type MCPServerInput struct {
	Name    string            `json:"name"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// ErrMCPConfigPath 表示无法确定 mcp.json 的位置。
var ErrMCPConfigPath = errors.New("app: mcp config path unavailable")

// mcpConfigPath 返回 ~/.b-desktop/mcp.json 的绝对路径。
func (a *App) mcpConfigPath() (string, error) {
	dir, err := a.settingsDir()
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrMCPConfigPath, err)
	}
	return filepath.Join(dir, mcp.ConfigFileName), nil
}

// SaveMCPServer 新增或覆盖一个 MCP 服务器，写盘后立即重连所有服务器。
// Args 为 nil 表示空参数；Env 为空值表示保留原值。
func (a *App) SaveMCPServer(in MCPServerInput) error {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return fmt.Errorf("%w: empty server name", ErrMCPConfigInvalid)
	}
	command := strings.TrimSpace(in.Command)
	if command == "" {
		return fmt.Errorf("%w: server %q has empty command", ErrMCPConfigInvalid, name)
	}
	path, err := a.mcpConfigPath()
	if err != nil {
		return err
	}
	cfg, err := mcp.LoadConfig(path)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMCPConfigInvalid, err)
	}
	existing := cfg.MCPServers[name]
	merged := make(map[string]string, len(existing.Env)+len(in.Env))
	for key, value := range existing.Env {
		merged[key] = value
	}
	for key, value := range in.Env {
		key = strings.TrimSpace(key)
		if key == "" || strings.ContainsRune(key, '=') {
			return fmt.Errorf("%w: invalid env key %q", ErrMCPConfigInvalid, key)
		}
		if strings.TrimSpace(value) == "" {
			continue
		}
		merged[key] = value
	}
	server := mcp.ServerConfig{Command: command, Args: in.Args}
	if len(merged) > 0 {
		server.Env = merged
	}
	cfg.MCPServers[name] = server
	if err := mcp.SaveConfig(path, cfg); err != nil {
		return fmt.Errorf("app: save mcp config: %w", err)
	}
	return a.reloadMCP(context.Background())
}

// DeleteMCPServer 删除一个 MCP 服务器配置，写盘后立即重连剩余服务器。
func (a *App) DeleteMCPServer(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("%w: empty server name", ErrMCPConfigInvalid)
	}
	path, err := a.mcpConfigPath()
	if err != nil {
		return err
	}
	cfg, err := mcp.LoadConfig(path)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMCPConfigInvalid, err)
	}
	if _, ok := cfg.MCPServers[name]; !ok {
		return fmt.Errorf("%w: unknown server %q", ErrMCPConfigInvalid, name)
	}
	delete(cfg.MCPServers, name)
	if err := mcp.SaveConfig(path, cfg); err != nil {
		return fmt.Errorf("app: save mcp config: %w", err)
	}
	return a.reloadMCP(context.Background())
}

// reloadMCP 重新读取配置并重连服务器，供配置写盘后复用。
func (a *App) reloadMCP(ctx context.Context) error {
	manager, err := a.mcpComponent()
	if err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, mcpRequestTimeout)
	defer cancel()
	if err := manager.Reload(callCtx); err != nil {
		return fmt.Errorf("app: reload mcp: %w", err)
	}
	return nil
}

// toMCPServerDTO 把领域状态转成前端视图。
func toMCPServerDTO(status mcp.ServerStatus) MCPServerDTO {
	args := status.Args
	if args == nil {
		args = []string{}
	}
	envKeys := status.EnvKeys
	if envKeys == nil {
		envKeys = []string{}
	}
	return MCPServerDTO{
		Name:            status.Name,
		Command:         status.Command,
		Args:            args,
		EnvKeys:         envKeys,
		State:           string(status.State),
		Error:           status.Error,
		ToolCount:       status.ToolCount,
		ProtocolVersion: status.ProtocolVersion,
		ServerName:      status.ServerName,
		ServerVersion:   status.ServerVersion,
	}
}

// toMCPToolDTO 把工具包装转成前端视图，schema 解码成 JSON 对象。
func toMCPToolDTO(item tool.Tool) MCPToolDTO {
	dto := MCPToolDTO{
		Name:        item.Name(),
		Description: item.Description(),
		ReadOnly:    item.ReadOnly(nil),
	}
	if meta, ok := item.(mcpToolMeta); ok {
		dto.Server = meta.Server()
		dto.Tool = meta.RemoteName()
	}
	if raw := item.InputSchema(); len(raw) > 0 {
		var schema any
		if err := json.Unmarshal(raw, &schema); err == nil {
			dto.InputSchema = schema
		}
	}
	return dto
}
