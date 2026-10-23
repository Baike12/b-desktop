package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	sysctx "anc-dsk/internal/context"
	"anc-dsk/internal/llm"
	"anc-dsk/internal/permission"
	"anc-dsk/internal/session"
	"anc-dsk/internal/tool"
)

// 运行时常量与默认值。
const (
	defaultMaxToolConcurrency = 10
	// persistTimeout 是 transcript 写入的独立超时。写入脱离轮次取消
	// （context.WithoutCancel），用户取消后仍必须落盘 tool_result。
	persistTimeout = 5 * time.Second
)

// 运行时的错误哨兵。
var (
	// ErrTurnRunning 表示同一 Engine 已有运行中的轮次。
	ErrTurnRunning = errors.New("agent: turn already running")
	// ErrInvalidConfig 表示 Config 缺少必填项或数值非法。
	ErrInvalidConfig = errors.New("agent: invalid config")
)

// Config 是 Engine 的构造参数。预算的唯一定义在 Compactor.Budget()：
// 请求的 max_tokens 与阻塞线都从它取，Engine 不保存第二份 MaxOutputTokens。
type Config struct {
	SessionID          string
	WorkDir            string
	Provider           llm.Provider
	Model              string
	MaxTurns           int // 0 表示不限
	MaxToolConcurrency int // 0 时取 10
	// SystemExtra 由注入的 Builder 通过 sysctx.Options.SystemExtra 持有，
	// Engine 不重复拼接，保留该字段仅为配置对照。
	SystemExtra string
	Tools       *Registry
	Permissions *permission.Engine
	Approvals   *Approvals
	Builder     *sysctx.Builder
	Compactor   *sysctx.Compactor
	Store       session.Store
	Now         func() time.Time
	NewID       func(prefix string) string
}

// Validate 校验必填项与数值范围。可选字段的默认值由 NewEngine 填充。
func (c Config) Validate() error {
	var missing []string
	if strings.TrimSpace(c.SessionID) == "" {
		missing = append(missing, "SessionID")
	}
	if c.Provider == nil {
		missing = append(missing, "Provider")
	}
	if c.Tools == nil {
		missing = append(missing, "Tools")
	}
	if c.Permissions == nil {
		missing = append(missing, "Permissions")
	}
	if c.Approvals == nil {
		missing = append(missing, "Approvals")
	}
	if c.Builder == nil {
		missing = append(missing, "Builder")
	}
	if c.Compactor == nil {
		missing = append(missing, "Compactor")
	}
	if c.Store == nil {
		missing = append(missing, "Store")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: missing %s", ErrInvalidConfig, strings.Join(missing, ", "))
	}
	if c.MaxTurns < 0 {
		return fmt.Errorf("%w: MaxTurns must be >= 0", ErrInvalidConfig)
	}
	if c.MaxToolConcurrency < 0 {
		return fmt.Errorf("%w: MaxToolConcurrency must be >= 0", ErrInvalidConfig)
	}
	return nil
}

// Input 是一轮输入。TurnID 由调用方预先生成，便于绑定层立即返回给前端。
type Input struct {
	TurnID string
	Prompt string
}

// Result 是一轮调用的结果。模型与工具错误不会以 error 返回，而是体现在 StopReason/Err。
type Result struct {
	SessionID  string
	TurnID     string
	StopReason StopReason
	Text       string
	Usage      llm.Usage
	Turns      int
	Err        string
}

// EventSink 接收事件。契约：只从 turn goroutine 调用，绝不并发调用；
// 实现必须快速返回，且不得回调 Engine 的任何方法。
type EventSink func(Event)

// EventFunc 是 EventSink 的兼容别名（契约文档使用该名称）。
type EventFunc = EventSink

// Engine 是一个会话的运行时。并发契约：
//   - runMu 用 TryLock 保证同一时刻只有一个 Run，第二个 Run 立即返回 ErrTurnRunning；
//   - stateMu 保护 history、turnCancel、running 与压缩所需的时间/用量快照；
//   - History 在 RLock 下返回深拷贝；Cancel 只读 stateMu，不会被运行中的 Run 阻塞。
type Engine struct {
	cfg             Config
	runMu           sync.Mutex
	stateMu         sync.RWMutex
	history         []llm.Message
	turnCancel      context.CancelFunc
	running         bool
	lastAssistantAt time.Time
	lastUsage       llm.Usage
}

