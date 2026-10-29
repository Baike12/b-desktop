package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Baike12/b-desktop/internal/agent"
	"github.com/Baike12/b-desktop/internal/llm"
	"github.com/Baike12/b-desktop/internal/permission"
	"github.com/Baike12/b-desktop/internal/session"
)

// persistTimeout 是桥接层 transcript 写入的独立超时，与 agent 引擎一致：
// 写入脱离轮次取消，用户取消后 tool_result 仍必须落盘。
const persistTimeout = 5 * time.Second

// Bridge 把一个 ACP 会话的更新流转成 agent.Event 并负责 transcript 落盘。
// 它对外暴露与 agent.Engine 相同的运行时接口（Run/Cancel/Close/
// SetPermissionMode），app 层据此把 benben 与外部 agent 统一路由。
//
// 并发契约：Run 串行（同时最多一轮）；Cancel/Close 可从任意 goroutine 调用；
// 更新回调从后端 goroutine 进入，通过锁保护累积状态与事件出口。
type Bridge struct {
	backend Backend
	store   session.Store
	opts    BridgeOptions

	mu         sync.Mutex
	sess       Session
	key        string // 后端会话键（claude 会话 uuid 等），持久化到 agent_state
	closed     bool
	cancelTurn context.CancelFunc
	permSeq    int
	// runMu 串行化 Run；runEmit 与 runAcc 只在 Run 期间有效。
	runMu   sync.Mutex
	runEmit agent.EventSink
	runAcc  *accumulator
}

// BridgeOptions 是桥接层的构造参数。
type BridgeOptions struct {
	// SessionID 是 b-desktop 会话 ID。
	SessionID string
	// WorkDir 是会话工作目录（ACP 的 cwd）。
	WorkDir string
	// Approvals 是会话级审批通道，与本地引擎共享。
	Approvals *agent.Approvals
	// Store 是 transcript 存储。
	Store session.Store
	// ResumeKey 是上次的后端会话键；空表示全新会话。
	ResumeKey string
	// PermissionMode 是初始权限模式；空用 agent 默认值。
	PermissionMode string
	// AgentID 是 agent 标识（写 agent_state 记录用）。
	AgentID string
	// MCPServers 是传给 agent 的 MCP 配置。
	MCPServers []MCPServer
	// NewID 生成记录 ID；nil 用默认实现。
	NewID func(prefix string) string
	// Now 是时间源；nil 用 time.Now。
	Now func() time.Time
}

