package acp

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Session 是一个已建立的 Agent 会话。实现必须保证：Prompt 串行（同时最多
// 一轮）；Cancel 可从任意 goroutine 调用；Close 释放子进程等资源，幂等。
type Session interface {
	// Prompt 提交一轮用户输入并阻塞到本轮结束。ctx 取消等价于取消本轮。
	Prompt(ctx context.Context, blocks []ContentBlock) (PromptResult, error)
	// Cancel 取消进行中的一轮提示；没有进行中的轮次时是空操作。
	Cancel()
	// SetMode 切换会话的权限模式。
	SetMode(ctx context.Context, modeID string) error
	// Modes 返回会话当前的模式状态。
	Modes() SessionModes
	// Close 释放会话资源，幂等。
	Close() error
}

// Client 是 Agent 会话反向调用宿主能力的接口，对应 ACP 的 agent→client 请求。
// 实现必须并发安全：多个请求可能同时到达。
type Client interface {
	// RequestPermission 请求用户审批一次工具调用，阻塞直到用户答复或 ctx 取消。
	// 取消时返回 Cancelled=true 而不是错误。
	RequestPermission(ctx context.Context, req PermissionRequest) (PermissionOutcome, error)
	// ReadTextFile 读取工作区内一个 UTF-8 文本文件。
	ReadTextFile(ctx context.Context, path string) (string, error)
	// WriteTextFile 写入工作区内一个 UTF-8 文本文件。
	WriteTextFile(ctx context.Context, path, content string) error
}

// Backend 是一个 Agent 后端：负责创建会话。
// NewSession 返回的会话把更新推给 sink（从后端 goroutine 调用，实现必须快速
// 返回），反向请求走 client。backend 必须并发安全。
type Backend interface {
	Info() AgentInfo
	NewSession(ctx context.Context, params SessionParams, sink UpdateSink, client Client) (Session, error)
}

// UpdateSink 接收一次会话更新。
type UpdateSink func(SessionUpdate)

// Factory 创建一个后端实例。factory 必须幂等且廉价：注册表会反复调用它探测
// 可用性（例如探测 CLI 是否安装）。
type Factory func(ctx context.Context) (Backend, error)

// Registry 是 Agent 后端注册表。本地 benben agent 不进注册表：它由 app 直接
// 驱动；注册表只收 ACP 风格的外部后端。
type Registry struct {
	mu        sync.Mutex
	next      int
	factories map[string]registryEntry
}

type registryEntry struct {
	order   int
	info    AgentInfo
	factory Factory
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{factories: make(map[string]registryEntry)}
}

// Register 登记一个后端工厂；同名重复注册覆盖旧条目。
func (r *Registry) Register(info AgentInfo, factory Factory) {
	if info.ID == "" || factory == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	r.factories[info.ID] = registryEntry{order: r.next, info: info, factory: factory}
}

// List 返回全部后端及其可用性，按注册顺序排列。探测失败的后端
// Available=false，Detail 携带原因。
func (r *Registry) List(ctx context.Context) []AgentAvailability {
	r.mu.Lock()
	entries := make([]registryEntry, 0, len(r.factories))
	for _, entry := range r.factories {
		entries = append(entries, entry)
	}
	r.mu.Unlock()
	sort.Slice(entries, func(i, j int) bool { return entries[i].order < entries[j].order })

	out := make([]AgentAvailability, 0, len(entries))
	for _, entry := range entries {
		avail := AgentAvailability{AgentInfo: entry.info, Available: true}
		backend, err := entry.factory(ctx)
		if err != nil {
			avail.Available = false
			avail.Detail = err.Error()
		} else if backend == nil {
			avail.Available = false
			avail.Detail = "backend factory returned nil"
		}
		out = append(out, avail)
	}
	return out
}

// Backend 返回指定 ID 的后端实例。
func (r *Registry) Backend(ctx context.Context, id string) (Backend, error) {
	r.mu.Lock()
	entry, ok := r.factories[id]
	r.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("acp: unknown agent backend %q", id)
	}
	backend, err := entry.factory(ctx)
	if err != nil {
		return nil, fmt.Errorf("acp: create backend %s: %w", id, err)
	}
	if backend == nil {
		return nil, fmt.Errorf("acp: backend %s factory returned nil", id)
	}
	return backend, nil
}