// NewEngine 创建引擎；Config 非法时返回错误。
func NewEngine(cfg Config) (*Engine, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.MaxToolConcurrency == 0 {
		cfg.MaxToolConcurrency = defaultMaxToolConcurrency
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.NewID == nil {
		cfg.NewID = defaultNewID
	}
	return &Engine{cfg: cfg}, nil
}

// defaultNewID 生成 "prefix_" + 16 字节随机数的十六进制。
func defaultNewID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return prefix + "_" + fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

// Restore 从 Store 载入历史。必须在第一次 Run 之前调用。
// Store 返回 ErrSessionNotFound 时视为新会话；ErrCorruptLines 表示部分行损坏，
// 但 Load 已返回可用记录，这里继续重放；其它错误原样返回。
func (e *Engine) Restore(ctx context.Context) error {
	recs, err := e.cfg.Store.Load(ctx, e.cfg.SessionID)
	if err != nil {
		switch {
		case errors.Is(err, session.ErrSessionNotFound):
			return nil
		case errors.Is(err, session.ErrCorruptLines):
			// 损坏行已被 Store 跳过，继续使用可用记录。
		default:
			return fmt.Errorf("agent: restore: %w", err)
		}
	}
	loaded, err := session.Replay(recs)
	if err != nil {
		return fmt.Errorf("agent: replay: %w", err)
	}
	// 恢复会话最后保存的权限模式；非法值保持当前配置，不让损坏记录阻断恢复。
	if loaded.Mode != "" {
		if mode, err := permission.ParseMode(loaded.Mode); err == nil {
			_ = e.cfg.Permissions.SetMode(mode)
		}
	}
	msgs := loaded.Messages
	if loaded.Interrupted {
		msgs = append(msgs, llm.Message{
			ID:     e.cfg.NewID("msg"),
			Role:   llm.RoleUser,
			Blocks: []llm.ContentBlock{{Type: llm.BlockText, Text: "Continue from where you left off."}},
			Time:   e.now(),
		})
	}
	e.stateMu.Lock()
	e.history = cloneMessages(msgs)
	e.lastUsage = loaded.LastUsage
	e.lastAssistantAt = loaded.LastAssistantAt
	e.stateMu.Unlock()
	return nil
}

// History 返回消息历史深拷贝。
func (e *Engine) History() []llm.Message {
	e.stateMu.RLock()
	defer e.stateMu.RUnlock()
	return cloneMessages(e.history)
}

// SetPermissionMode 切换会话的权限模式；对后续工具调用立即生效。
func (e *Engine) SetPermissionMode(mode permission.Mode) error {
	return e.cfg.Permissions.SetMode(mode)
}

// Cancel 取消进行中的 Run（只取消当前轮的 turnCtx）。
// 没有进行中的轮次时是空操作，不影响之后的新轮次。
func (e *Engine) Cancel() {
	e.stateMu.RLock()
	cancel := e.turnCancel
	e.stateMu.RUnlock()
	if cancel != nil {
		cancel()
	}
}

// Close 释放引擎持有的资源（不关闭 Store）。
func (e *Engine) Close() error {
	e.Cancel()
	return nil
}

// Run 执行一轮用户输入。
//
// 返回 error 仅表示「无法继续」的内部故障（配置非法、transcript 写入失败、并发 Run）；
// 模型错误、工具错误、用户取消、panic 都通过 Result.StopReason 表达，error 为 nil。
// 无论从哪条路径返回（含 panic），turn_end 都恰好发出一次且是本轮最后一个事件。
func (e *Engine) Run(ctx context.Context, in Input, emit EventSink) (res Result, err error) {
	if emit == nil {
		emit = func(Event) {}
	}
	if !e.runMu.TryLock() {
		return Result{SessionID: e.cfg.SessionID, TurnID: in.TurnID}, ErrTurnRunning
	}
	defer e.runMu.Unlock()

	if in.TurnID == "" {
		in.TurnID = e.cfg.NewID("turn")
	}
	res = Result{SessionID: e.cfg.SessionID, TurnID: in.TurnID}

	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
	defer persistCancel()

	e.stateMu.Lock()
	e.running = true
	e.turnCancel = cancel
	e.stateMu.Unlock()

	t := &turn{engine: e, ctx: turnCtx, persistCtx: persistCtx, in: in, emit: emit, res: res}

	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprintf("panic: %v", r)
			t.emitErrorView("panic", &ErrorView{Kind: "panic", Message: msg})
			t.res.StopReason = StopError
			t.res.Err = msg
		}
		t.res.Usage = t.roundUsage
		e.stateMu.Lock()
		e.running = false
		e.turnCancel = nil
		e.stateMu.Unlock()
		t.appendTurnEnd()
		_ = e.cfg.Store.Flush(persistCtx)
		t.finish()
		res = t.res
	}()

	err = t.run()
	return
}