// NewBridge 创建桥接层。
func NewBridge(backend Backend, opts BridgeOptions) *Bridge {
	if opts.NewID == nil {
		opts.NewID = defaultRecordID
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Bridge{backend: backend, store: opts.Store, opts: opts, key: opts.ResumeKey}
}

// defaultRecordID 生成 "prefix_<毫秒时间戳>" 的记录 ID。
func defaultRecordID(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

// Run 执行一轮用户输入。错误只表示内部故障（会话创建失败、落盘失败、并发）；
// agent 错误通过 Result.StopReason 表达。
func (b *Bridge) Run(ctx context.Context, in agent.Input, emit agent.EventSink) (agent.Result, error) {
	if emit == nil {
		emit = func(agent.Event) {}
	}
	if !b.runMu.TryLock() {
		return agent.Result{SessionID: b.opts.SessionID, TurnID: in.TurnID}, agent.ErrTurnRunning
	}
	defer b.runMu.Unlock()

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return agent.Result{SessionID: b.opts.SessionID, TurnID: in.TurnID}, agent.ErrTurnRunning
	}
	sess := b.sess
	b.mu.Unlock()

	if in.TurnID == "" {
		in.TurnID = b.opts.NewID("turn")
	}
	res := agent.Result{SessionID: b.opts.SessionID, TurnID: in.TurnID}

	// 本轮的事件出口与累积器：更新回调从后端 goroutine 进入，靠字段交换。
	b.mu.Lock()
	b.runEmit = emit
	b.runAcc = newAccumulator(b)
	b.mu.Unlock()
	acc := b.runAcc
	defer func() {
		b.mu.Lock()
		b.runEmit = nil
		b.runAcc = nil
		b.mu.Unlock()
	}()

	if sess == nil {
		var err error
		sess, err = b.ensureSession(ctx)
		if err != nil {
			return b.failTurn(res, "agent", err)
		}
		b.mu.Lock()
		b.sess = sess
		b.mu.Unlock()
	}

	turnCtx, cancel := context.WithCancel(ctx)
	b.mu.Lock()
	b.cancelTurn = cancel
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.cancelTurn = nil
		b.mu.Unlock()
		cancel()
	}()

	b.emitEvent(agent.Event{Type: agent.EventTurnStart, TurnID: in.TurnID})

	// 用户消息：先落盘，再进 prompt。
	userMsg := b.userMessage(in)
	if len(userMsg.Blocks) > 0 {
		if err := b.appendRecord(ctx, session.Record{Type: session.RecordMessage, Message: &userMsg}); err != nil {
			return b.failTurn(res, "storage", err)
		}
	}
	b.emitEvent(agent.Event{Type: agent.EventStepStart, TurnID: in.TurnID, Step: 1})

	promptCtx, promptCancel := context.WithCancel(turnCtx)
	defer promptCancel()
	result, promptErr := sess.Prompt(promptCtx, promptBlocks(in))
	acc.flush()
	if promptErr != nil {
		if errors.Is(promptErr, ErrResumeNotFound) {
			// resume 键失效：丢掉会话与键，下一轮自动重开。
			b.mu.Lock()
			b.sess = nil
			b.key = ""
			b.mu.Unlock()
		}
		return b.failTurn(res, "agent", promptErr)
	}

	if result.Error != "" {
		b.emitEvent(agent.Event{
			Type: agent.EventError, TurnID: in.TurnID,
			Error: &agent.ErrorView{Kind: "agent", Message: result.Error},
		})
		res.StopReason = agent.StopError
		res.Err = result.Error
	} else {
		res.StopReason = mapStopReason(result.StopReason)
	}

	if result.Usage != nil {
		usage := llm.Usage{
			InputTokens:              result.Usage.InputTokens,
			OutputTokens:             result.Usage.OutputTokens,
			CacheReadInputTokens:     result.Usage.CachedRead,
			CacheCreationInputTokens: result.Usage.CachedWrite,
		}
		res.Usage = usage
	}
	res.Turns = result.Turns

	if result.Usage != nil {
		b.emitEvent(agent.Event{
			Type: agent.EventUsage, TurnID: in.TurnID,
			Usage: usagePtr(res.Usage), ContextTokens: res.Usage.ContextTokens(),
		})
		usageRecord := res.Usage
		if err := b.appendRecord(ctx, session.Record{Type: session.RecordUsage, Usage: &usageRecord}); err != nil {
			b.emitEvent(agent.Event{
				Type: agent.EventError, TurnID: in.TurnID,
				Error: &agent.ErrorView{Kind: "storage", Message: err.Error()},
			})
		}
	}

	// 会话键变化（首轮创建、/clear 重置）时落盘 agent_state。
	b.persistSessionKey(sess)

	b.emitEvent(agent.Event{
		Type: agent.EventTurnEnd, TurnID: in.TurnID,
		StopReason: res.StopReason, Usage: usagePtr(res.Usage),
		ContextTokens: res.Usage.ContextTokens(),
	})
	turnEnd := session.TurnEnd{TurnID: in.TurnID, StopReason: string(res.StopReason), Error: res.Err, Turns: res.Turns}
	if err := b.appendRecord(ctx, session.Record{Type: session.RecordTurnEnd, TurnEnd: &turnEnd}); err != nil {
		b.emitEvent(agent.Event{
			Type: agent.EventError, TurnID: in.TurnID,
			Error: &agent.ErrorView{Kind: "storage", Message: err.Error()},
		})
	}
	return res, nil
}

