// Package app 承载 Wails 绑定给前端的应用服务。
//
// 绑定方法只做参数校验、DTO 转换与编排：Agent 循环、工具执行、权限判定与
// transcript 落盘都在 internal/ 下的领域包里。前端通过 frontend/wailsjs/go/app/App
// 调用这些方法，并通过 "agent:event" 事件接收流式输出，不存在轮询接口。
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Baike12/b-desktop/internal/agent"
	"github.com/Baike12/b-desktop/internal/llm"
	"github.com/Baike12/b-desktop/internal/permission"
	"github.com/Baike12/b-desktop/internal/project"
	"github.com/Baike12/b-desktop/internal/session"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// 绑定层的固定值。
const (
	// EventNameAgent 是后端主动推送 Agent 事件的唯一 Wails 事件名。
	EventNameAgent = "agent:event"
	// EventNameSessionTitle 是会话标题更新事件名。标题由首轮消息异步生成，
	// 前端收到后只更新对应列表项，不重新加载会话。
	EventNameSessionTitle = "session:title"
	// shutdownTimeout 是 Shutdown 等待进行中轮次退出的上限。
	shutdownTimeout = 3 * time.Second
	// maxPromptBytes 是单条用户输入的字节上限，防止前端误传超大文本。
	maxPromptBytes = 1 << 20

	defaultProvider           = "anthropic"
	defaultContextWindow      = 200000
	defaultMaxOutputTokens    = 8192
	defaultMaxTurns           = 100
	defaultMaxToolConcurrency = 10
	defaultDataDirName        = "b-desktop"
	defaultConfigDirName      = ".b-desktop"
	defaultConfigFileName     = "config.json"
)

// 绑定层错误哨兵。底层错误一律用 %w 包装，前端可用 errors.Is 对应。
var (
	// ErrClosed 表示应用已关闭，不再接受新轮次。
	ErrClosed = errors.New("app: closed")
	// ErrStoreUnavailable 表示会话存储尚未初始化。
	ErrStoreUnavailable = errors.New("app: session store unavailable")
	// ErrSessionNotFound 表示会话在内存与磁盘上都不存在。
	ErrSessionNotFound = errors.New("app: session not found")
	// ErrSessionClosed 表示会话运行时已释放。
	ErrSessionClosed = errors.New("app: session closed")
	// ErrEmptyPrompt 表示用户输入为空。
	ErrEmptyPrompt = errors.New("app: empty prompt")
	// ErrPromptTooLong 表示用户输入超过 maxPromptBytes。
	ErrPromptTooLong = errors.New("app: prompt too long")
	// ErrTurnRunning 表示该会话已有进行中的轮次。
	ErrTurnRunning = errors.New("app: turn already running")
	// ErrApprovalNotFound 表示审批请求不存在、已结束或不属于该会话。
	ErrApprovalNotFound = errors.New("app: approval not found")
	// ErrInvalidOption 表示审批选项不在四个标准选项内。
	ErrInvalidOption = errors.New("app: invalid approval option")
	// ErrModelNotConfigured 表示未配置模型名，无法发起模型请求。
	ErrModelNotConfigured = errors.New("app: model not configured")
	// ErrTooManyImages 表示随消息提交的图片数量超过 llm.MaxImagesPerMessage。
	ErrTooManyImages = errors.New("app: too many images")
	// ErrInvalidImage 表示图片附件非法：格式不支持、base64 损坏或超过体积上限。
	ErrInvalidImage = errors.New("app: invalid image")
	// ErrModelNoImageSupport 表示当前模型不支持图像输入（llm.ModelInfoOf 判定）。
	ErrModelNoImageSupport = errors.New("app: model does not support image input")
	// ErrInvalidProviderConfig 表示 Provider 配置非法。
	ErrInvalidProviderConfig = errors.New("app: invalid provider config")
	// ErrProjectDialogCanceled 表示用户取消了打开项目对话框。
	ErrProjectDialogCanceled = errors.New("app: project dialog canceled")
)

// Sender 是一次事件发送。生产实现是 runtime.EventsEmit，测试注入内存实现。
// 实现必须快速返回，不得阻塞：Emitter 在持锁期间调用它。
type Sender func(ctx context.Context, name string, payload any)

// Emitter 是进程内唯一的事件发射器：互斥锁 + 单调递增的全局 seq。
// 并发调用时事件按 seq 顺序串行发送，前端据此去重与排序。
type Emitter struct {
	mu  sync.Mutex
	seq int64
	snd Sender
}

// NewEmitter 创建发射器；snd 为 nil 时事件被丢弃。
func NewEmitter(snd Sender) *Emitter { return &Emitter{snd: snd} }

// Emit 为事件分配 seq 并同步发送。事件类型与 internal/agent/event.go 完全一致，
// 直接序列化 agent.Event，绑定层不做二次建模。
func (e *Emitter) Emit(ctx context.Context, ev agent.Event) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seq++
	ev.Seq = e.seq
	if e.snd != nil {
		e.snd(ctx, EventNameAgent, ev)
	}
}

// Config 是应用级配置。零值字段由 normalized 填充默认值。
type Config struct {
	Provider        string // 供应商：anthropic | openai
	APIKey          string // 模型密钥；只保存在进程内，绝不下发前端
	BaseURL         string // 空时使用供应商默认地址
	Model           string // 模型名；为空时 SendMessage 返回 ErrModelNotConfigured
	PermissionMode  string // 五档权限模式之一
	DataDir         string // 会话存储根目录
	WorkDir         string // 新会话的默认工作目录
	ContextWindow   int    // 上下文窗口（token）
	MaxOutputTokens int    // 单次请求最大输出（token）
	// ThinkingEnabled 打开扩展思考；ThinkingBudget 是思考预算（token），0 取默认值。
	ThinkingEnabled    bool
	ThinkingBudget     int
	MaxTurns           int // 单轮最大模型调用次数，0 取默认 100
	MaxToolConcurrency int // 工具并发上限，0 取默认 10
	// ConfigPath 是配置文件路径，供设置页保存后回写；空表示不落盘（测试注入）。
	ConfigPath string
}

// Getenv 读取环境变量的函数；测试可替换 LoadConfig 的注入点。
type Getenv func(string) string

