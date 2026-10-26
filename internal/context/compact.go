package context

import (
	stdctx "context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/Baike12/b-desktop/internal/llm"
)

// 预算与压缩的默认值，与设计文档 §7.3、§7.4、§7.5 一致。
const (
	defaultMaxOutputTokens    = 8192
	defaultAutoBuffer         = 13000
	defaultReservedForSummary = 20000
	defaultBlockingBuffer     = 3000
	autoCompactMinWindow      = 32000
	defaultKeepMinTokens      = 10000
	defaultKeepMaxTokens      = 40000
	defaultKeepMinMessages    = 4
	defaultKeepRecentResults  = 5
	defaultMicroIdle          = 60 * time.Minute
	defaultSummaryMaxTokens   = 8000
	defaultContextWindow      = 200000 // 未知模型的默认窗口，与 llm 的未知模型约定一致
	microCompactMinSaved      = 256
	maxConsecutiveFailures    = 3
)

// compactSummaryPrefix 是压缩后注入历史的第一条 user 消息前缀。
// 语境句与参考实现一致（cc compact/prompt.ts：压缩摘要以 "This session is
// being continued..." 开头），让模型明确知道自己拿到的是续接摘要而非对话起点。
const compactSummaryPrefix = "This session is being continued from a previous conversation " +
	"that ran out of context. The summary below covers the earlier portion of the conversation:\n\n"

// clearedToolResult 是旧工具结果的替换文本。
const clearedToolResult = "[old tool result cleared]"

// compactableTools 是微压缩允许清除旧结果的白名单（与 cc 的 COMPACTABLE_TOOLS
// 一致，microCompact.ts）：这些工具的旧结果可以安全丢弃，模型需要时可重新调用；
// 白名单之外（MCP、AskUserQuestion、TodoWrite 等）与错误结果一律保留。
var compactableTools = map[string]bool{
	"Read":       true,
	"Bash":       true,
	"Grep":       true,
	"Glob":       true,
	"Edit":       true,
	"Write":      true,
	"WebFetch":   true,
	"WebSearch":  true,
	"PowerShell": true,
}

// SummaryInstruction 是发给摘要模型的固定指令（与 cc/ZCode 的九段结构一致，
// compact/prompt.ts）：首尾禁工具，要求逐字保留安全相关指令。
const SummaryInstruction = "你负责压缩对话上下文。请阅读完整对话，输出一份能让另一个 Agent 无缝继续工作的摘要。\n" +
	"只输出摘要文本，不要调用任何工具，工具调用会被拒绝。\n" +
	"严格按以下九段输出，每段以段名开头（保留英文段名）：\n" +
	"1. Primary Request and Intent: 用户的最终目标与全部明确要求，逐条列出。\n" +
	"2. Key Technical Concepts: 涉及的关键技术、架构与约定。\n" +
	"3. Files and Code Sections: 已读/已改的文件路径、关键改动与代码片段。\n" +
	"4. Errors and fixes: 出现过的错误与最终修复方式；未解决的错误单独标注。\n" +
	"5. Problem Solving: 问题处理思路与已放弃的方案。\n" +
	"6. All user messages: 按顺序列出用户的全部非工具消息（截取要点，保留原意）。\n" +
	"7. Pending Tasks: 尚未完成的任务。\n" +
	"8. Current Work: 被压缩时刻正在做什么、进行到哪一步。\n" +
	"9. Optional Next Step: 接下来具体要做的事；没有则写 None。\n" +
	"安全相关指令（权限边界、用户否决过的操作）必须逐字保留，不得改写。\n" +
	"只写事实，不要寒暄，不要编造未发生的事。"

// 压缩错误哨兵。
var (
	// ErrCompactionDisabled 表示压缩已因连续失败熔断。
	ErrCompactionDisabled = errors.New("context: compaction disabled after repeated failures")
	// ErrNoSummarizer 表示既没有注入 Summarizer，也没有可用于构造默认实现的 Provider。
	ErrNoSummarizer = errors.New("context: no summarizer configured")
	// ErrEmptySummary 表示摘要模型返回了空文本。
	ErrEmptySummary = errors.New("context: empty summary")
	// ErrNothingToCompact 表示没有可被压缩掉的历史。
	ErrNothingToCompact = errors.New("context: nothing to compact")
)