// emitEvent 补齐公共字段并推送事件；Run 之外调用是空操作。
func (b *Bridge) emitEvent(ev agent.Event) {
	b.mu.Lock()
	emit := b.runEmit
	b.mu.Unlock()
	if emit == nil {
		return
	}
	ev.SessionID = b.opts.SessionID
	ev.TimeMS = b.opts.Now().UnixMilli()
	emit(ev)
}

// failTurn 以错误收尾：发 error 与 turn_end 事件并返回。
func (b *Bridge) failTurn(res agent.Result, kind string, err error) (agent.Result, error) {
	res.StopReason = agent.StopError
	res.Err = err.Error()
	b.emitEvent(agent.Event{
		Type: agent.EventError, TurnID: res.TurnID,
		Error: &agent.ErrorView{Kind: kind, Message: err.Error()},
	})
	b.emitEvent(agent.Event{
		Type: agent.EventTurnEnd, TurnID: res.TurnID, StopReason: res.StopReason,
	})
	return res, err
}

// mapStopReason 把 ACP 结束原因映射成本地 StopReason。
func mapStopReason(reason StopReason) agent.StopReason {
	switch reason {
	case StopEndTurn:
		return agent.StopCompleted
	case StopMaxTokens, StopMaxTurnReqs:
		return agent.StopMaxTurns
	case StopRefusal:
		return agent.StopError
	case StopCancelled:
		return agent.StopCanceled
	default:
		return agent.StopCompleted
	}
}

func usagePtr(u llm.Usage) *llm.Usage { return &u }

// userMessage 构造用户消息记录（文本 + 附件 + 图片）。
func (b *Bridge) userMessage(in agent.Input) llm.Message {
	blocks := make([]llm.ContentBlock, 0, 1+len(in.Images)+len(in.Attachments))
	if in.Prompt != "" {
		blocks = append(blocks, llm.ContentBlock{Type: llm.BlockText, Text: in.Prompt})
	}
	for _, block := range in.Attachments {
		if (block.Type == llm.BlockText && block.Text != "") || (block.Type == llm.BlockImage && block.Data != "") {
			blocks = append(blocks, block)
		}
	}
	for _, img := range in.Images {
		if img.Type == llm.BlockImage && img.Data != "" {
			blocks = append(blocks, img)
		}
	}
	return llm.Message{ID: b.opts.NewID("msg"), Role: llm.RoleUser, Blocks: blocks, Time: b.opts.Now()}
}

// promptBlocks 构造发给 agent 的内容块（文本 + 图片）。
func promptBlocks(in agent.Input) []ContentBlock {
	blocks := make([]ContentBlock, 0, 1+len(in.Images))
	if in.Prompt != "" {
		blocks = append(blocks, ContentBlock{Type: "text", Text: in.Prompt})
	}
	for _, img := range in.Images {
		if img.Type == llm.BlockImage && img.Data != "" {
			blocks = append(blocks, ContentBlock{Type: "image", Data: img.Data, MimeType: img.MediaType})
		}
	}
	return blocks
}

// ensureSession 创建 ACP 会话；resume 键失效时自动重开新会话。
func (b *Bridge) ensureSession(ctx context.Context) (Session, error) {
	params := SessionParams{
		CWD:            b.opts.WorkDir,
		MCPServers:     b.opts.MCPServers,
		PermissionMode: b.opts.PermissionMode,
		ResumeKey:      b.key,
	}
	sess, err := b.backend.NewSession(ctx, params, b.handleUpdate, b)
	if err == nil {
		return sess, nil
	}
	if !errors.Is(err, ErrResumeNotFound) {
		return nil, fmt.Errorf("acp: create agent session: %w", err)
	}
	b.mu.Lock()
	b.key = ""
	b.mu.Unlock()
	params.ResumeKey = ""
	sess, err = b.backend.NewSession(ctx, params, b.handleUpdate, b)
	if err != nil {
		return nil, fmt.Errorf("acp: create agent session: %w", err)
	}
	return sess, nil
}

