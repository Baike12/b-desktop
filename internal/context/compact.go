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
const compactSummaryPrefix = "The conversation was compacted. Summary:\n"

// clearedToolResult 是旧工具结果的替换文本。
const clearedToolResult = "[old tool result cleared]"

// SummaryInstruction 是发给摘要模型的固定指令（五段式 Goal / State / Context / Next / Pitfalls）。
const SummaryInstruction = "你负责压缩对话上下文。请阅读完整对话，输出一份能让另一个 Agent 无缝继续工作的摘要。\n" +
	"严格按五段输出，每段用标题开头：\n" +
	"Goal: 用户最终想完成什么。\n" +
	"State: 已完成的工作与当前代码/环境状态。\n" +
	"Context: 继续工作必须知道的约束、路径、命令与结论。\n" +
	"Next: 接下来要做的具体步骤。\n" +
	"Pitfalls: 已知的坑、失败过的尝试与注意事项。\n" +
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

// MicroCompact 把最近 keepRecent 组之外的旧工具结果替换为 "[old tool result cleared]"，
// 返回新切片（不修改入参）、替换块数与节省的 token 估算。
// 节省不足 256 token 时整体放弃，返回原切片。
func MicroCompact(msgs []llm.Message, keepRecent int) (out []llm.Message, cleared int, savedTokens int) {
	if keepRecent < 0 {
		keepRecent = 0
	}
	groups := make([]int, 0, len(msgs))
	for i, m := range msgs {
		if m.Role == llm.RoleUser && hasToolResult(m) {
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
			if strings.TrimSpace(b.Text) == "" || b.Text == clearedToolResult {
				continue
			}
			b.Text = clearedToolResult
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

	summary, err := c.summarize(ctx, msgs, progress)
	if err != nil && isContextOverflow(err) {
		trimmed := trimOldestGroup(msgs)
		if len(trimmed) < len(msgs) {
			summary, err = c.summarize(ctx, trimmed, progress)
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

// hasToolResult 报告消息是否包含工具结果块。
func hasToolResult(m llm.Message) bool {
	for _, b := range m.Blocks {
		if b.Type == llm.BlockToolResult {
			return true
		}
	}
	return false
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