// CompactPhase 是压缩进度阶段。
type CompactPhase string

// 压缩进度阶段常量。
const (
	CompactPhaseStart  CompactPhase = "start"  // CompactPhaseStart 表示开始压缩。
	CompactPhaseDelta  CompactPhase = "delta"  // CompactPhaseDelta 表示摘要流式增量。
	CompactPhaseDone   CompactPhase = "done"   // CompactPhaseDone 表示压缩成功。
	CompactPhaseFailed CompactPhase = "failed" // CompactPhaseFailed 表示压缩失败。
)

// CompactProgress 是一次压缩进度。Failed 阶段带 Err，供前端结束「压缩中」状态。
type CompactProgress struct {
	Phase CompactPhase
	Text  string
	Err   string
}

// CompactProgressFunc 接收压缩进度。可能为 nil；实现必须非阻塞。
type CompactProgressFunc func(CompactProgress)

// Budget 是压缩阈值参数。零值字段在 NewBudget 中填默认值。
type Budget struct {
	ContextWindow      int
	MaxOutputTokens    int // <=0 时按 8192
	AutoBuffer         int // 默认 13000
	ReservedForSummary int // 默认 20000
	BlockingBuffer     int // 默认 3000
}

// Budget 状态取值。
const (
	StatusOK       = "ok"       // StatusOK 表示无需处理。
	StatusWarning  = "warning"  // StatusWarning 表示接近自动压缩阈值。
	StatusAuto     = "auto"     // StatusAuto 表示应触发自动压缩。
	StatusBlocking = "blocking" // StatusBlocking 表示超过阻塞线。
)

// NewBudget 填充默认值。阈值由各方法实时计算，零值 Budget 也可安全使用。
//   - reset         = min(ReservedForSummary, max(2048, ContextWindow/4))
//   - effective     = max(4096, ContextWindow - min(MaxOutputTokens, reset))
//   - autoThreshold = max(effective/2, effective - AutoBuffer)
//   - warning       = max(0, autoThreshold - 20000)
//   - blocking      = max(autoThreshold, effective - BlockingBuffer)
func NewBudget(b Budget) Budget {
	if b.MaxOutputTokens <= 0 {
		b.MaxOutputTokens = defaultMaxOutputTokens
	}
	if b.AutoBuffer <= 0 {
		b.AutoBuffer = defaultAutoBuffer
	}
	if b.ReservedForSummary <= 0 {
		b.ReservedForSummary = defaultReservedForSummary
	}
	if b.BlockingBuffer <= 0 {
		b.BlockingBuffer = defaultBlockingBuffer
	}
	return b
}

// AutoCompactEnabled 报告是否启用摘要式压缩。ContextWindow < 32000 时禁用，
// 只保留 microCompact 与阻塞线，避免摘要请求自身超过窗口。
func (b Budget) AutoCompactEnabled() bool {
	return b.ContextWindow >= autoCompactMinWindow
}

// EffectiveWindow 返回有效窗口。
func (b Budget) EffectiveWindow() int {
	n := NewBudget(b)
	reserve := max(2048, n.ContextWindow/4)
	reserve = min(reserve, n.ReservedForSummary)
	output := min(n.MaxOutputTokens, reserve)
	return max(4096, n.ContextWindow-output)
}

// AutoCompactThreshold 返回自动压缩阈值。
func (b Budget) AutoCompactThreshold() int {
	n := NewBudget(b)
	effective := n.EffectiveWindow()
	return max(effective/2, effective-n.AutoBuffer)
}

// WarningThreshold 返回警告阈值。
func (b Budget) WarningThreshold() int {
	return max(0, b.AutoCompactThreshold()-20000)
}

// BlockingLimit 返回阻塞线。
func (b Budget) BlockingLimit() int {
	n := NewBudget(b)
	effective := n.EffectiveWindow()
	return max(n.AutoCompactThreshold(), effective-n.BlockingBuffer)
}

// Status 返回 ok / warning / auto / blocking。警告阈值为 0 时视为未启用警告。
func (b Budget) Status(tokens int) string {
	switch {
	case tokens >= b.BlockingLimit():
		return StatusBlocking
	case tokens >= b.AutoCompactThreshold():
		return StatusAuto
	case b.WarningThreshold() > 0 && tokens >= b.WarningThreshold():
		return StatusWarning
	default:
		return StatusOK
	}
}