// fileConfig 是 ~/.b-desktop/config.json 的字段集合。
// 字段与前端 DTO 同名，便于对照；未知字段被忽略，方便后续版本向前兼容。
type fileConfig struct {
	Provider           string `json:"provider"`
	APIKey             string `json:"apiKey"`
	BaseURL            string `json:"baseUrl"`
	Model              string `json:"model"`
	PermissionMode     string `json:"permissionMode"`
	DataDir            string `json:"dataDir"`
	WorkDir            string `json:"workDir"`
	ContextWindow      int    `json:"contextWindow"`
	MaxOutputTokens    int    `json:"maxOutputTokens"`
	ThinkingEnabled    bool   `json:"thinkingEnabled"`
	ThinkingBudget     int    `json:"thinkingBudget"`
	MaxTurns           int    `json:"maxTurns"`
	MaxToolConcurrency int    `json:"maxToolConcurrency"`
}

// defaultConfigPath 返回 ~/.b-desktop/config.json。
func defaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("app: resolve home dir: %w", err)
	}
	return filepath.Join(home, defaultConfigDirName, defaultConfigFileName), nil
}

// settingsDir 返回用户级设置目录 ~/.b-desktop，并确保它以 0700 权限存在。
// 记忆、技能、hooks、MCP 配置等扩展数据都存放在该目录下。
func (a *App) settingsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("app: resolve home dir: %w", err)
	}
	dir := filepath.Join(home, defaultConfigDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("app: create settings dir %s: %w", dir, err)
	}
	return dir, nil
}

// loadConfigFile 读取 JSON 配置文件；文件不存在时返回空配置，不视为错误。
func loadConfigFile(path string) (Config, error) {
	if path == "" {
		return Config{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("app: read config %s: %w", path, err)
	}
	var raw fileConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return Config{}, fmt.Errorf("app: parse config %s: %w", path, err)
	}
	return Config{
		Provider:           raw.Provider,
		APIKey:             raw.APIKey,
		BaseURL:            raw.BaseURL,
		Model:              raw.Model,
		PermissionMode:     raw.PermissionMode,
		DataDir:            raw.DataDir,
		WorkDir:            raw.WorkDir,
		ContextWindow:      raw.ContextWindow,
		MaxOutputTokens:    raw.MaxOutputTokens,
		ThinkingEnabled:    raw.ThinkingEnabled,
		ThinkingBudget:     raw.ThinkingBudget,
		MaxTurns:           raw.MaxTurns,
		MaxToolConcurrency: raw.MaxToolConcurrency,
	}, nil
}

// saveConfigFile 把配置原子写回 JSON 文件，权限 0600（文件含 API Key）。
// path 为空时是空操作，便于测试使用纯内存配置。
func saveConfigFile(path string, cfg Config) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	raw := fileConfig{
		Provider:           cfg.Provider,
		APIKey:             cfg.APIKey,
		BaseURL:            cfg.BaseURL,
		Model:              cfg.Model,
		PermissionMode:     cfg.PermissionMode,
		DataDir:            cfg.DataDir,
		WorkDir:            cfg.WorkDir,
		ContextWindow:      cfg.ContextWindow,
		MaxOutputTokens:    cfg.MaxOutputTokens,
		ThinkingEnabled:    cfg.ThinkingEnabled,
		ThinkingBudget:     cfg.ThinkingBudget,
		MaxTurns:           cfg.MaxTurns,
		MaxToolConcurrency: cfg.MaxToolConcurrency,
	}
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("app: marshal config: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("app: create config dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return fmt.Errorf("app: create temp config: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("app: chmod temp config: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("app: write temp config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("app: sync temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("app: close temp config: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("app: replace config %s: %w", path, err)
	}
	return nil
}

// LoadConfig 先读 ~/.b-desktop/config.json，再用 BDESKTOP_* 环境变量覆盖（同名字段以环境变量为准）。
// BDESKTOP_CONFIG 可以指定另一份配置文件；文件不存在时使用默认值。
// 返回的错误只表示配置文件不可读、数值或枚举非法；缺少 API Key 与 Model 不算配置错误，
// 分别由 GetProviderConfig().HasAPIKey 与 SendMessage 报告。
func LoadConfig(getenv Getenv) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}

	var errs []error
	path := strings.TrimSpace(getenv("BDESKTOP_CONFIG"))
	if path == "" {
		p, err := defaultConfigPath()
		if err != nil {
			errs = append(errs, err)
		}
		path = p
	}
	cfg, err := loadConfigFile(path)
	if err != nil {
		errs = append(errs, err)
	}
	cfg.ConfigPath = path

	override := func(name string, dst *string) {
		if v := getenv(name); v != "" {
			*dst = v
		}
	}
	override("BDESKTOP_PROVIDER", &cfg.Provider)
	override("BDESKTOP_API_KEY", &cfg.APIKey)
	override("BDESKTOP_BASE_URL", &cfg.BaseURL)
	override("BDESKTOP_MODEL", &cfg.Model)
	override("BDESKTOP_PERMISSION_MODE", &cfg.PermissionMode)
	override("BDESKTOP_DATA_DIR", &cfg.DataDir)
	override("BDESKTOP_WORKDIR", &cfg.WorkDir)

	readInt := func(name string, dst *int) {
		raw := strings.TrimSpace(getenv(name))
		if raw == "" {
			return
		}
		n, err := strconv.Atoi(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("app: %s: %w", name, err))
			return
		}
		*dst = n
	}
	readInt("BDESKTOP_CONTEXT_WINDOW", &cfg.ContextWindow)
	readInt("BDESKTOP_MAX_OUTPUT_TOKENS", &cfg.MaxOutputTokens)
	readInt("BDESKTOP_THINKING_BUDGET", &cfg.ThinkingBudget)
	readInt("BDESKTOP_MAX_TURNS", &cfg.MaxTurns)
	readInt("BDESKTOP_MAX_TOOL_CONCURRENCY", &cfg.MaxToolConcurrency)

	// BDESKTOP_THINKING_ENABLED 单独用布尔开关：值为 "0"/"false"/"off" 关闭，其余非空值打开。
	if v := strings.TrimSpace(getenv("BDESKTOP_THINKING_ENABLED")); v != "" {
		switch strings.ToLower(v) {
		case "0", "false", "off", "no":
			cfg.ThinkingEnabled = false
		default:
			cfg.ThinkingEnabled = true
		}
	}

	normalized, err := cfg.normalized()
	if err != nil {
		errs = append(errs, err)
	}
	return normalized, errors.Join(errs...)
}