// persistSessionKey 在后端会话键变化时追加 agent_state 记录。
func (b *Bridge) persistSessionKey(sess Session) {
	keyer, ok := sess.(interface{ SessionKey() string })
	if !ok {
		return
	}
	key := keyer.SessionKey()
	b.mu.Lock()
	changed := key != "" && key != b.key
	if changed {
		b.key = key
	}
	b.mu.Unlock()
	if !changed {
		return
	}
	record := session.Record{
		Type:      session.RecordAgentState,
		SessionID: b.opts.SessionID,
		Time:      b.opts.Now(),
		Meta:      map[string]string{"agent": b.opts.AgentID, b.opts.AgentID + ".key": key},
	}
	_ = b.appendRecord(context.Background(), record)
}

// Cancel 取消进行中的一轮。
func (b *Bridge) Cancel() {
	b.mu.Lock()
	cancel := b.cancelTurn
	sess := b.sess
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if sess != nil {
		sess.Cancel()
	}
}

// Close 释放会话资源，幂等。
func (b *Bridge) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	cancel := b.cancelTurn
	sess := b.sess
	b.cancelTurn = nil
	b.sess = nil
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if sess != nil {
		_ = sess.Close()
	}
	return nil
}

// SetPermissionMode 切换 agent 的权限模式。
func (b *Bridge) SetPermissionMode(mode permission.Mode) error {
	b.mu.Lock()
	sess := b.sess
	b.mu.Unlock()
	if sess == nil {
		return nil // 会话未创建：初始模式在 NewSession 时传入。
	}
	return sess.SetMode(context.Background(), string(mode))
}

// appendRecord 在脱离取消的 persistCtx 下追加一条记录。
func (b *Bridge) appendRecord(ctx context.Context, rec session.Record) error {
	rec.SessionID = b.opts.SessionID
	if rec.Time.IsZero() {
		rec.Time = b.opts.Now()
	}
	if rec.ID == "" {
		rec.ID = b.opts.NewID("rec")
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
	defer cancel()
	if err := b.store.Append(persistCtx, rec); err != nil {
		return fmt.Errorf("acp: append record: %w", err)
	}
	return nil
}

// accumulator 累积一轮的持久化状态：当前 assistant 消息与待落盘的工具结果。
// 只累积主线程内容；子线程内容只产生事件，不进 transcript（与重放配对算法
// 兼容：tool_result 必须紧跟其 assistant 消息）。
type accumulator struct {
	bridge *Bridge

	mu             sync.Mutex
	messageID      string
	message        *llm.Message
	pendingResults []llm.ContentBlock
	nextSynthetic  int
}

// newAccumulator 创建累积器。
func newAccumulator(b *Bridge) *accumulator { return &accumulator{bridge: b} }

// syntheticMessageID 生成兜底消息 ID。
func (a *accumulator) syntheticMessageID() string {
	a.nextSynthetic++
	return fmt.Sprintf("msg_acp_%d", a.nextSynthetic)
}

// currentMessageID 返回当前分组键；无分组时生成一个。
func (a *accumulator) currentMessageID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.messageID == "" {
		a.messageID = a.syntheticMessageID()
	}
	return a.messageID
}

// startMessage 切换到新的 assistant 消息；切换前先 flush。
func (a *accumulator) startMessage(messageID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if messageID == "" || messageID == a.messageID {
		return
	}
	a.flushLocked()
	a.messageID = messageID
	a.message = &llm.Message{ID: messageID, Role: llm.RoleAssistant, Blocks: []llm.ContentBlock{}, Time: a.bridge.opts.Now()}
}

// ensureMessage 保证存在当前消息；纯工具消息（无文本前缀）使用兜底 ID。
func (a *accumulator) ensureMessage() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.message == nil {
		if a.messageID == "" {
			a.messageID = a.syntheticMessageID()
		}
		a.message = &llm.Message{ID: a.messageID, Role: llm.RoleAssistant, Blocks: []llm.ContentBlock{}, Time: a.bridge.opts.Now()}
	}
}