// Summarizer 把一段对话历史压缩成可继续对话的摘要文本。
// 注入它即替换摘要的生成方式（测试里传 fake，生产上可用 ProviderSummarizer）。
type Summarizer interface {
	// Summarize 返回摘要。返回错误表示失败；返回空串同样按失败处理。
	Summarize(ctx stdctx.Context, msgs []llm.Message, instruction string) (string, error)
}

// StreamingSummarizer 是可选扩展：支持逐段回调摘要文本的 Summarizer。
// 实现它时，AutoCompact 会把增量作为 CompactPhaseDelta 上报。
type StreamingSummarizer interface {
	SummarizeStream(ctx stdctx.Context, msgs []llm.Message, instruction string, onDelta func(string)) (string, error)
}

// ProviderSummarizer 用 llm.Provider 实现 Summarizer：发一次无工具的流式请求并拼接文本。
type ProviderSummarizer struct {
	Provider  llm.Provider
	Model     string
	MaxTokens int
}

// NewProviderSummarizer 创建基于 Provider 的摘要器；maxTokens <= 0 时取 8000。
func NewProviderSummarizer(provider llm.Provider, model string, maxTokens int) *ProviderSummarizer {
	if maxTokens <= 0 {
		maxTokens = defaultSummaryMaxTokens
	}
	return &ProviderSummarizer{Provider: provider, Model: model, MaxTokens: maxTokens}
}

// Summarize 实现 Summarizer。
func (s *ProviderSummarizer) Summarize(ctx stdctx.Context, msgs []llm.Message, instruction string) (string, error) {
	return s.SummarizeStream(ctx, msgs, instruction, nil)
}

// SummarizeStream 实现 StreamingSummarizer。
func (s *ProviderSummarizer) SummarizeStream(ctx stdctx.Context, msgs []llm.Message, instruction string, onDelta func(string)) (string, error) {
	if s.Provider == nil {
		return "", ErrNoSummarizer
	}
	stream, err := s.Provider.Stream(ctx, llm.Request{
		Model:     s.Model,
		System:    instruction,
		Messages:  msgs,
		MaxTokens: s.MaxTokens,
	})
	if err != nil {
		return "", fmt.Errorf("context: summary request: %w", err)
	}
	defer func() { _ = stream.Close() }()

	var sb strings.Builder
	for {
		ev, err := stream.Recv(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("context: summary stream: %w", err)
		}
		switch ev.Type {
		case llm.EventTextDelta:
			sb.WriteString(ev.Text)
			if onDelta != nil && ev.Text != "" {
				onDelta(ev.Text)
			}
		case llm.EventError:
			if ev.Error != nil {
				// 用 %w 保留 *llm.StreamError，超窗时 AutoCompact 才能重试一次。
				return "", fmt.Errorf("context: summary stream: %w", ev.Error)
			}
		}
	}
	return sb.String(), nil
}

// BeforeCallInput 是每次模型调用前的压缩输入。
type BeforeCallInput struct {
	Messages        []llm.Message
	LastAssistantAt time.Time // 零值表示未知（新会话）
	LastUsage       llm.Usage // 最近一次 provider 返回的 usage；零值表示无
}

// CompactorConfig 是 Compactor 的构造参数。
type CompactorConfig struct {
	Provider llm.Provider // 未注入 Summarizer 时用于构造 ProviderSummarizer
	Model    string
	// Summarizer 显式注入的摘要器，优先级高于 Provider。
	Summarizer Summarizer
	// Estimator 可替换的 token 估算器；nil 时用 DefaultEstimator。
	Estimator             Estimator
	Budget                Budget
	KeepMinTokens         int           // 默认 10000
	KeepMaxTokens         int           // 默认 40000
	KeepMinMessages       int           // 默认 4
	KeepRecentToolResults int           // 默认 5
	MicroIdle             time.Duration // 默认 60m
	Now                   func() time.Time
}

// Compactor 在每次模型调用前执行 microCompact → autoCompact。除熔断计数外不持有可变状态。
type Compactor struct {
	summarizer Summarizer
	estimator  Estimator
	budget     Budget
	model      string

	keepMinTokens    int
	keepMaxTokens    int
	keepMinMessages  int
	keepRecentResult int
	microIdle        time.Duration
	now              func() time.Time

	mu       sync.Mutex
	failures int
}