// normalized 填充默认值并校验枚举与数值范围。
// 即使返回错误，也尽量返回可用的配置，便于应用在错误状态下启动并展示原因。
func (c Config) normalized() (Config, error) {
	var errs []error

	c.Provider = strings.ToLower(strings.TrimSpace(c.Provider))
	if c.Provider == "" {
		c.Provider = defaultProvider
	}
	switch c.Provider {
	case "anthropic", "openai":
	default:
		errs = append(errs, fmt.Errorf("%w: unknown provider %q", ErrInvalidProviderConfig, c.Provider))
	}

	c.PermissionMode = strings.TrimSpace(c.PermissionMode)
	if c.PermissionMode == "" {
		c.PermissionMode = string(permission.ModeDefault)
	}
	if _, err := permission.ParseMode(c.PermissionMode); err != nil {
		errs = append(errs, fmt.Errorf("%w: %v", ErrInvalidProviderConfig, err))
	}

	if strings.TrimSpace(c.WorkDir) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			errs = append(errs, fmt.Errorf("app: resolve work dir: %w", err))
		} else {
			c.WorkDir = cwd
		}
	}
	if abs, err := filepath.Abs(c.WorkDir); err != nil {
		errs = append(errs, fmt.Errorf("app: resolve work dir %q: %w", c.WorkDir, err))
	} else {
		c.WorkDir = abs
	}

	if strings.TrimSpace(c.DataDir) == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			errs = append(errs, fmt.Errorf("app: resolve data dir: %w", err))
		} else {
			c.DataDir = filepath.Join(base, defaultDataDirName)
		}
	}

	if c.ContextWindow < 0 {
		errs = append(errs, fmt.Errorf("%w: context window must be >= 0", ErrInvalidProviderConfig))
	}
	if c.ContextWindow == 0 {
		c.ContextWindow = defaultContextWindow
	}
	if c.MaxOutputTokens < 0 {
		errs = append(errs, fmt.Errorf("%w: max output tokens must be >= 0", ErrInvalidProviderConfig))
	}
	if c.MaxOutputTokens == 0 {
		c.MaxOutputTokens = defaultMaxOutputTokens
	}
	if c.ThinkingBudget < 0 {
		errs = append(errs, fmt.Errorf("%w: thinking budget must be >= 0", ErrInvalidProviderConfig))
	}
	if c.ThinkingBudget == 0 {
		c.ThinkingBudget = llm.DefaultThinkingBudget
	}
	// Anthropic 要求 budget_tokens 严格小于 max_tokens；无法满足时不启用扩展思考，
	// 而不是让每次请求都失败。
	if c.ThinkingEnabled && c.ThinkingBudget >= c.MaxOutputTokens {
		c.ThinkingEnabled = false
	}
	if c.MaxTurns < 0 {
		errs = append(errs, fmt.Errorf("%w: max turns must be >= 0", ErrInvalidProviderConfig))
	}
	if c.MaxTurns == 0 {
		c.MaxTurns = defaultMaxTurns
	}
	if c.MaxToolConcurrency < 0 {
		errs = append(errs, fmt.Errorf("%w: max tool concurrency must be >= 0", ErrInvalidProviderConfig))
	}
	if c.MaxToolConcurrency == 0 {
		c.MaxToolConcurrency = defaultMaxToolConcurrency
	}
	return c, errors.Join(errs...)
}

// ProviderFactory 为一个会话构造 llm.Provider。测试注入 fake，避免联网。
type ProviderFactory func(cfg Config) (llm.Provider, error)

// defaultProviderFactory 按配置创建内置 Provider。
// 缺少 API Key 不在这里报错：llm 会在首次请求时返回 ErrMissingAPIKey。
func defaultProviderFactory(cfg Config) (llm.Provider, error) {
	provider, err := llm.NewProvider(cfg.Provider, llm.HTTPOptions{
		BaseURL: cfg.BaseURL,
		APIKey:  cfg.APIKey,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidProviderConfig, err)
	}
	return provider, nil
}

// sessionRuntime 是一个会话的内存运行时：引擎实例、审批通道与当前轮次取消函数。
//
// 生命周期：
//   - CreateSession 或首次 SendMessage 时创建；
//   - 引擎在首次 SendMessage 时惰性构建并 Restore；
//   - SetProviderConfig 递增 generation，下一次 SendMessage 重建引擎；
//   - Shutdown 取消当前轮次、释放引擎与待审批项。
type sessionRuntime struct {
	id        string
	workDir   string
	approvals *agent.Approvals
	// notifs 承接后台任务完成通知：运行中由循环在工具边界注入，
	// 轮次结束后由自动续跑逻辑取走；它跨引擎重建存活。
	notifs     *agent.NotificationBuffer
	mu         sync.Mutex
	engine     *agent.Engine
	engineGen  int64
	mode       permission.Mode
	turnID     string
	turnCancel context.CancelFunc
	running    bool
	closed     bool
	// titleChecked 表示本次进程内已检查过是否需要一个自动标题，
	// 避免每轮消息都读取 transcript。
	titleChecked bool
}

// newSessionRuntime 创建运行时。
func newSessionRuntime(meta session.Meta) *sessionRuntime {
	return &sessionRuntime{
		id:        meta.ID,
		workDir:   meta.WorkDir,
		approvals: agent.NewApprovals(),
		notifs:    agent.NewNotificationBuffer(),
	}
}

// isRunning 报告该会话是否已有进行中的轮次。
func (rt *sessionRuntime) isRunning() bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.running
}

// permissionMode 返回该会话显式选择的权限模式；空表示沿用全局配置。
func (rt *sessionRuntime) permissionMode() permission.Mode {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.mode
}

// DirectoryPicker 让用户选择一个目录。生产实现是 Wails 的系统目录对话框，
// 测试注入固定路径。返回空路径表示用户取消。
type DirectoryPicker func(ctx context.Context) (string, error)

// App 是 Wails 的主绑定对象。
// Wails 生命周期 context 只用于事件派发与轮次派生；请求级 ctx 不存入运行时。
type App struct {
	mu       sync.Mutex
	ctx      context.Context
	cfg      Config
	store    session.Store
	projects *project.Store
	pickDir  DirectoryPicker
	emitter  *Emitter
	// sender 与 emitter 内部持有的是同一个 Sender，供 agent:event 之外的
	// 高频事件通道（terminal:data 等）直接发送；构造后不变。
	sender   Sender
	factory  ProviderFactory
	sessions map[string]*sessionRuntime
	gen      int64
	closed   bool
	// started 表示 Wails Startup 已执行：runtime.* 调用（剪贴板、事件）需要
	// 生命周期 context，未启动时必须跳过而不是 panic。
	started bool
	turns   sync.WaitGroup
	initErr error
	// extMu 保护 extData：扩展组件（skills / memory / mcp / …）按 key 惰性创建并复用。
	extMu   sync.Mutex
	extData map[string]any
}