// flushLocked 把当前 assistant 消息与其工具结果落盘（调用方持锁）。
func (a *accumulator) flushLocked() {
	if a.message != nil && len(a.message.Blocks) > 0 {
		msg := *a.message
		_ = a.bridge.appendRecord(context.Background(), session.Record{Type: session.RecordMessage, Message: &msg})
		a.message = nil
	}
	if len(a.pendingResults) > 0 {
		results := a.pendingResults
		a.pendingResults = nil
		msg := llm.Message{
			ID:     a.bridge.opts.NewID("msg"),
			Role:   llm.RoleUser,
			Blocks: results,
			Time:   a.bridge.opts.Now(),
		}
		_ = a.bridge.appendRecord(context.Background(), session.Record{Type: session.RecordMessage, Message: &msg})
	}
	a.messageID = ""
}

// flush 把当前 assistant 消息与其工具结果落盘。
func (a *accumulator) flush() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.flushLocked()
}

// appendText 向当前消息追加文本/思考块；空文本不产生块。
func (a *accumulator) appendText(kind llm.BlockType, text string) {
	if text == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.message == nil {
		if a.messageID == "" {
			a.messageID = a.syntheticMessageID()
		}
		a.message = &llm.Message{ID: a.messageID, Role: llm.RoleAssistant, Blocks: []llm.ContentBlock{}, Time: a.bridge.opts.Now()}
	}
	a.message.Blocks = append(a.message.Blocks, llm.ContentBlock{Type: kind, Text: text})
}

// appendToolUse 向当前消息追加 tool_use 块。
func (a *accumulator) appendToolUse(toolUseID, toolName string, input json.RawMessage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.message == nil {
		if a.messageID == "" {
			a.messageID = a.syntheticMessageID()
		}
		a.message = &llm.Message{ID: a.messageID, Role: llm.RoleAssistant, Blocks: []llm.ContentBlock{}, Time: a.bridge.opts.Now()}
	}
	a.message.Blocks = append(a.message.Blocks, llm.ContentBlock{
		Type: llm.BlockToolUse, ToolUseID: toolUseID, ToolName: toolName, Input: input,
	})
}

// appendToolResult 缓存一个工具结果，随下一段一起落盘。
func (a *accumulator) appendToolResult(toolUseID string, text string, isError bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pendingResults = append(a.pendingResults, llm.ContentBlock{
		Type: llm.BlockToolResult, ToolUseID: toolUseID, Text: text, IsError: isError,
	})
}