// NewCompactor 创建压缩器。既没有 Summarizer 也没有 Provider 时返回 ErrNoSummarizer。
func NewCompactor(cfg CompactorConfig) (*Compactor, error) {
	summarizer := cfg.Summarizer
	if summarizer == nil {
		if cfg.Provider == nil {
			return nil, ErrNoSummarizer
		}
		summarizer = NewProviderSummarizer(cfg.Provider, cfg.Model, defaultSummaryMaxTokens)
	}
	estimator := cfg.Estimator
	if estimator == nil {
		estimator = DefaultEstimator{}
	}
	budget := cfg.Budget
	if budget.ContextWindow <= 0 {
		// 未显式配置时按未知模型的默认窗口处理，避免压缩被永久关闭。
		budget.ContextWindow = defaultContextWindow
	}
	budget = NewBudget(budget)

	c := &Compactor{
		summarizer:       summarizer,
		estimator:        estimator,
		budget:           budget,
		model:            cfg.Model,
		keepMinTokens:    cfg.KeepMinTokens,
		keepMaxTokens:    cfg.KeepMaxTokens,
		keepMinMessages:  cfg.KeepMinMessages,
		keepRecentResult: cfg.KeepRecentToolResults,
		microIdle:        cfg.MicroIdle,
		now:              cfg.Now,
	}
	if c.keepMinTokens <= 0 {
		c.keepMinTokens = defaultKeepMinTokens
	}
	if c.keepMaxTokens <= 0 {
		c.keepMaxTokens = defaultKeepMaxTokens
	}
	if c.keepMinMessages <= 0 {
		c.keepMinMessages = defaultKeepMinMessages
	}
	if c.keepRecentResult <= 0 {
		c.keepRecentResult = defaultKeepRecentResults
	}
	if c.microIdle <= 0 {
		c.microIdle = defaultMicroIdle
	}
	if c.now == nil {
		c.now = time.Now
	}
	return c, nil
}

// Budget 返回构造时冻结的预算，供 agent 做阻塞线判定。
func (c *Compactor) Budget() Budget {
	return c.budget
}

// CompactInfo 描述一次压缩。Compactor 不持有 Store，也不写任何记录。
type CompactInfo struct {
	Trigger      string
	Model        string
	Summary      string
	PreTokens    int
	PostTokens   int
	KeptMessages []llm.Message
}

// BeforeCallResult 是 BeforeCall 的输出。
type BeforeCallResult struct {
	Messages  []llm.Message
	Compacted bool
	Micro     bool
	Info      *CompactInfo
}

// BeforeCall 按需压缩：
//   - microCompact：距最后一条 assistant 超过 MicroIdle，或估算 token 落在
//     [0.9*auto, auto-2000] 区间；
//   - autoCompact：估算 token 或最近一次真实 usage 达到 auto 阈值，且窗口 >= 32k。
//
// 压缩失败不阻断调用：返回原消息、Compacted=false 与错误，由调用方记录。
// 一旦决定压缩，必然回调 Start → Done 或 Start → Failed。
func (c *Compactor) BeforeCall(ctx stdctx.Context, in BeforeCallInput, progress CompactProgressFunc) (res BeforeCallResult, err error) {
	res.Messages = in.Messages
	if len(in.Messages) == 0 {
		return res, nil
	}

	est := c.estimate(in.Messages)
	if usage := usageTotal(in.LastUsage); usage > est {
		est = usage
	}
	auto := c.budget.AutoCompactThreshold()
	usageTokens := usageTotal(in.LastUsage)

	idle := !in.LastAssistantAt.IsZero() && c.now().Sub(in.LastAssistantAt) >= c.microIdle
	pressure := est >= auto*9/10 && est <= auto-2000
	needMicro := idle || pressure
	needAuto := c.budget.AutoCompactEnabled() && (est >= auto || usageTokens >= auto)
	if !needMicro && !needAuto {
		return res, nil
	}

	emitProgress(progress, CompactProgress{Phase: CompactPhaseStart})
	defer func() {
		if err != nil {
			emitProgress(progress, CompactProgress{Phase: CompactPhaseFailed, Err: err.Error()})
			return
		}
		emitProgress(progress, CompactProgress{Phase: CompactPhaseDone})
	}()

	msgs := in.Messages
	if needMicro {
		out, cleared, saved := MicroCompact(msgs, c.keepRecentResult)
		if cleared > 0 && saved > 0 {
			msgs = out
			res.Micro = true
			res.Compacted = true
		}
	}

	if c.budget.AutoCompactEnabled() {
		estAfter := c.estimate(msgs)
		if estAfter >= auto || usageTokens >= auto {
			info, newMsgs, compactErr := c.AutoCompact(ctx, msgs, "auto", progress)
			if compactErr != nil {
				res.Messages = in.Messages
				res.Compacted = false
				res.Micro = false
				return res, compactErr
			}
			res.Messages = newMsgs
			res.Compacted = true
			res.Info = &info
			return res, nil
		}
	}

	res.Messages = msgs
	return res, nil
}