// New 创建生产用应用实例：读取 ~/.b-desktop/config.json，再用 BDESKTOP_* 环境变量覆盖；
// 事件发送器为 runtime.EventsEmit。
// 配置或存储初始化失败会记录在 initErr 中，由 GetProviderConfig / 绑定方法返回。
func New() *App {
	cfg, err := LoadConfig(nil)
	a := newApp(cfg, eventsEmitSender, nil, defaultProviderFactory)
	if err != nil {
		a.initErr = errors.Join(a.initErr, err)
	}
	return a
}

// eventsEmitSender 把 Sender 适配到 Wails 的 variadic EventsEmit。
func eventsEmitSender(ctx context.Context, name string, payload any) {
	runtime.EventsEmit(ctx, name, payload)
}

// NewWithDeps 创建可测试的应用实例：注入发送器、存储与 Provider 工厂。
// 不启动 Wails；cfg 非法时返回错误。
func NewWithDeps(cfg Config, sender Sender, store session.Store, factory ProviderFactory) (*App, error) {
	if sender == nil {
		return nil, errors.New("app: nil sender")
	}
	normalized, err := cfg.normalized()
	if err != nil {
		return nil, err
	}
	return newApp(normalized, sender, store, factory), nil
}

// newApp 组装应用实例。
func newApp(cfg Config, sender Sender, store session.Store, factory ProviderFactory) *App {
	if factory == nil {
		factory = defaultProviderFactory
	}
	a := &App{
		ctx:      context.Background(),
		cfg:      cfg,
		store:    store,
		emitter:  NewEmitter(sender),
		sender:   sender,
		factory:  factory,
		sessions: make(map[string]*sessionRuntime),
	}
	a.pickDir = func(ctx context.Context) (string, error) {
		a.mu.Lock()
		defaultDir := a.cfg.WorkDir
		a.mu.Unlock()
		return runtime.OpenDirectoryDialog(ctx, runtime.OpenDialogOptions{
			Title:                "打开项目",
			DefaultDirectory:     defaultDir,
			CanCreateDirectories: true,
			ResolvesAliases:      true,
		})
	}
	return a
}

// Startup 在窗口加载前由 Wails 调用：保存生命周期 context，必要时创建文件存储。
// 存储初始化失败只记录错误，不阻止窗口加载。
func (a *App) Startup(ctx context.Context) {
	a.mu.Lock()
	if ctx != nil {
		a.ctx = ctx
	}
	a.started = true
	needStore := a.store == nil
	cfg := a.cfg
	a.mu.Unlock()

	if needStore {
		store, err := session.NewFileStore(cfg.DataDir)
		a.mu.Lock()
		if a.store == nil {
			a.store = store
		}
		if err != nil {
			a.initErr = errors.Join(a.initErr, fmt.Errorf("app: open session store: %w", err))
		}
		a.mu.Unlock()
	}
}

// Shutdown 在应用退出时由 Wails 调用，幂等：
//  1. 标记关闭，取消每个会话的当前轮次与待审批项，释放引擎；
//  2. 等待进行中的轮次退出（最多 shutdownTimeout）；
//  3. 关闭扩展组件：子 Agent、MCP 连接与用量存储；
//  4. Flush 并关闭进程内唯一的会话存储。
func (a *App) Shutdown(ctx context.Context) {
	_ = ctx // 清理使用独立的后台超时，不复用可能已被取消的入参 ctx。

	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.closed = true
	runtimes := make([]*sessionRuntime, 0, len(a.sessions))
	for id, rt := range a.sessions {
		runtimes = append(runtimes, rt)
		delete(a.sessions, id)
	}
	store := a.store
	a.mu.Unlock()

	for _, rt := range runtimes {
		rt.shutdown()
	}

	done := make(chan struct{})
	go func() {
		a.turns.Wait()
		close(done)
	}()
	timer := time.NewTimer(shutdownTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}

	// 轮次已退出，扩展组件不会再有新的调用：先停调度循环，再释放子进程与会话。
	a.closeExtensions()
	// 终端进程独立于扩展组件：最后统一回收。
	a.closeTerminals()

	if store != nil {
		flushCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		_ = store.Flush(flushCtx)
		cancel()
		_ = store.Close()
	}
}

// shutdown 取消轮次、关闭审批通道并释放引擎。
func (rt *sessionRuntime) shutdown() {
	rt.mu.Lock()
	rt.closed = true
	cancel := rt.turnCancel
	engine := rt.engine
	rt.engine = nil
	rt.turnCancel = nil
	rt.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	rt.approvals.CancelSession(rt.id)
	if engine != nil {
		_ = engine.Close()
	}
}

// CreateSession 创建会话并写入 session_meta 首行，同时登记内存运行时。
// SessionID 为空时自动生成；WorkDir 为空时使用应用默认工作目录。
func (a *App) CreateSession(in CreateSessionInput) (SessionInfoDTO, error) {
	store, cfg, err := a.sessionStore()
	if err != nil {
		return SessionInfoDTO{}, err
	}
	if a.isClosed() {
		return SessionInfoDTO{}, ErrClosed
	}

	id := strings.TrimSpace(in.SessionID)
	if id == "" {
		id = newID("s")
	}
	if err := session.ValidateID(id); err != nil {
		return SessionInfoDTO{}, fmt.Errorf("app: create session: %w", err)
	}

	workDir := strings.TrimSpace(in.WorkDir)
	if workDir == "" {
		workDir = cfg.WorkDir
	}
	workDir, err = resolveWorkDir(workDir)
	if err != nil {
		return SessionInfoDTO{}, fmt.Errorf("app: create session: %w", err)
	}

	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = "新会话"
	}
	meta := session.Meta{
		ID:        id,
		Title:     title,
		WorkDir:   workDir,
		Model:     cfg.Model,
		CreatedAt: time.Now(),
	}
	if err := store.CreateSession(a.lifeCtx(), meta); err != nil {
		return SessionInfoDTO{}, fmt.Errorf("app: create session %q: %w", id, err)
	}

	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return SessionInfoDTO{}, ErrClosed
	}
	a.sessions[id] = newSessionRuntime(meta)
	a.mu.Unlock()

	info := toSessionInfoDTO(session.Info{
		ID:        id,
		Title:     title,
		WorkDir:   workDir,
		Messages:  0,
		UpdatedAt: meta.CreatedAt,
	}, false)
	if projects, perr := a.projectPaths(); perr == nil {
		info.ProjectPath = projectPathForWorkDir(workDir, projects)
	} else {
		fmt.Fprintf(os.Stderr, "app: resolve project for session %s: %v\n", id, perr)
	}
	return info, nil
}