// handleUpdate 把一条 ACP 更新转成事件并更新累积状态。
// 由后端 goroutine 在 Prompt 期间调用。
func (b *Bridge) handleUpdate(update SessionUpdate) {
	b.mu.Lock()
	acc := b.runAcc
	b.mu.Unlock()
	if acc == nil {
		return
	}
	base := agent.Event{AgentID: update.AgentID}

	switch update.Kind {
	case UpdateAgentMessageChunk, UpdateAgentThoughtChunk:
		if update.Content == nil {
			return
		}
		text := update.Content.Text
		if text == "" {
			// 空块是消息边界信号：切换持久化分组，不产生事件。
			if update.AgentID == "" {
				acc.startMessage(update.MessageID)
			}
			return
		}
		messageID := update.MessageID
		if messageID == "" {
			messageID = acc.currentMessageID()
		}
		base.MessageID = messageID
		base.Text = text
		if update.Kind == UpdateAgentMessageChunk {
			base.Type = agent.EventTextDelta
			if update.AgentID == "" {
				acc.appendText(llm.BlockText, text)
			}
		} else {
			base.Type = agent.EventThinkingDelta
			if update.AgentID == "" {
				acc.appendText(llm.BlockThinking, text)
			}
		}
		b.emitEvent(base)
	case UpdateToolCall:
		if update.ToolCall == nil {
			return
		}
		call := update.ToolCall
		toolName := call.ToolName
		if toolName == "" {
			toolName = kindToolName(call.Kind)
		}
		if update.AgentID == "" {
			acc.startMessage(update.MessageID)
			acc.ensureMessage()
			acc.appendToolUse(call.ToolCallID, toolName, call.RawInput)
		}
		base.Type = agent.EventToolCallStart
		base.ToolUseID = call.ToolCallID
		base.ToolName = toolName
		base.Input = json.RawMessage(call.RawInput)
		b.emitEvent(base)
		if len(call.RawInput) > 0 {
			ready := base
			ready.Type = agent.EventToolCallReady
			b.emitEvent(ready)
		}
	case UpdateToolCallUpdate:
		if update.ToolCallUpdate == nil {
			return
		}
		patch := update.ToolCallUpdate
		if patch.InputDelta != "" {
			base.Type = agent.EventToolInputDelta
			base.ToolUseID = patch.ToolCallID
			base.Text = patch.InputDelta
			b.emitEvent(base)
		}
		if len(patch.RawInput) > 0 {
			base.Type = agent.EventToolCallReady
			base.ToolUseID = patch.ToolCallID
			base.Input = json.RawMessage(patch.RawInput)
			base.ToolName = patch.ToolName
			b.emitEvent(base)
		}
		switch patch.Status {
		case StatusCompleted, StatusFailed:
			preview := toolOutputPreview(patch.RawOutput, patch.Content)
			isError := patch.Status == StatusFailed
			if update.AgentID == "" {
				acc.appendToolResult(patch.ToolCallID, preview, isError)
			}
			base.Type = agent.EventToolResult
			base.ToolUseID = patch.ToolCallID
			base.Result = &agent.ToolResultView{
				Preview: preview,
				IsError: isError,
				Bytes:   len(preview),
			}
			b.emitEvent(base)
		}
	case UpdatePlan:
		if update.Plan == nil {
			return
		}
		items := make([]agent.TodoItemView, 0, len(update.Plan.Entries))
		for _, entry := range update.Plan.Entries {
			items = append(items, agent.TodoItemView{Content: entry.Content, Status: entry.Status})
		}
		base.Type = agent.EventTodoUpdate
		base.Todos = &agent.TodoStateView{Items: items}
		b.emitEvent(base)
	case UpdateAvailableCommands:
		commands := make([]agent.CommandView, 0, len(update.Commands))
		for _, command := range update.Commands {
			commands = append(commands, agent.CommandView{Name: command.Name, Description: command.Description, Hint: command.Hint})
		}
		base.Type = agent.EventCommandsUpdate
		base.Commands = &agent.CommandsView{Commands: commands}
		b.emitEvent(base)
	case UpdateCurrentMode:
		base.Type = agent.EventAgentStatus
		base.AgentStatus = &agent.AgentStatusView{Mode: update.CurrentModeID}
		b.emitEvent(base)
	case UpdateStatus:
		if update.Status == nil {
			return
		}
		base.Type = agent.EventAgentStatus
		base.AgentStatus = &agent.AgentStatusView{
			Model:         update.Status.Model,
			ContextWindow: update.Status.ContextWindow,
			Mode:          update.Status.Mode,
		}
		b.emitEvent(base)
	default:
	}
}

// kindToolName 从工具类别回退工具名（外部 ACP agent 不带原始工具名时使用）。
func kindToolName(kind ToolKind) string {
	switch kind {
	case ToolRead:
		return "Read"
	case ToolEdit:
		return "Edit"
	case ToolDelete:
		return "Delete"
	case ToolMove:
		return "Move"
	case ToolSearch:
		return "Search"
	case ToolExecute:
		return "Bash"
	case ToolThink:
		return "Task"
	case ToolFetch:
		return "WebFetch"
	case ToolSwitchMode:
		return "PlanMode"
	default:
		return "Tool"
	}
}