// toolUseNames 从历史 assistant 消息里收集 toolUseID → 工具名。
// 微压缩据此判断一条旧结果是否来自白名单工具。
func toolUseNames(msgs []llm.Message) map[string]string {
	names := make(map[string]string)
	for _, m := range msgs {
		if m.Role != llm.RoleAssistant {
			continue
		}
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolUse && b.ToolUseID != "" && b.ToolName != "" {
				names[b.ToolUseID] = b.ToolName
			}
		}
	}
	return names
}

// MicroCompact 把白名单工具（compactableTools）最近 keepRecent 组之外的旧
// tool_result 替换为 "[old tool result cleared]"，返回新切片（不修改入参）、
// 替换块数与节省的 token 估算。错误结果一律保留（它们是恢复工作的关键线索），
// 白名单之外的工具与身份不明的结果也保留。节省不足 256 token 时整体放弃，
// 返回原切片。
func MicroCompact(msgs []llm.Message, keepRecent int) (out []llm.Message, cleared int, savedTokens int) {
	if keepRecent < 0 {
		keepRecent = 0
	}
	names := toolUseNames(msgs)
	groups := make([]int, 0, len(msgs))
	for i, m := range msgs {
		if m.Role == llm.RoleUser && hasClearableToolResult(m, names) {
			groups = append(groups, i)
		}
	}
	if len(groups) <= keepRecent {
		return msgs, 0, 0
	}

	out = make([]llm.Message, len(msgs))
	copy(out, msgs)
	before := EstimateMessages(msgs)

	for _, idx := range groups[:len(groups)-keepRecent] {
		clone := cloneMessage(msgs[idx])
		changed := false
		for i := range clone.Blocks {
			b := &clone.Blocks[i]
			if b.Type != llm.BlockToolResult {
				continue
			}
			if b.IsError || strings.TrimSpace(b.Text) == "" || b.Text == clearedToolResult {
				if len(b.Images) == 0 {
					continue
				}
			}
			b.Text = clearedToolResult
			b.Images = nil
			changed = true
			cleared++
		}
		if changed {
			out[idx] = clone
		}
	}
	if cleared == 0 {
		return msgs, 0, 0
	}
	savedTokens = before - EstimateMessages(out)
	if savedTokens < microCompactMinSaved {
		return msgs, 0, 0
	}
	return out, cleared, savedTokens
}

// hasClearableToolResult 报告消息里是否含有「白名单工具的非错误结果」。
func hasClearableToolResult(m llm.Message, names map[string]string) bool {
	for _, b := range m.Blocks {
		if b.Type != llm.BlockToolResult {
			continue
		}
		if b.IsError {
			continue
		}
		if name, ok := names[b.ToolUseID]; ok && compactableTools[name] {
			return true
		}
	}
	return false
}