// OpenProject 弹出系统目录选择框，并把选中的文件夹登记为项目。
// 用户取消时返回 ErrProjectDialogCanceled，前端据此静默忽略。
func (a *App) OpenProject() (ProjectDTO, error) {
	a.mu.Lock()
	picker := a.pickDir
	closed := a.closed
	a.mu.Unlock()
	if closed {
		return ProjectDTO{}, ErrClosed
	}
	if picker == nil {
		return ProjectDTO{}, errors.New("app: directory picker unavailable")
	}

	dir, err := picker(a.lifeCtx())
	if err != nil {
		return ProjectDTO{}, fmt.Errorf("app: open project: %w", err)
	}
	if strings.TrimSpace(dir) == "" {
		return ProjectDTO{}, ErrProjectDialogCanceled
	}
	return a.AddProject(dir)
}

// AddProject 登记一个已经确定的项目目录（不弹对话框）。
// 目录必须存在；重复登记返回已有条目。
func (a *App) AddProject(dir string) (ProjectDTO, error) {
	store, err := a.projectStore()
	if err != nil {
		return ProjectDTO{}, err
	}
	entry, err := store.Add(a.lifeCtx(), dir)
	if err != nil {
		return ProjectDTO{}, fmt.Errorf("app: add project: %w", err)
	}
	return toProjectDTO(entry), nil
}

// ListProjects 返回已打开的项目，最近打开的在前。
func (a *App) ListProjects() ([]ProjectDTO, error) {
	store, err := a.projectStore()
	if err != nil {
		return nil, err
	}
	items, err := store.List(a.lifeCtx())
	if err != nil {
		return nil, fmt.Errorf("app: list projects: %w", err)
	}
	out := make([]ProjectDTO, 0, len(items))
	for _, item := range items {
		out = append(out, toProjectDTO(item))
	}
	return out, nil
}

// ListSessions 返回全部会话摘要，按最近更新时间倒序。
func (a *App) ListSessions() ([]SessionInfoDTO, error) {
	store, _, err := a.sessionStore()
	if err != nil {
		return nil, err
	}
	infos, err := store.List(a.lifeCtx())
	if err != nil {
		return nil, fmt.Errorf("app: list sessions: %w", err)
	}

	runtimes := a.runtimeSnapshot()
	// 旧版本创建的会话没有标题记录：首次出现在列表时用首条用户消息补一个，
	// 之后读取到的就是已落盘的标题。正在生成标题的会话跳过，避免覆盖模型结果。
	for i := range infos {
		if !isDefaultSessionTitle(infos[i].Title) {
			continue
		}
		if rt := runtimes[infos[i].ID]; rt != nil && rt.titleScheduled() {
			continue
		}
		if title, ok := a.deriveSessionTitle(store, infos[i].ID); ok {
			infos[i].Title = title
		}
	}

	projects, err := a.projectPaths()
	if err != nil {
		return nil, fmt.Errorf("app: list sessions: %w", err)
	}

	out := make([]SessionInfoDTO, 0, len(infos))
	for _, info := range infos {
		running := false
		if rt := runtimes[info.ID]; rt != nil {
			running = rt.isRunning()
		}
		item := toSessionInfoDTO(info, running)
		item.ProjectPath = projectPathForWorkDir(info.WorkDir, projects)
		out = append(out, item)
	}
	return out, nil
}

// LoadSession 读取会话 transcript 并重放成前端视图。
// 只读，不创建运行时；首次 SendMessage 时会按需恢复。
func (a *App) LoadSession(sessionID string) (SessionViewDTO, error) {
	store, cfg, err := a.sessionStore()
	if err != nil {
		return SessionViewDTO{}, err
	}
	if err := session.ValidateID(sessionID); err != nil {
		return SessionViewDTO{}, fmt.Errorf("app: load session: %w", err)
	}

	recs, err := store.Load(a.lifeCtx(), sessionID)
	if err != nil && !errors.Is(err, session.ErrCorruptLines) {
		return SessionViewDTO{}, fmt.Errorf("app: load session %q: %w", sessionID, err)
	}
	// ErrCorruptLines 时 recs 仍是损坏行之前/之后的可用记录，继续重放。
	loaded, err := session.Replay(recs)
	if err != nil {
		return SessionViewDTO{}, fmt.Errorf("app: load session %q: %w", sessionID, err)
	}

	model := cfg.Model
	if strings.TrimSpace(model) == "" {
		model = loaded.Meta.Model
	}
	mode := cfg.PermissionMode
	if loaded.Mode != "" {
		mode = loaded.Mode
	}
	running := false
	a.mu.Lock()
	rt := a.sessions[sessionID]
	a.mu.Unlock()
	if rt != nil {
		running = rt.isRunning()
		if m := rt.permissionMode(); m != "" {
			mode = string(m)
		}
	}
	return toSessionViewDTO(loaded, model, mode, running), nil
}

