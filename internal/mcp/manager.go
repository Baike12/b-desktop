package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Baike12/b-desktop/internal/tool"
)

const (
	// defaultConnectTimeout 是 Manager 在没有调用方 ctx 时（Tools 无 ctx）启动服务器的上限。
	defaultConnectTimeout = 60 * time.Second
)

// Manager 错误哨兵。
var (
	// ErrManagerClosed 表示 Manager 已关闭。
	ErrManagerClosed = errors.New("mcp: manager closed")
	// ErrUnknownServer 表示调用的服务器不在当前配置中。
	ErrUnknownServer = errors.New("mcp: unknown server")
)

// State 是服务器连接状态。
type State string

const (
	// StateStopped 表示尚未尝试启动。
	StateStopped State = "stopped"
	// StateReady 表示 initialize 成功，工具列表可用。
	StateReady State = "ready"
	// StateError 表示启动或握手失败。
	StateError State = "error"
)

// ServerStatus 是一个 MCP 服务器的配置与运行状态快照，供前端展示。
type ServerStatus struct {
	Name            string
	Command         string
	Args            []string
	EnvKeys         []string
	State           State
	Error           string
	ToolCount       int
	ProtocolVersion string
	ServerName      string
	ServerVersion   string
}

// serverState 是 Manager 内部维护的单个服务器状态。
type serverState struct {
	name      string
	cfg       ServerConfig
	client    *Client
	state     State
	err       error
	attempted bool
	tools     []ToolInfo
}

// Manager 管理 mcp.json 中的服务器连接与工具快照。
//
// 连接是惰性的：构造 Manager 不启动任何进程，首次 Tools / ListServers / RefreshTools
// 才读取配置并启动服务器；启动失败的服务器不会被反复重试，直到 Reload。
// 所有方法并发安全，Close 可重复调用。
type Manager struct {
	path string
	dial DialFunc

	mu      sync.Mutex
	cfg     Config
	loaded  bool
	names   []string
	servers map[string]*serverState
	closed  bool
}

// NewManager 创建管理器。path 是 mcp.json 路径（空表示无配置）；
// dial 为 nil 时使用 StartStdio，测试可注入内存传输。
func NewManager(path string, dial DialFunc) *Manager {
	if dial == nil {
		dial = StartStdio
	}
	return &Manager{
		path:    path,
		dial:    dial,
		servers: make(map[string]*serverState),
	}
}

// Tools 返回当前可用工具的包装，必要时惰性启动服务器。
//
// 该方法满足扩展契约的 Tools() []tool.Tool 签名，因此没有 ctx 与 error：
// 内部使用 defaultConnectTimeout 限时启动；单个服务器失败只记入状态，
// 不影响其它服务器的工具。需要错误时用 RefreshTools。
func (m *Manager) Tools() []tool.Tool {
	ctx, cancel := context.WithTimeout(context.Background(), defaultConnectTimeout)
	defer cancel()
	tools, _ := m.RefreshTools(ctx)
	return tools
}

// RefreshTools 惰性加载配置、启动未连接的服务器并刷新工具列表。
// 返回错误只表示配置不可读；服务器级失败记录在 ListServers 的状态中。
func (m *Manager) RefreshTools(ctx context.Context) ([]tool.Tool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrManagerClosed
	}
	if err := m.ensureLoadedLocked(); err != nil {
		return nil, err
	}
	m.refreshLocked(ctx)
	return m.toolsLocked(), nil
}

// ListServers 返回配置中的服务器及其状态，服务器名按字典序排列。
// 与 Tools 一样会惰性启动服务器；错误只表示配置不可读。
func (m *Manager) ListServers(ctx context.Context) ([]ServerStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrManagerClosed
	}
	if err := m.ensureLoadedLocked(); err != nil {
		return nil, err
	}
	m.refreshLocked(ctx)

	out := make([]ServerStatus, 0, len(m.names))
	for _, name := range m.names {
		s := m.servers[name]
		status := ServerStatus{
			Name:            s.name,
			Command:         s.cfg.Command,
			Args:            append([]string(nil), s.cfg.Args...),
			EnvKeys:         s.cfg.envKeys(),
			State:           s.state,
			ToolCount:       len(s.tools),
			ProtocolVersion: s.protocolVersion(),
			ServerName:      s.serverInfo().Name,
			ServerVersion:   s.serverInfo().Version,
		}
		if s.err != nil {
			status.Error = s.err.Error()
		}
		out = append(out, status)
	}
	return out, nil
}

// Reload 关闭全部连接、重新读取配置文件并重连所有服务器。
// 返回配置文件错误与各服务器连接错误的合并结果；单个服务器失败不影响其它服务器。
func (m *Manager) Reload(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrManagerClosed
	}

	errs := m.closeAllLocked()
	m.loaded = false
	m.cfg = Config{}
	m.names = nil
	m.servers = make(map[string]*serverState)

	if err := m.ensureLoadedLocked(); err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, name := range m.names {
		s := m.servers[name]
		s.attempted = true
		if err := m.connectLocked(ctx, s); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Close 结束全部会话并杀死所有子进程。可重复调用。
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	return errors.Join(m.closeAllLocked()...)
}