// toolOutputPreview 从 RawOutput 或内容块提取结果预览文本。
func toolOutputPreview(raw json.RawMessage, contents []ToolCallContent) string {
	if len(raw) > 0 {
		var text string
		if err := json.Unmarshal(raw, &text); err == nil {
			return text
		}
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &blocks); err == nil {
			parts := make([]string, 0, len(blocks))
			for _, block := range blocks {
				if block.Text != "" {
					parts = append(parts, block.Text)
				}
			}
			return strings.Join(parts, "\n")
		}
		return string(raw)
	}
	parts := make([]string, 0, len(contents))
	for _, content := range contents {
		if content.Content != nil && content.Content.Text != "" {
			parts = append(parts, content.Content.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// RequestPermission 实现 acp.Client：审批请求走会话级 Approvals 通道。
func (b *Bridge) RequestPermission(ctx context.Context, req PermissionRequest) (PermissionOutcome, error) {
	approvals := b.opts.Approvals
	if approvals == nil {
		return PermissionOutcome{}, errors.New("acp: approvals channel unavailable")
	}
	b.mu.Lock()
	b.permSeq++
	requestID := fmt.Sprintf("perm_%d", b.permSeq)
	b.mu.Unlock()

	options := req.Options
	if len(options) == 0 {
		options = []PermissionOption{
			{OptionID: "allow-once", Name: "允许一次", Kind: AllowOnce},
			{OptionID: "allow-with-updates", Name: "总是允许", Kind: AllowAlways},
			{OptionID: "reject", Name: "拒绝", Kind: RejectOnce},
		}
	}
	input := req.ToolCall.RawInput
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	view := permission.ApprovalView{
		RequestID: requestID,
		SessionID: b.opts.SessionID,
		ToolName:  req.ToolName,
		Input:     input,
		Reason:    req.ToolCall.Title,
		Options:   make([]permission.ApprovalOption, 0, len(options)),
	}
	for _, option := range options {
		view.Options = append(view.Options, permission.ApprovalOption{ID: option.OptionID, Label: option.Name})
	}

	// Prepare 必须先于事件通知：保证用户秒回时能找到登记。
	ch, cleanup := approvals.Prepare(view)
	defer cleanup()
	b.emitEvent(agent.Event{
		Type:      agent.EventPermissionRequest,
		ToolName:  req.ToolName,
		ToolUseID: req.ToolCall.ToolCallID,
		Approval:  &view,
	})

	select {
	case resp, open := <-ch:
		if !open {
			return PermissionOutcome{Cancelled: true}, nil
		}
		b.emitEvent(agent.Event{
			Type:     agent.EventPermissionResolved,
			Text:     approvalOutcomeLabel(resp.Option),
			Approval: &view,
		})
		return PermissionOutcome{OptionID: resp.Option}, nil
	case <-ctx.Done():
		// 用户停止本轮：发 resolved 事件让前端收起弹窗，返回 cancelled。
		b.emitEvent(agent.Event{
			Type:     agent.EventPermissionResolved,
			Text:     "canceled",
			Approval: &view,
		})
		return PermissionOutcome{Cancelled: true}, nil
	}
}

// approvalOutcomeLabel 把审批选项映射为事件文本（与本地引擎一致）。
func approvalOutcomeLabel(option string) string {
	switch option {
	case "allow_once", "allow-once", "allow_always", "allow-with-updates":
		return "allow"
	case "deny_once", "deny_always", "reject", "reject_once", "reject_always":
		return "deny"
	default:
		return "canceled"
	}
}

// ReadTextFile 实现 acp.Client：读取工作目录内的文本文件。
func (b *Bridge) ReadTextFile(ctx context.Context, path string) (string, error) {
	if !b.pathAllowed(path) {
		return "", fmt.Errorf("acp: path outside workspace: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("acp: read file: %w", err)
	}
	return string(data), nil
}

// WriteTextFile 实现 acp.Client：写工作目录内的文本文件。
func (b *Bridge) WriteTextFile(ctx context.Context, path, content string) error {
	if !b.pathAllowed(path) {
		return fmt.Errorf("acp: path outside workspace: %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("acp: create dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("acp: write file: %w", err)
	}
	return nil
}

// pathAllowed 报告路径是否位于会话工作目录内。
func (b *Bridge) pathAllowed(path string) bool {
	if b.opts.WorkDir == "" {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(b.opts.WorkDir, abs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