// SendMessage 启动一轮用户输入并立即返回 turnID；输出通过 "agent:event" 推送。
// images 是随消息提交的图片附件，可以为 nil；文本与图片不允许同时为空。
// 同一会话同时只允许一轮：已有轮次运行时返回 ErrTurnRunning（不排队，不轮询）。
func (a *App) SendMessage(sessionID, prompt string, images []ImageInput) (SendMessageResult, error) {
	if err := session.ValidateID(sessionID); err != nil {
		return SendMessageResult{}, fmt.Errorf("app: send message: %w", err)
	}
	// 模型能力门控先行：不支持图像的模型直接拒绝，错误只回给前端，
	// 不把必然失败的请求转成错误工具结果打断对话。
	if len(images) > 0 {
		cfgNow, _ := a.configSnapshot()
		if !llm.ModelInfoOf(cfgNow.Model).SupportsImages {
			return SendMessageResult{}, fmt.Errorf("%w: %s", ErrModelNoImageSupport, cfgNow.Model)
		}
	}
	promptBlocks, err := a.validateMessageInput(prompt, images)
	if err != nil {
		return SendMessageResult{}, err
	}
	if len(prompt) > maxPromptBytes {
		return SendMessageResult{}, ErrPromptTooLong
	}
	if a.isClosed() {
		return SendMessageResult{}, ErrClosed
	}

	rt, err := a.runtimeForSend(sessionID)
	if err != nil {
		return SendMessageResult{}, err
	}
	cfg, gen := a.configSnapshot()
	if strings.TrimSpace(cfg.Model) == "" {
		return SendMessageResult{}, ErrModelNotConfigured
	}

	// @ 引用与 / 命令在这里展开成附件块：用户原文保留，内容追加在其后，
	// 与参考实现的 attachment 消息组装一致。展开失败不阻断发送。
	workDir := rt.workDirSnapshot()
	attachments := a.expandAtMentions(workDir, prompt)
	if commandBlocks := a.expandSlashCommand(workDir, prompt); len(commandBlocks) > 0 {
		attachments = append(commandBlocks, attachments...)
	}

	rt.mu.Lock()
	if rt.closed {
		rt.mu.Unlock()
		return SendMessageResult{}, ErrSessionClosed
	}
	if rt.running {
		rt.mu.Unlock()
		return SendMessageResult{}, ErrTurnRunning
	}
	// 首次发送消息时异步生成会话标题；它不参与本轮，也不阻塞返回。
	a.startSessionTitle(rt, prompt, cfg)
	engine, err := a.ensureEngineLocked(rt, cfg, gen)
	if err != nil {
		rt.mu.Unlock()
		return SendMessageResult{}, err
	}

	base := a.lifeCtx()
	turnCtx, cancel := context.WithCancel(base)
	turnID := newID("turn")
	rt.running = true
	rt.turnID = turnID
	rt.turnCancel = cancel

	// WaitGroup.Add 必须与 Shutdown 的 closed 置位互斥：否则 Add 可能发生在
	// Wait 已开始之后（计数为 0 时 Add 与 Wait 并发属于未定义行为）。
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		rt.running = false
		rt.turnID = ""
		rt.turnCancel = nil
		rt.mu.Unlock()
		cancel()
		return SendMessageResult{}, ErrClosed
	}
	a.turns.Add(1)
	a.mu.Unlock()
	rt.mu.Unlock()

	go func() {
		defer a.turns.Done()
		defer cancel()
		defer func() {
			rt.mu.Lock()
			rt.running = false
			rt.turnID = ""
			rt.turnCancel = nil
			rt.mu.Unlock()
		}()
		// Run 只在内部故障时返回 error；模型/工具错误、取消与 panic 都已转成事件。
		// 后台任务完成通知若在轮次收尾后才到达，会留在 rt.notifs 里：
		// 这里取走并以它们自动续跑（cc 的 target-continuation 语义），
		// 直到没有遗留通知或本轮被用户取消。
		// 图片与附件块只随首次提交进入 user 消息；通知续跑轮次不携带。
		prompt := prompt
		promptBlocks := promptBlocks
		attachments := attachments
		for {
			_, err := engine.Run(turnCtx, agent.Input{TurnID: turnID, Prompt: prompt, Attachments: attachments, Images: promptBlocks}, func(ev agent.Event) {
				a.emitter.Emit(base, ev)
			})
			attachments = nil
			promptBlocks = nil
			if err != nil || turnCtx.Err() != nil {
				return
			}
			pending := rt.notifs.Drain()
			if len(pending) == 0 {
				return
			}
			prompt = strings.Join(pending, "\n\n")
			turnID = newID("turn")
		}
	}()

	return SendMessageResult{SessionID: sessionID, TurnID: turnID, Running: true}, nil
}

// validateMessageInput 校验文本与图片附件，返回可直接放进 agent.Input.Images
// 的图片块。文本与图片不允许同时为空；每张图片按 magic bytes 判定格式并
// 重新归一化（media type 不可信），数量与单张体积受 llm 包约束。
func (a *App) validateMessageInput(prompt string, images []ImageInput) ([]llm.ContentBlock, error) {
	if strings.TrimSpace(prompt) == "" && len(images) == 0 {
		return nil, ErrEmptyPrompt
	}
	if len(images) == 0 {
		return nil, nil
	}
	if len(images) > llm.MaxImagesPerMessage {
		return nil, fmt.Errorf("%w: %d images, max %d", ErrTooManyImages, len(images), llm.MaxImagesPerMessage)
	}
	blocks := make([]llm.ContentBlock, 0, len(images))
	for i, img := range images {
		mediaType, data, err := llm.NormalizeImageInput(img.MediaType, img.Data)
		if err != nil {
			return nil, fmt.Errorf("%w: image %d: %v", ErrInvalidImage, i+1, err)
		}
		blocks = append(blocks, llm.ImageBlock(mediaType, data))
	}
	return blocks, nil
}

// CancelTurn 取消指定会话进行中的轮次。没有进行中的轮次时是空操作。
// 用户主动取消同时清空通知队列：后台任务的遗留通知不再自动续跑。
func (a *App) CancelTurn(sessionID string) error {
	if err := session.ValidateID(sessionID); err != nil {
		return fmt.Errorf("app: cancel turn: %w", err)
	}
	rt, err := a.runtimeOf(sessionID)
	if err != nil {
		return err
	}

	rt.mu.Lock()
	cancel := rt.turnCancel
	engine := rt.engine
	rt.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if engine != nil {
		engine.Cancel()
	}
	rt.notifs.Drain()
	return nil
}

// ApproveTool 回应一次工具审批请求。
// option 必须是 allow_once / allow_always / deny_once / deny_always 之一；
// requestID 必须属于该会话且尚未应答，否则返回 ErrApprovalNotFound。
// updatedInput 是前端改写后的工具输入 JSON（交互类工具回填用户应答），
// 仅对 allow 类选项生效；空字符串表示保持原输入。
func (a *App) ApproveTool(sessionID, requestID, option, updatedInput string) error {
	if err := session.ValidateID(sessionID); err != nil {
		return fmt.Errorf("app: approve tool: %w", err)
	}
	if strings.TrimSpace(requestID) == "" {
		return fmt.Errorf("app: approve tool: %w", ErrApprovalNotFound)
	}
	if !validApprovalOption(option) {
		return fmt.Errorf("app: approve tool: %w", ErrInvalidOption)
	}
	resp := permission.Response{Option: option}
	if trimmed := strings.TrimSpace(updatedInput); trimmed != "" {
		if !json.Valid([]byte(trimmed)) {
			return fmt.Errorf("app: approve tool: %w", ErrInvalidOption)
		}
		resp.UpdatedInput = json.RawMessage(trimmed)
	}

	rt, err := a.runtimeOf(sessionID)
	if err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			return fmt.Errorf("app: approve tool: %w", ErrApprovalNotFound)
		}
		return err
	}
	if !approvalPending(rt.approvals, sessionID, requestID) {
		return ErrApprovalNotFound
	}
	if !rt.approvals.Decide(requestID, resp) {
		return ErrApprovalNotFound
	}
	return nil
}