// ensureLoadedLocked 首次读取配置并建立服务器表；失败不缓存。
func (m *Manager) ensureLoadedLocked() error {
	if m.loaded {
		return nil
	}
	cfg, err := LoadConfig(m.path)
	if err != nil {
		return fmt.Errorf("mcp: load config: %w", err)
	}
	m.cfg = cfg
	m.names = cfg.names()
	m.servers = make(map[string]*serverState, len(m.names))
	for _, name := range m.names {
		m.servers[name] = &serverState{
			name:  name,
			cfg:   cfg.MCPServers[name],
			state: StateStopped,
		}
	}
	m.loaded = true
	return nil
}

// refreshLocked 连接未就绪的服务器，并在服务器报告工具变化时重新拉取列表。
func (m *Manager) refreshLocked(ctx context.Context) {
	for _, name := range m.names {
		s := m.servers[name]
		if s.client != nil && s.client.Closed() {
			if s.state == StateReady {
				s.state = StateError
				s.err = s.client.Err()
			}
			s.client = nil
		}
		if s.state == StateReady {
			if s.client != nil && s.client.Dirty() {
				m.listToolsLocked(ctx, s)
				s.client.ClearDirty()
			}
			continue
		}
		if s.attempted {
			continue
		}
		s.attempted = true
		_ = m.connectLocked(ctx, s)
	}
}

// connectLocked 启动进程、完成 initialize 并拉取工具列表。
func (m *Manager) connectLocked(ctx context.Context, s *serverState) error {
	if err := ctx.Err(); err != nil {
		return m.setServerError(s, fmt.Errorf("mcp: connect %q: %w", s.name, err))
	}
	rw, err := m.dial(ctx, s.name, s.cfg)
	if err != nil {
		return m.setServerError(s, fmt.Errorf("mcp: connect %q: %w", s.name, err))
	}
	client := NewClient(s.name, rw)
	if err := client.Initialize(ctx); err != nil {
		_ = client.Close()
		return m.setServerError(s, fmt.Errorf("mcp: connect %q: %w", s.name, err))
	}
	s.client = client
	s.state = StateReady
	s.err = nil
	s.tools = nil
	if client.ToolsSupported() {
		m.listToolsLocked(ctx, s)
	}
	return nil
}

// listToolsLocked 刷新单个服务器的工具列表；失败只记录错误，不断开连接。
func (m *Manager) listToolsLocked(ctx context.Context, s *serverState) {
	infos, err := s.client.ListTools(ctx)
	if err != nil {
		s.err = fmt.Errorf("mcp: list tools of %q: %w", s.name, err)
		return
	}
	s.tools = infos
	s.err = nil
}

// setServerError 记录服务器错误并返回它。
func (m *Manager) setServerError(s *serverState, err error) error {
	s.state = StateError
	s.err = err
	s.tools = nil
	return err
}

// toolsLocked 把各服务器的工具定义包装成 tool.Tool；规范化后重名的条目被跳过。
func (m *Manager) toolsLocked() []tool.Tool {
	out := make([]tool.Tool, 0, 8)
	seen := make(map[string]struct{}, 8)
	for _, name := range m.names {
		s := m.servers[name]
		if s.state != StateReady || s.client == nil {
			continue
		}
		for _, info := range s.tools {
			wrapped := newClientTool(m, s.name, info)
			if _, dup := seen[wrapped.Name()]; dup {
				continue
			}
			seen[wrapped.Name()] = struct{}{}
			out = append(out, wrapped)
		}
	}
	return out
}

// closeAllLocked 关闭所有连接并收集错误。
func (m *Manager) closeAllLocked() []error {
	var errs []error
	for _, s := range m.servers {
		if s.client == nil {
			continue
		}
		if err := s.client.Close(); err != nil {
			errs = append(errs, err)
		}
		s.client = nil
		s.state = StateStopped
	}
	return errs
}

// callTool 解析当前连接并调用远端工具。
func (m *Manager) callTool(ctx context.Context, server, remote string, input json.RawMessage) (tool.Result, error) {
	client, err := m.clientFor(ctx, server)
	if err != nil {
		return tool.Result{}, wrapRunError(err, server, remote)
	}
	res, err := client.CallTool(ctx, remote, input)
	if err != nil {
		return tool.Result{}, wrapRunError(err, server, remote)
	}
	return tool.Result{
		Content: renderCallResult(res),
		IsError: res.IsError,
		Meta:    resultMeta(server, remote),
	}, nil
}

// clientFor 返回可用的连接；服务器未连接时惰性连接一次。
func (m *Manager) clientFor(ctx context.Context, name string) (*Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrManagerClosed
	}
	if err := m.ensureLoadedLocked(); err != nil {
		return nil, err
	}
	s := m.servers[name]
	if s == nil {
		return nil, fmt.Errorf("%w: %q", ErrUnknownServer, name)
	}
	if s.state == StateReady && s.client != nil && !s.client.Closed() {
		return s.client, nil
	}
	if s.attempted && s.err != nil {
		return nil, s.err
	}
	s.attempted = true
	if err := m.connectLocked(ctx, s); err != nil {
		return nil, err
	}
	return s.client, nil
}

// protocolVersion 返回服务器协商的协议版本。
func (s *serverState) protocolVersion() string {
	if s.client == nil {
		return ""
	}
	return s.client.ProtocolVersion()
}

// serverInfo 返回服务器自报的名称与版本。
func (s *serverState) serverInfo() ServerInfo {
	if s.client == nil {
		return ServerInfo{}
	}
	return s.client.ServerInfo()
}