// now 返回配置的时间源。
func (e *Engine) now() time.Time { return e.cfg.Now() }

// historySnapshot 返回历史深拷贝，供请求组装与压缩使用。
func (e *Engine) historySnapshot() []llm.Message {
	e.stateMu.RLock()
	defer e.stateMu.RUnlock()
	return cloneMessages(e.history)
}

// replaceHistory 用压缩后的历史整体替换当前历史。
func (e *Engine) replaceHistory(msgs []llm.Message) {
	cp := cloneMessages(msgs)
	e.stateMu.Lock()
	e.history = cp
	e.stateMu.Unlock()
}

// appendHistory 追加一条消息到内存历史。
func (e *Engine) appendHistory(msg llm.Message) {
	e.stateMu.Lock()
	e.history = append(e.history, cloneMessages([]llm.Message{msg})[0])
	e.stateMu.Unlock()
}

// recordAssistantMeta 记录最近一条 assistant 的时间与本次请求的用量，
// 供下一次 BeforeCall 判断 micro/auto 压缩。用量必须是最近一次真实请求的值，
// 不能跨请求累加：InputTokens 描述的是该次上下文规模，累加值会随会话单调增长，
// 与当前上下文无关（见 docs/agent-architecture.md §7.5）。
func (e *Engine) recordAssistantMeta(at time.Time, u llm.Usage) {
	e.stateMu.Lock()
	e.lastAssistantAt = at
	e.lastUsage = u
	e.stateMu.Unlock()
}

// resetLastUsage 在历史被压缩替换后清空最近一次用量：
// 旧值描述的是压缩前的上下文规模，继续用它判断会重复触发无效压缩或误判阻塞线。
func (e *Engine) resetLastUsage() {
	e.stateMu.Lock()
	e.lastUsage = llm.Usage{}
	e.stateMu.Unlock()
}

// metaSnapshot 返回压缩所需的最近 assistant 时间与最近一次 usage。
func (e *Engine) metaSnapshot() (time.Time, llm.Usage) {
	e.stateMu.RLock()
	defer e.stateMu.RUnlock()
	return e.lastAssistantAt, e.lastUsage
}

// Approvals 是 ask 决策的异步审批通道。
// 它把 permission.ApprovalView 登记为待审批项，前端通过 Decide 投递答复；
// ctx 取消时等待方自行清理，不会留下 goroutine。
type Approvals struct {
	mu      sync.Mutex
	seq     int64
	pending map[string]*pendingApproval
}

// pendingApproval 是一个待审批项。通道容量固定为 1，最多投递一次。
type pendingApproval struct {
	view   permission.ApprovalView
	ch     chan permission.Response
	closed bool
	seq    int64
}

// NewApprovals 创建空的审批通道。
func NewApprovals() *Approvals {
	return &Approvals{pending: make(map[string]*pendingApproval)}
}

// Prepare 登记 view.RequestID 并返回应答通道与幂等清理函数。
// 调用方必须在通知前端之前调用，保证用户秒回时也能找到登记。
func (a *Approvals) Prepare(view permission.ApprovalView) (<-chan permission.Response, func()) {
	a.mu.Lock()
	a.seq++
	entry := &pendingApproval{view: view, ch: make(chan permission.Response, 1), seq: a.seq}
	a.pending[view.RequestID] = entry
	a.mu.Unlock()

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			current, ok := a.pending[view.RequestID]
			if !ok || current != entry {
				return
			}
			delete(a.pending, view.RequestID)
			if !entry.closed {
				entry.closed = true
				close(entry.ch)
			}
		})
	}
	return entry.ch, cleanup
}

