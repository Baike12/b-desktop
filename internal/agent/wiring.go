package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	sysctx "anc-dsk/internal/context"
	"anc-dsk/internal/llm"
	"anc-dsk/internal/permission"
	"anc-dsk/internal/tool"
	"anc-dsk/internal/tool/builtin"
)

// Registry 保存本次会话可用的工具集合，读写安全。
// internal/tool 目前只冻结了 Tool 契约，注册表按 §2.2 的契约在 agent 内实现。
type Registry struct {
	mu     sync.RWMutex
	byName map[string]tool.Tool
	order  []string
}

// NewRegistry 创建注册表；重复工具名返回错误。
func NewRegistry(tools ...tool.Tool) (*Registry, error) {
	r := &Registry{byName: make(map[string]tool.Tool, len(tools))}
	for _, t := range tools {
		if err := r.Register(t); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Register 注册一个工具；重名返回错误。
func (r *Registry) Register(t tool.Tool) error {
	if t == nil {
		return fmt.Errorf("agent: register nil tool")
	}
	name := t.Name()
	if name == "" {
		return fmt.Errorf("agent: register tool with empty name")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byName[name]; exists {
		return fmt.Errorf("agent: duplicate tool %q", name)
	}
	r.byName[name] = t
	r.order = append(r.order, name)
	return nil
}

// Lookup 按名查找工具。
func (r *Registry) Lookup(name string) (tool.Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.byName[name]
	return t, ok
}

// Specs 按注册顺序返回全部规格。
func (r *Registry) Specs() []tool.Spec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]tool.Spec, 0, len(r.order))
	for _, name := range r.order {
		t := r.byName[name]
		out = append(out, tool.Spec{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		})
	}
	return out
}

// Names 按注册顺序返回工具名。
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// DefaultTools 按内置工具清单构造注册表：
// Read、Write、Edit、Glob、Grep、Bash、TodoWrite。
// root 是会话工作目录，所有路径类工具都被限制在该目录内。
func DefaultTools(root string) (*Registry, error) {
	registry, err := NewRegistry(
		builtin.NewRead(root),
		builtin.NewWrite(root),
		builtin.NewEdit(root),
		builtin.NewGlob(root),
		builtin.NewGrep(root),
		builtin.NewBash(root),
		builtin.NewTodoWrite(),
	)
	if err != nil {
		return nil, fmt.Errorf("agent: default tools: %w", err)
	}
	return registry, nil
}

// DefaultPermissions 创建权限引擎；mode 为空时使用 default 模式。
func DefaultPermissions(workDir string, mode permission.Mode) (*permission.Engine, error) {
	if mode == "" {
		mode = permission.ModeDefault
	}
	engine, err := permission.NewEngine(permission.Config{Mode: mode, WorkDir: workDir})
	if err != nil {
		return nil, fmt.Errorf("agent: permissions: %w", err)
	}
	return engine, nil
}

// DefaultBuilder 创建系统提示组装器（上下文组装器）。
func DefaultBuilder(opts sysctx.Options) *sysctx.Builder {
	return sysctx.NewBuilder(opts)
}

// DefaultCompactor 用给定 provider 与预算创建压缩器。
// 预算由调用方构造一次并同时用于请求 max_tokens 与阻塞线，避免阈值漂移。
func DefaultCompactor(provider llm.Provider, model string, budget sysctx.Budget, summarizer sysctx.Summarizer, now func() time.Time) (*sysctx.Compactor, error) {
	compactor, err := sysctx.NewCompactor(sysctx.CompactorConfig{
		Provider:   provider,
		Model:      model,
		Summarizer: summarizer,
		Budget:     sysctx.NewBudget(budget),
		Now:        now,
	})
	if err != nil {
		return nil, fmt.Errorf("agent: compactor: %w", err)
	}
	return compactor, nil
}

// Components 是一次默认装配的结果。
type Components struct {
	Tools       *Registry
	Permissions *permission.Engine
	Builder     *sysctx.Builder
	Compactor   *sysctx.Compactor
}

// WireOptions 是默认装配的输入。
type WireOptions struct {
	WorkDir         string
	Model           string
	Provider        llm.Provider
	PermissionMode  permission.Mode
	ContextWindow   int
	MaxOutputTokens int
	SystemExtra     string
	Summarizer      sysctx.Summarizer
	Now             func() time.Time
	// ReadFile 与 RunGit 是测试注入点；nil 时使用默认实现。
	ReadFile func(path string) ([]byte, error)
	RunGit   func(ctx context.Context, dir string, args ...string) ([]byte, error)
}

// Wire 装配默认工具注册表、权限引擎、上下文组装器与压缩器。
func Wire(opts WireOptions) (*Components, error) {
	if opts.Provider == nil {
		return nil, fmt.Errorf("agent: wire: %w: missing Provider", ErrInvalidConfig)
	}
	tools, err := DefaultTools(opts.WorkDir)
	if err != nil {
		return nil, err
	}
	permissions, err := DefaultPermissions(opts.WorkDir, opts.PermissionMode)
	if err != nil {
		return nil, err
	}
	builder := DefaultBuilder(sysctx.Options{
		WorkDir:         opts.WorkDir,
		Model:           opts.Model,
		ContextWindow:   opts.ContextWindow,
		MaxOutputTokens: opts.MaxOutputTokens,
		SystemExtra:     opts.SystemExtra,
		Now:             opts.Now,
		ReadFile:        opts.ReadFile,
		RunGit:          opts.RunGit,
	})
	compactor, err := DefaultCompactor(opts.Provider, opts.Model, sysctx.Budget{
		ContextWindow:   opts.ContextWindow,
		MaxOutputTokens: opts.MaxOutputTokens,
	}, opts.Summarizer, opts.Now)
	if err != nil {
		return nil, err
	}
	return &Components{Tools: tools, Permissions: permissions, Builder: builder, Compactor: compactor}, nil
}