// SetPermissionMode 切换一个会话的权限模式：立即对进行中的轮次生效，
// 并写入 transcript 的 mode 记录，恢复会话时沿用。
// 模式是会话级的，不修改全局配置里新会话的默认值。
func (a *App) SetPermissionMode(sessionID, mode string) error {
	if err := session.ValidateID(sessionID); err != nil {
		return fmt.Errorf("app: set permission mode: %w", err)
	}
	parsed, err := permission.ParseMode(strings.TrimSpace(mode))
	if err != nil {
		return fmt.Errorf("app: set permission mode: %w", err)
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
	rt.mode = parsed
	engine := rt.engine
	rt.mu.Unlock()

	if engine != nil {
		if err := engine.SetPermissionMode(parsed); err != nil {
			return fmt.Errorf("app: set permission mode: %w", err)
		}
	}

	store, _, err := a.sessionStore()
	if err != nil {
		return err
	}
	rec := session.Record{
		Type:      session.RecordMode,
		SessionID: sessionID,
		Time:      time.Now(),
		Meta:      map[string]string{"mode": string(parsed)},
	}
	if err := store.Append(a.lifeCtx(), rec); err != nil {
		return fmt.Errorf("app: persist permission mode: %w", err)
	}
	return nil
}

// GetProviderConfig 返回 Provider 配置的前端视图。响应中不含 API Key 明文，
// 只返回是否已配置与末四位掩码。
func (a *App) GetProviderConfig() ProviderConfigDTO {
	a.mu.Lock()
	cfg := a.cfg
	initErr := a.initErr
	a.mu.Unlock()
	return toProviderConfigDTO(cfg, initErr)
}

// SetProviderConfig 更新 Provider 配置。改动在下一轮 SendMessage 时生效：
// 绑定层递增 generation，并在构建引擎前重建 Provider（会话历史不受影响）。
//
// APIKey 为空表示保留现有密钥（前端拿不到明文，不能回传）；其余字段按整表替换。
func (a *App) SetProviderConfig(in ProviderConfigInput) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return ErrClosed
	}
	next, err := in.apply(a.cfg)
	if err != nil {
		return err
	}
	if err := saveConfigFile(next.ConfigPath, next); err != nil {
		return err
	}
	a.cfg = next
	a.gen++
	return nil
}

// Greet 是脚手架示例方法，保留以避免既有前端调用失效。
func (a *App) Greet(name string) string {
	return fmt.Sprintf("Hello %s, B Desktop is running.", name)
}

// sessionStore 返回存储与配置快照；存储未就绪或应用已关闭时返回错误。
func (a *App) sessionStore() (session.Store, Config, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, Config{}, ErrClosed
	}
	if a.store == nil {
		if a.initErr != nil {
			return nil, a.cfg, fmt.Errorf("%w: %v", ErrStoreUnavailable, a.initErr)
		}
		return nil, a.cfg, ErrStoreUnavailable
	}
	return a.store, a.cfg, nil
}

// projectStore 返回项目列表存储；首次调用时按数据目录惰性创建。
func (a *App) projectStore() (*project.Store, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, ErrClosed
	}
	if a.projects == nil {
		if strings.TrimSpace(a.cfg.DataDir) == "" {
			return nil, ErrStoreUnavailable
		}
		a.projects = project.NewStore(filepath.Join(a.cfg.DataDir, "projects.json"))
	}
	return a.projects, nil
}

// isClosed 报告应用是否已关闭。
func (a *App) isClosed() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.closed
}

// isStarted 报告 Wails 生命周期是否已启动；runtime.* 调用前必须检查。
func (a *App) isStarted() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.started
}

// configSnapshot 返回配置与 generation 的快照。
func (a *App) configSnapshot() (Config, int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg, a.gen
}

// lifeCtx 返回 Wails 生命周期 context；未启动时退化为 Background。
func (a *App) lifeCtx() context.Context {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}

// runtimeSnapshot 返回会话运行时的浅拷贝（id → runtime），调用方不得持有 a.mu。
func (a *App) runtimeSnapshot() map[string]*sessionRuntime {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[string]*sessionRuntime, len(a.sessions))
	for id, rt := range a.sessions {
		out[id] = rt
	}
	return out
}

// runtimeOf 按 ID 查找已有运行时。
func (a *App) runtimeOf(sessionID string) (*sessionRuntime, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, ErrClosed
	}
	rt := a.sessions[sessionID]
	if rt == nil {
		return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, sessionID)
	}
	return rt, nil
}

// runtimeForSend 返回运行时；应用重启后内存中没有运行时则从 transcript 恢复元信息。
func (a *App) runtimeForSend(sessionID string) (*sessionRuntime, error) {
	if rt, err := a.runtimeOf(sessionID); err == nil {
		return rt, nil
	} else if !errors.Is(err, ErrSessionNotFound) {
		return nil, err
	}

	store, _, err := a.sessionStore()
	if err != nil {
		return nil, err
	}
	recs, err := store.Load(a.lifeCtx(), sessionID)
	if err != nil {
		switch {
		case errors.Is(err, session.ErrSessionNotFound):
			return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, sessionID)
		case errors.Is(err, session.ErrCorruptLines):
			// 损坏行已被 Store 跳过，继续使用可用记录恢复运行时。
		default:
			return nil, fmt.Errorf("app: load session %q: %w", sessionID, err)
		}
	}
	loaded, err := session.Replay(recs)
	if err != nil {
		return nil, fmt.Errorf("app: load session %q: %w", sessionID, err)
	}

	rt := newSessionRuntime(loaded.Meta)
	if mode, err := permission.ParseMode(loaded.Mode); err == nil {
		rt.mode = mode
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, ErrClosed
	}
	if existing := a.sessions[sessionID]; existing != nil {
		return existing, nil
	}
	a.sessions[sessionID] = rt
	return rt, nil
}

// ensureEngineLocked 返回可用的引擎；配置 generation 变化时重建。
// 调用方必须持有 rt.mu，且此时没有进行中的轮次。
func (a *App) ensureEngineLocked(rt *sessionRuntime, cfg Config, gen int64) (*agent.Engine, error) {
	if rt.engine != nil && rt.engineGen == gen {
		return rt.engine, nil
	}
	if rt.engine != nil {
		_ = rt.engine.Close()
		rt.engine = nil
	}
	engine, err := a.buildEngine(rt, cfg)
	if err != nil {
		return nil, err
	}
	rt.engine = engine
	rt.engineGen = gen
	return engine, nil
}