// AutoCompact 调用注入的 Summarizer 生成摘要，返回「摘要消息 + 保留的最近消息」。
// 连续失败 3 次后熔断，返回 ErrCompactionDisabled。
// progress 只上报 Delta；Start/Done/Failed 由 BeforeCall 统一负责。
func (c *Compactor) AutoCompact(ctx stdctx.Context, msgs []llm.Message, trigger string, progress CompactProgressFunc) (CompactInfo, []llm.Message, error) {
	if c.disabled() {
		return CompactInfo{}, msgs, ErrCompactionDisabled
	}
	if len(msgs) == 0 {
		return CompactInfo{}, msgs, ErrNothingToCompact
	}

	preTokens := c.estimate(msgs)
	tail := c.keepTail(msgs)
	if len(tail) == 0 || len(tail) >= len(msgs) {
		return CompactInfo{}, msgs, ErrNothingToCompact
	}

	summary, err := c.summarize(ctx, stripImages(msgs), progress)
	if err != nil && isContextOverflow(err) {
		trimmed := trimOldestGroup(msgs)
		if len(trimmed) < len(msgs) {
			summary, err = c.summarize(ctx, stripImages(trimmed), progress)
			if err == nil {
				msgs = trimmed
				tail = c.keepTail(msgs)
			}
		}
	}
	if err != nil {
		return CompactInfo{}, msgs, c.recordFailure(fmt.Errorf("context: summarize: %w", err))
	}
	if strings.TrimSpace(summary) == "" {
		return CompactInfo{}, msgs, c.recordFailure(ErrEmptySummary)
	}
	c.recordSuccess()

	newMsgs := make([]llm.Message, 0, len(tail)+1)
	newMsgs = append(newMsgs, summaryMessage(summary))
	newMsgs = append(newMsgs, tail...)

	return CompactInfo{
		Trigger:      trigger,
		Model:        c.model,
		Summary:      summary,
		PreTokens:    preTokens,
		PostTokens:   c.estimate(newMsgs),
		KeptMessages: tail,
	}, newMsgs, nil
}

func (c *Compactor) summarize(ctx stdctx.Context, msgs []llm.Message, progress CompactProgressFunc) (string, error) {
	if s, ok := c.summarizer.(StreamingSummarizer); ok {
		return s.SummarizeStream(ctx, msgs, SummaryInstruction, func(delta string) {
			emitProgress(progress, CompactProgress{Phase: CompactPhaseDelta, Text: delta})
		})
	}
	return c.summarizer.Summarize(ctx, msgs, SummaryInstruction)
}

// stripImages 返回去掉图片内容的消息副本：BlockImage 替换成 "[image]" 文本占位，
// tool_result 附带的图片同样移除并在文本尾部补 "[image]"。摘要请求不需要
// 看图（与参考实现一致），保留只会撑爆摘要调用的上下文。
// 传入的切片不被修改；保留尾部（keepTail）仍使用原始消息。
func stripImages(msgs []llm.Message) []llm.Message {
	stripped := false
	out := make([]llm.Message, len(msgs))
	for i, m := range msgs {
		hasImages := false
		for _, b := range m.Blocks {
			if b.Type == llm.BlockImage || len(b.Images) > 0 {
				hasImages = true
				break
			}
		}
		if !hasImages {
			out[i] = m
			continue
		}
		stripped = true
		clone := cloneMessage(m)
		blocks := make([]llm.ContentBlock, 0, len(clone.Blocks))
		for _, b := range clone.Blocks {
			if b.Type == llm.BlockImage {
				blocks = append(blocks, llm.ContentBlock{Type: llm.BlockText, Text: "[image]"})
				continue
			}
			if len(b.Images) > 0 {
				b.Images = nil
				if b.Type == llm.BlockToolResult && !strings.Contains(b.Text, "[image]") {
					b.Text = strings.TrimRight(b.Text, "\n") + "\n[image]"
				}
			}
			blocks = append(blocks, b)
		}
		clone.Blocks = blocks
		out[i] = clone
	}
	if !stripped {
		return msgs
	}
	return out
}

// keepTail 按设计文档的保留策略取尾部：
//   - 至少 KeepMinMessages 条；
//   - 累计至少 KeepMinTokens，且不超过 KeepMaxTokens（单条超长消息仍会保留）；
//   - 最后一条用户输入永远保留；
//   - 起点落在只含 tool_result 的 user 消息时，再前移一条 assistant 消息，保证配对完整。
func (c *Compactor) keepTail(msgs []llm.Message) []llm.Message {
	if len(msgs) == 0 {
		return nil
	}
	start := len(msgs)
	total, count := 0, 0
	for i := len(msgs) - 1; i >= 0; i-- {
		t := estimateMessage(c.estimator, msgs[i])
		if count >= c.keepMinMessages && total >= c.keepMinTokens {
			break
		}
		if count >= c.keepMinMessages && c.keepMaxTokens > 0 && total+t > c.keepMaxTokens {
			break
		}
		total += t
		count++
		start = i
	}

	if lastUser := lastUserInputIndex(msgs); lastUser >= 0 && lastUser < start {
		start = lastUser
	}
	for start > 0 && isToolResultOnlyUser(msgs[start]) {
		start--
	}

	out := make([]llm.Message, len(msgs)-start)
	copy(out, msgs[start:])
	return out
}