// Decide 投递一次审批答复；未知或已结束的 requestID 返回 false（幂等）。
func (a *Approvals) Decide(requestID string, resp permission.Response) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	entry, ok := a.pending[requestID]
	if !ok || entry.closed {
		return false
	}
	delete(a.pending, requestID)
	entry.closed = true
	entry.ch <- resp
	close(entry.ch)
	return true
}

// Pending 返回该会话尚未应答的审批请求快照（按登记顺序）。
func (a *Approvals) Pending(sessionID string) []permission.ApprovalView {
	a.mu.Lock()
	entries := make([]*pendingApproval, 0, len(a.pending))
	for _, e := range a.pending {
		if e.view.SessionID == sessionID {
			entries = append(entries, e)
		}
	}
	a.mu.Unlock()

	sort.Slice(entries, func(i, j int) bool { return entries[i].seq < entries[j].seq })
	out := make([]permission.ApprovalView, len(entries))
	for i, e := range entries {
		out[i] = e.view
	}
	return out
}

// CancelSession 关闭该会话全部待审批项并返回其 requestID。
func (a *Approvals) CancelSession(sessionID string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var ids []string
	for id, e := range a.pending {
		if e.view.SessionID != sessionID {
			continue
		}
		delete(a.pending, id)
		if !e.closed {
			e.closed = true
			close(e.ch)
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// cloneMessages 深拷贝消息数组（块、工具输入与 usage 都复制）。
func cloneMessages(msgs []llm.Message) []llm.Message {
	if msgs == nil {
		return nil
	}
	out := make([]llm.Message, len(msgs))
	for i, m := range msgs {
		out[i] = m
		if m.Blocks != nil {
			out[i].Blocks = make([]llm.ContentBlock, len(m.Blocks))
			copy(out[i].Blocks, m.Blocks)
			for j := range out[i].Blocks {
				if m.Blocks[j].Input != nil {
					input := make([]byte, len(m.Blocks[j].Input))
					copy(input, m.Blocks[j].Input)
					out[i].Blocks[j].Input = input
				}
			}
		}
		if m.Usage != nil {
			u := *m.Usage
			out[i].Usage = &u
		}
	}
	return out
}

// sumUsage 逐字段相加，用于本轮用量的累计（会话最近用量见 recordAssistantMeta）。
func sumUsage(a, b llm.Usage) llm.Usage {
	return llm.Usage{
		InputTokens:              a.InputTokens + b.InputTokens,
		OutputTokens:             a.OutputTokens + b.OutputTokens,
		CacheReadInputTokens:     a.CacheReadInputTokens + b.CacheReadInputTokens,
		CacheCreationInputTokens: a.CacheCreationInputTokens + b.CacheCreationInputTokens,
	}
}

// mergeUsage 合并同一条消息分段上报的 usage：other 中非零字段覆盖 a。
func mergeUsage(a, b llm.Usage) llm.Usage {
	if b.InputTokens != 0 {
		a.InputTokens = b.InputTokens
	}
	if b.OutputTokens != 0 {
		a.OutputTokens = b.OutputTokens
	}
	if b.CacheReadInputTokens != 0 {
		a.CacheReadInputTokens = b.CacheReadInputTokens
	}
	if b.CacheCreationInputTokens != 0 {
		a.CacheCreationInputTokens = b.CacheCreationInputTokens
	}
	return a
}

// usageTotal 返回输入与输出 token 之和。
func usageTotal(u llm.Usage) int { return u.InputTokens + u.OutputTokens }

// toolSpecs 把工具规格转换成模型请求格式。
func toolSpecs(specs []tool.Spec) []llm.ToolSpec {
	out := make([]llm.ToolSpec, len(specs))
	for i, s := range specs {
		out[i] = llm.ToolSpec{Name: s.Name, Description: s.Description, InputSchema: s.InputSchema}
	}
	return out
}