// buildEngine 按会话元信息与当前配置装配引擎，并从 transcript 恢复历史。
func (a *App) buildEngine(rt *sessionRuntime, cfg Config) (*agent.Engine, error) {
	store, _, err := a.sessionStore()
	if err != nil {
		return nil, err
	}
	provider, err := a.factory(cfg)
	if err != nil {
		return nil, fmt.Errorf("app: provider: %w", err)
	}

	workDir := rt.workDir
	if strings.TrimSpace(workDir) == "" {
		workDir = cfg.WorkDir
	}
	mode, err := permission.ParseMode(cfg.PermissionMode)
	if err != nil {
		return nil, fmt.Errorf("app: permission mode: %w", err)
	}
	// 会话级选择优先于全局配置；调用方持有 rt.mu。
	if rt.mode != "" {
		mode = rt.mode
	}
	reg, err := a.taskRegistryComponent()
	if err != nil {
		reportExtensionError("tasks", err)
	}
	components, err := agent.Wire(agent.WireOptions{
		WorkDir:         workDir,
		Model:           cfg.Model,
		Provider:        provider,
		PermissionMode:  mode,
		ContextWindow:   cfg.ContextWindow,
		MaxOutputTokens: cfg.MaxOutputTokens,
		SessionID:       rt.id,
		TaskRegistry:    reg,
	})
	if err != nil {
		return nil, fmt.Errorf("app: wire session %q: %w", rt.id, err)
	}

	extensions, err := a.buildExtensions(workDir)
	if err != nil {
		return nil, fmt.Errorf("app: extensions session %q: %w", rt.id, err)
	}

	engine, err := agent.NewEngine(agent.Config{
		SessionID:          rt.id,
		WorkDir:            workDir,
		Provider:           provider,
		Model:              cfg.Model,
		MaxTurns:           cfg.MaxTurns,
		MaxToolConcurrency: cfg.MaxToolConcurrency,
		ThinkingEnabled:    cfg.ThinkingEnabled,
		ThinkingBudget:     cfg.ThinkingBudget,
		Tools:              components.Tools,
		Permissions:        components.Permissions,
		Approvals:          rt.approvals,
		Builder:            components.Builder,
		Compactor:          components.Compactor,
		Store:              store,
		Notifications:      rt.notifs,
		Extensions:         extensions,
	})
	if err != nil {
		return nil, fmt.Errorf("app: engine session %q: %w", rt.id, err)
	}
	if err := engine.Restore(a.lifeCtx()); err != nil {
		_ = engine.Close()
		return nil, fmt.Errorf("app: restore session %q: %w", rt.id, err)
	}
	return engine, nil
}

// approvalPending 报告 requestID 是否仍属于该会话的待审批项。
func approvalPending(approvals *agent.Approvals, sessionID, requestID string) bool {
	for _, view := range approvals.Pending(sessionID) {
		if view.RequestID == requestID {
			return true
		}
	}
	return false
}

// validApprovalOption 校验四个标准审批选项。
func validApprovalOption(option string) bool {
	switch option {
	case permission.OptionAllowOnce, permission.OptionAllowAlways,
		permission.OptionDenyOnce, permission.OptionDenyAlways:
		return true
	}
	return false
}

// resolveWorkDir 把工作目录解析为绝对真实路径（解析符号链接），
// 与 builtin.Resolve 解析出的路径处于同一基准，保证权限规则比较一致。
func resolveWorkDir(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", errors.New("app: empty work dir")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("app: work dir %q: %w", dir, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("app: work dir %q: %w", dir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("app: work dir %q is not a directory", dir)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real, nil
	}
	return abs, nil
}

// newID 生成 "prefix_" + 12 随机字节的十六进制；随机源失败时退回时间戳。
func newID(prefix string) string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

// apply 把输入整表合并到现有配置。APIKey 为空表示保留现有密钥。
func (in ProviderConfigInput) apply(current Config) (Config, error) {
	next := current

	provider := strings.ToLower(strings.TrimSpace(in.Provider))
	if provider == "" {
		return current, fmt.Errorf("%w: provider is required", ErrInvalidProviderConfig)
	}
	switch provider {
	case "anthropic", "openai":
	default:
		return current, fmt.Errorf("%w: unknown provider %q", ErrInvalidProviderConfig, provider)
	}
	next.Provider = provider

	baseURL := strings.TrimSpace(in.BaseURL)
	if baseURL != "" {
		parsed, err := url.Parse(baseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return current, fmt.Errorf("%w: invalid base url %q", ErrInvalidProviderConfig, baseURL)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return current, fmt.Errorf("%w: base url scheme must be http or https", ErrInvalidProviderConfig)
		}
	}
	next.BaseURL = baseURL

	next.Model = strings.TrimSpace(in.Model)

	if key := strings.TrimSpace(in.APIKey); key != "" {
		next.APIKey = key
	}

	if in.ContextWindow < 0 {
		return current, fmt.Errorf("%w: context window must be >= 0", ErrInvalidProviderConfig)
	}
	if in.ContextWindow == 0 {
		next.ContextWindow = defaultContextWindow
	} else {
		next.ContextWindow = in.ContextWindow
	}

	if in.MaxOutputTokens < 0 {
		return current, fmt.Errorf("%w: max output tokens must be >= 0", ErrInvalidProviderConfig)
	}
	if in.MaxOutputTokens == 0 {
		next.MaxOutputTokens = defaultMaxOutputTokens
	} else {
		next.MaxOutputTokens = in.MaxOutputTokens
	}

	if in.ThinkingBudget < 0 {
		return current, fmt.Errorf("%w: thinking budget must be >= 0", ErrInvalidProviderConfig)
	}
	next.ThinkingEnabled = in.ThinkingEnabled
	if in.ThinkingBudget == 0 {
		next.ThinkingBudget = llm.DefaultThinkingBudget
	} else {
		next.ThinkingBudget = in.ThinkingBudget
	}
	// budget_tokens 必须严格小于 max_tokens；无法满足时关闭扩展思考并保留预算值，
	// 用户调大 max_tokens 后重新打开即可。
	if next.ThinkingEnabled && next.ThinkingBudget >= next.MaxOutputTokens {
		next.ThinkingEnabled = false
	}
	return next, nil
}