func (c *Compactor) estimate(msgs []llm.Message) int {
	return c.estimator.EstimateMessages(msgs)
}

func (c *Compactor) disabled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failures >= maxConsecutiveFailures
}

func (c *Compactor) recordFailure(err error) error {
	c.mu.Lock()
	c.failures++
	n := c.failures
	c.mu.Unlock()
	if n >= maxConsecutiveFailures {
		return fmt.Errorf("%w: %w", ErrCompactionDisabled, err)
	}
	return err
}

func (c *Compactor) recordSuccess() {
	c.mu.Lock()
	c.failures = 0
	c.mu.Unlock()
}

// summaryMessage 把摘要包装成压缩边界后的第一条 user 消息。
func summaryMessage(summary string) llm.Message {
	return llm.Message{
		Role:   llm.RoleUser,
		Blocks: []llm.ContentBlock{{Type: llm.BlockText, Text: compactSummaryPrefix + summary}},
	}
}

// usageTotal 返回一次调用的输入与输出 token 之和。
func usageTotal(u llm.Usage) int {
	return u.InputTokens + u.OutputTokens
}

// emitProgress 在 progress 非 nil 时回调。
func emitProgress(progress CompactProgressFunc, p CompactProgress) {
	if progress != nil {
		progress(p)
	}
}

// isContextOverflow 识别「摘要请求自身超窗」的错误。
// 生产路径的 Provider 返回 *llm.StreamError（Kind 为 ErrKindContextOverflow），
// 测试替身也可以实现 ContextOverflow() bool。
func isContextOverflow(err error) bool {
	var o interface{ ContextOverflow() bool }
	if errors.As(err, &o) && o.ContextOverflow() {
		return true
	}
	var se *llm.StreamError
	return errors.As(err, &se) && se.Kind == llm.ErrKindContextOverflow
}

// trimOldestGroup 从最旧处删除一整组（assistant + 其 tool_result），用于超窗后重试一次。
func trimOldestGroup(msgs []llm.Message) []llm.Message {
	if len(msgs) == 0 {
		return msgs
	}
	i := 1
	if msgs[0].Role == llm.RoleAssistant && hasToolUse(msgs[0]) {
		for i < len(msgs) && isToolResultOnlyUser(msgs[i]) {
			i++
		}
	}
	out := make([]llm.Message, len(msgs)-i)
	copy(out, msgs[i:])
	return out
}

// hasToolUse 报告消息是否包含工具调用块。
func hasToolUse(m llm.Message) bool {
	for _, b := range m.Blocks {
		if b.Type == llm.BlockToolUse {
			return true
		}
	}
	return false
}

// isToolResultOnlyUser 报告消息是否为「只含 tool_result 的 user 消息」。
func isToolResultOnlyUser(m llm.Message) bool {
	if m.Role != llm.RoleUser || len(m.Blocks) == 0 {
		return false
	}
	for _, b := range m.Blocks {
		if b.Type != llm.BlockToolResult {
			return false
		}
	}
	return true
}

// lastUserInputIndex 返回最后一条「真正的用户输入」下标：user 角色且不止是工具结果。
func lastUserInputIndex(msgs []llm.Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleUser && !isToolResultOnlyUser(msgs[i]) {
			return i
		}
	}
	return -1
}

// cloneMessage 深拷贝一条消息（块、工具输入与 usage）。
func cloneMessage(m llm.Message) llm.Message {
	out := m
	if m.Blocks != nil {
		out.Blocks = make([]llm.ContentBlock, len(m.Blocks))
		copy(out.Blocks, m.Blocks)
		for i := range out.Blocks {
			if out.Blocks[i].Input != nil {
				input := make([]byte, len(out.Blocks[i].Input))
				copy(input, out.Blocks[i].Input)
				out.Blocks[i].Input = input
			}
		}
	}
	if m.Usage != nil {
		usage := *m.Usage
		out.Usage = &usage
	}
	return out
}
