package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	sysctx "anc-dsk/internal/context"
	"anc-dsk/internal/llm"
	"anc-dsk/internal/permission"
	"anc-dsk/internal/session"
	"anc-dsk/internal/tool"
)

// toolResultPreviewBytes 是 tool_result 事件里预览文本的上限（完整结果在历史里）。
const toolResultPreviewBytes = 2048

// emptyToolOutput 是工具结果为空时回灌给模型的占位文本。
const emptyToolOutput = "<tool returned no output>"

// continuationPrompt 是恢复中断会话时追加的用户消息。
const continuationPrompt = "Continue from where you left off."

// turn 是一次 Run 的可变状态。所有字段只在 turn goroutine 中读写，
// 因此事件发射天然单线程，满足 EventSink 契约。
type turn struct {
	engine     *Engine
	ctx        context.Context
	persistCtx context.Context
	in         Input
	emit       EventSink

	res           Result
	step          int
	roundUsage    llm.Usage
	contextTokens int
	finished      bool
	turnEndSent   bool
}

// run 是单层状态机循环。唯一推进信号是 assistant 消息里的 tool_use：
// 有则执行并回灌，没有才结束本轮。
func (t *turn) run() error {
	e := t.engine
	e.cfg.Builder.BeginTurn()
	t.emitEvent(EventTurnStart, nil)

	userMsg := llm.Message{
		ID:     e.cfg.NewID("msg"),
		Role:   llm.RoleUser,
		Blocks: []llm.ContentBlock{{Type: llm.BlockText, Text: t.in.Prompt}},
		Time:   e.now(),
	}
	if err := t.appendMessage(userMsg); err != nil {
		t.fail("storage", err)
		return err
	}
	t.appendHistory(userMsg)

	for {
		if t.ctx.Err() != nil {
			t.end(StopCanceled, "")
			return nil
		}
		if e.cfg.MaxTurns > 0 && t.step >= e.cfg.MaxTurns {
			t.res.Turns = t.step
			t.end(StopMaxTurns, "")
			return nil
		}
		t.step++
		t.res.Turns = t.step
		t.emitEvent(EventStepStart, func(ev *Event) { ev.Step = t.step })

		system, specs, ended, err := t.prepare()
		if err != nil {
			t.fail("storage", err)
			return err
		}
		if ended {
			return nil
		}

		msg, ended, err := t.callModel(system, specs)
		if err != nil {
			t.fail("storage", err)
			return err
		}
		if ended {
			return nil
		}

		calls := toolCalls(msg)
		if len(calls) == 0 {
			t.end(StopCompleted, "")
			return nil
		}
		if err := t.executeTools(calls); err != nil {
			t.fail("storage", err)
			return err
		}
		if t.finished {
			return nil
		}
	}
}

// prepare 在每次模型调用前组装系统提示并检查 token 预算：
// 超过阻塞线直接结束；否则执行压缩，压缩后写入会话边界。
func (t *turn) prepare() (system string, specs []llm.ToolSpec, ended bool, err error) {
	e := t.engine
	registrySpecs := e.cfg.Tools.Specs()
	specs = toolSpecs(registrySpecs)

	history := e.historySnapshot()
	budget := e.cfg.Compactor.Budget()
	tokens := sysctx.EstimateMessages(history)
	lastAt, lastUsage := e.metaSnapshot()
	if u := usageTotal(lastUsage); u > tokens {
		tokens = u
	}
	if tokens >= budget.BlockingLimit() {
		msg := fmt.Sprintf("context estimate %d exceeds blocking limit %d", tokens, budget.BlockingLimit())
		t.emitErrorView("context", &ErrorView{Kind: "context", Message: msg})
		t.end(StopBlockingLimit, msg)
		return "", nil, true, nil
	}

	preTokens := sysctx.EstimateMessages(history)
	compactStarted := false
	var compactFailure string
	progress := func(p sysctx.CompactProgress) {
		switch p.Phase {
		case sysctx.CompactPhaseStart:
			if compactStarted {
				return
			}
			compactStarted = true
			t.emitEvent(EventCompactStart, func(ev *Event) {
				ev.Compact = &CompactView{Trigger: "auto"}
			})
		case sysctx.CompactPhaseFailed:
			compactFailure = p.Err
		}
	}

	compacted, compactErr := e.cfg.Compactor.BeforeCall(t.ctx, sysctx.BeforeCallInput{
		Messages:        history,
		LastAssistantAt: lastAt,
		LastUsage:       lastUsage,
	}, progress)

	if compactStarted {
		postTokens := sysctx.EstimateMessages(compacted.Messages)
		view := &CompactView{Trigger: "auto", PreTokens: preTokens, PostTokens: postTokens}
		switch {
		case compactErr != nil:
			view.Failed = true
			view.Error = compactErr.Error()
			if compactFailure != "" {
				view.Error = compactFailure
			}
		case compacted.Info != nil:
			view.Trigger = compacted.Info.Trigger
			view.PreTokens = compacted.Info.PreTokens
			view.PostTokens = compacted.Info.PostTokens
		case compacted.Micro:
			view.Trigger = "micro"
		}
		t.emitEvent(EventCompactEnd, func(ev *Event) { ev.Compact = view })
	}

	if compactErr != nil {
		// 压缩失败不阻断调用：保留原历史，错误只上报给前端。
		t.emitErrorView("compact", &ErrorView{Kind: "compact", Message: compactErr.Error()})
	} else if compacted.Compacted {
		if perr := t.persistCompaction(compacted, preTokens); perr != nil {
			return "", nil, false, perr
		}
		e.replaceHistory(compacted.Messages)
		// 压缩已替换历史，旧的最近用量描述的是压缩前的规模，不再适用。
		e.resetLastUsage()
	}

	if t.ctx.Err() != nil {
		t.end(StopCanceled, "")
		return "", nil, true, nil
	}

	prompt, serr := e.cfg.Builder.System(t.ctx, registrySpecs)
	if serr != nil {
		return "", nil, false, fmt.Errorf("agent: system prompt: %w", serr)
	}
	return prompt, specs, false, nil
}

// persistCompaction 写入压缩边界记录，并把保留的消息作为普通 message 记录追加，
// 保证「摘要 + 边界之后的消息」自洽。
func (t *turn) persistCompaction(res sysctx.BeforeCallResult, preTokens int) error {
	trigger := "micro"
	summary := ""
	pre := preTokens
	post := sysctx.EstimateMessages(res.Messages)
	model := t.engine.cfg.Model
	kept := res.Messages
	if res.Info != nil {
		trigger = res.Info.Trigger
		summary = res.Info.Summary
		pre = res.Info.PreTokens
		post = res.Info.PostTokens
		kept = res.Info.KeptMessages
		if res.Info.Model != "" {
			model = res.Info.Model
		}
	}
	rec := session.Record{
		Type: session.RecordCompact,
		Compact: &session.Compact{
			Trigger:    trigger,
			PreTokens:  pre,
			PostTokens: post,
			Summary:    summary,
			Model:      model,
		},
	}
	if err := t.appendRecord(rec); err != nil {
		return err
	}
	for _, m := range kept {
		if err := t.appendMessage(m); err != nil {
			return err
		}
	}
	if err := t.engine.cfg.Store.Flush(t.persistCtx); err != nil {
		return fmt.Errorf("agent: flush compact: %w", err)
	}
	return nil
}

// callModel 发起一次流式请求，逐条转发事件，最后把完整 assistant 消息落盘。
// 返回 ended=true 表示本轮已经结束（取消或错误），调用方应立即返回。
func (t *turn) callModel(system string, specs []llm.ToolSpec) (llm.Message, bool, error) {
	e := t.engine
	req := llm.Request{
		Model:     e.cfg.Model,
		System:    system,
		Messages:  e.historySnapshot(),
		Tools:     specs,
		MaxTokens: e.cfg.Compactor.Budget().MaxOutputTokens,
		RequestID: e.cfg.NewID("req"),
	}

	stream, err := e.cfg.Provider.Stream(t.ctx, req)
	if err != nil {
		if isCanceled(t.ctx, err) {
			t.end(StopCanceled, "")
			return llm.Message{}, true, nil
		}
		t.emitErrorView("model", errorViewOf(err))
		t.end(StopError, err.Error())
		return llm.Message{}, true, nil
	}
	defer func() { _ = stream.Close() }()

	sr := t.consumeStream(stream)
	if sr.err != nil {
		if sr.hadDelta {
			t.emitMessageDiscard(sr.msg.ID, sr.err)
		}
		if isCanceled(t.ctx, sr.err) {
			t.end(StopCanceled, "")
			return llm.Message{}, true, nil
		}
		t.emitErrorView("model", errorViewOf(sr.err))
		t.end(StopError, sr.err.Error())
		return llm.Message{}, true, nil
	}
	if t.ctx.Err() != nil {
		if sr.hadDelta {
			t.emitMessageDiscard(sr.msg.ID, t.ctx.Err())
		}
		t.end(StopCanceled, "")
		return llm.Message{}, true, nil
	}

	msg := sr.msg
	msg.Model = e.cfg.Model
	if msg.Time.IsZero() {
		msg.Time = e.now()
	}
	usage := sr.usage
	msg.Usage = &usage
	e.recordAssistantMeta(msg.Time, usage)
	t.roundUsage = sumUsage(t.roundUsage, usage)
	// 当前上下文取最近一次调用的规模，而不是本轮累计。
	t.contextTokens = usage.ContextTokens()
	if txt := textOf(msg); txt != "" {
		t.res.Text = txt
	}

	if len(msg.Blocks) > 0 {
		if err := t.appendMessage(msg); err != nil {
			return msg, false, err
		}
		t.appendHistory(msg)
	}
	if err := t.appendRecord(session.Record{Type: session.RecordUsage, Usage: &usage}); err != nil {
		return msg, false, err
	}
	t.emitEvent(EventMessageStop, func(ev *Event) { ev.MessageID = msg.ID })

	round := t.roundUsage
	contextTokens := t.contextTokens
	t.emitEvent(EventUsage, func(ev *Event) {
		ev.MessageID = msg.ID
		ev.Usage = &round
		ev.ContextTokens = contextTokens
	})
	return msg, false, nil
}

// streamResult 是一次流消费的结果。
type streamResult struct {
	msg      llm.Message
	usage    llm.Usage
	hadDelta bool
	err      error
}

// blockAcc 是单个内容块的累加器，键为块索引。
type blockAcc struct {
	index     int
	kind      llm.BlockType
	text      strings.Builder
	thinking  strings.Builder
	signature string
	toolID    string
	toolName  string
	partial   strings.Builder
	input     json.RawMessage
	closed    bool
}

// consumeStream 逐条转发流事件，并在结束时按块索引拼出 assistant 消息。
func (t *turn) consumeStream(stream llm.Stream) streamResult {
	var out streamResult
	accs := make(map[int]*blockAcc)
	messageID := ""
	started := false

	ensureStart := func() {
		if started {
			return
		}
		started = true
		if messageID == "" {
			messageID = t.engine.cfg.NewID("msg")
		}
		t.emitEvent(EventMessageStart, func(ev *Event) {
			ev.MessageID = messageID
			ev.Step = t.step
		})
	}

	// lookup 按 (Index, Kind) 找块。Kind 为空时按索引找已打开的块（契约 B1）。
	lookup := func(index int, kind llm.BlockType) *blockAcc {
		acc, ok := accs[index]
		if kind == "" {
			if !ok {
				return nil
			}
			return acc
		}
		if ok {
			if acc.kind != kind {
				return nil
			}
			return acc
		}
		acc = &blockAcc{index: index, kind: kind}
		accs[index] = acc
		return acc
	}

	for {
		ev, err := stream.Recv(t.ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			out.err = err
			break
		}
		switch ev.Type {
		case llm.EventMessageStart:
			if ev.MessageID != "" {
				messageID = ev.MessageID
			}
			if ev.Usage != nil {
				out.usage = mergeUsage(out.usage, *ev.Usage)
			}
			ensureStart()
		case llm.EventBlockStart:
			ensureStart()
			if lookup(ev.Index, ev.Kind) == nil {
				t.emitAdapterError(fmt.Sprintf("block_start with kind %q at index %d", ev.Kind, ev.Index))
				continue
			}
			index := ev.Index
			t.emitEvent(EventBlockStart, func(e *Event) {
				e.MessageID = messageID
				e.BlockIndex = index
				e.ToolName = ev.ToolName
			})
		case llm.EventTextDelta:
			ensureStart()
			acc := lookup(ev.Index, ev.Kind)
			if acc == nil || acc.kind != llm.BlockText {
				t.emitAdapterError(fmt.Sprintf("text_delta without text block at index %d", ev.Index))
				continue
			}
			acc.text.WriteString(ev.Text)
			out.hadDelta = true
			index, text := ev.Index, ev.Text
			t.emitEvent(EventTextDelta, func(e *Event) {
				e.MessageID = messageID
				e.BlockIndex = index
				e.Text = text
			})
		case llm.EventThinkingDelta:
			ensureStart()
			acc := lookup(ev.Index, ev.Kind)
			if acc == nil || acc.kind != llm.BlockThinking {
				t.emitAdapterError(fmt.Sprintf("thinking_delta without thinking block at index %d", ev.Index))
				continue
			}
			acc.thinking.WriteString(ev.Thinking)
			out.hadDelta = true
			index, text := ev.Index, ev.Thinking
			t.emitEvent(EventThinkingDelta, func(e *Event) {
				e.MessageID = messageID
				e.BlockIndex = index
				e.Text = text
			})
		case llm.EventSignature:
			acc := lookup(ev.Index, ev.Kind)
			if acc == nil || acc.kind != llm.BlockThinking {
				t.emitAdapterError(fmt.Sprintf("signature without thinking block at index %d", ev.Index))
				continue
			}
			acc.signature += ev.Signature
		case llm.EventToolUseStart:
			ensureStart()
			acc := lookup(ev.Index, llm.BlockToolUse)
			if acc == nil {
				t.emitAdapterError(fmt.Sprintf("tool_use_start collides at index %d", ev.Index))
				continue
			}
			acc.kind = llm.BlockToolUse
			acc.toolID = ev.ToolUseID
			if ev.ToolName != "" {
				acc.toolName = ev.ToolName
			}
			index, id, name := ev.Index, acc.toolID, acc.toolName
			t.emitEvent(EventToolCallStart, func(e *Event) {
				e.MessageID = messageID
				e.BlockIndex = index
				e.ToolUseID = id
				e.ToolName = name
			})
		case llm.EventToolInputDelta:
			acc := lookup(ev.Index, ev.Kind)
			if acc == nil || acc.kind != llm.BlockToolUse {
				t.emitAdapterError(fmt.Sprintf("tool_input_delta without tool block at index %d", ev.Index))
				continue
			}
			acc.partial.WriteString(ev.PartialJSON)
			out.hadDelta = true
			index, id, text := ev.Index, acc.toolID, ev.PartialJSON
			t.emitEvent(EventToolInputDelta, func(e *Event) {
				e.MessageID = messageID
				e.BlockIndex = index
				e.ToolUseID = id
				e.Text = text
			})
		case llm.EventToolUseStop:
			acc := lookup(ev.Index, ev.Kind)
			if acc == nil || acc.kind != llm.BlockToolUse {
				t.emitAdapterError(fmt.Sprintf("tool_use_stop without tool block at index %d", ev.Index))
				continue
			}
			t.closeToolBlock(acc, messageID)
		case llm.EventMessageDelta:
			if ev.Usage != nil {
				out.usage = mergeUsage(out.usage, *ev.Usage)
			}
			if ev.StopReason != "" {
				out.msg.StopReason = ev.StopReason
			}
		case llm.EventMessageStop:
			// 兜底关闭所有仍打开的工具块（按块索引升序）。
			for _, index := range sortedBlockIndices(accs) {
				acc := accs[index]
				if acc.kind == llm.BlockToolUse && !acc.closed {
					t.closeToolBlock(acc, messageID)
				}
			}
			goto assembled
		case llm.EventPing:
			// 心跳不转发。
		case llm.EventRetry:
			retry := ev.Retry
			t.emitEvent(EventRetry, func(e *Event) { e.Retry = retry })
		case llm.EventError:
			if ev.Error != nil {
				out.err = ev.Error
			} else {
				out.err = errors.New("llm stream error")
			}
			goto assembled
		}
	}

assembled:
	if messageID == "" {
		messageID = t.engine.cfg.NewID("msg")
	}
	out.msg = llm.Message{
		ID:         messageID,
		Role:       llm.RoleAssistant,
		Blocks:     assembleBlocks(accs),
		StopReason: out.msg.StopReason,
		Time:       t.engine.now(),
	}
	return out
}

// assembleBlocks 按块索引升序拼装内容块，丢弃空的文本/思考块。
func assembleBlocks(accs map[int]*blockAcc) []llm.ContentBlock {
	indices := sortedBlockIndices(accs)
	blocks := make([]llm.ContentBlock, 0, len(indices))
	for _, index := range indices {
		acc := accs[index]
		switch acc.kind {
		case llm.BlockText:
			if acc.text.Len() == 0 {
				continue
			}
			blocks = append(blocks, llm.ContentBlock{Type: llm.BlockText, Text: acc.text.String()})
		case llm.BlockThinking:
			if acc.thinking.Len() == 0 && acc.signature == "" {
				continue
			}
			blocks = append(blocks, llm.ContentBlock{
				Type:      llm.BlockThinking,
				Thinking:  acc.thinking.String(),
				Signature: acc.signature,
			})
		case llm.BlockToolUse:
			blocks = append(blocks, llm.ContentBlock{
				Type:      llm.BlockToolUse,
				ToolUseID: acc.toolID,
				ToolName:  acc.toolName,
				Input:     acc.input,
			})
		}
	}
	return blocks
}

// sortedBlockIndices 返回升序的块索引。
func sortedBlockIndices(accs map[int]*blockAcc) []int {
	indices := make([]int, 0, len(accs))
	for index := range accs {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	return indices
}

// closeToolBlock 解析累积的工具参数并发出 tool_call_ready。
func (t *turn) closeToolBlock(acc *blockAcc, messageID string) {
	if acc.closed {
		return
	}
	acc.closed = true
	raw := strings.TrimSpace(acc.partial.String())
	switch {
	case raw == "":
		acc.input = json.RawMessage("{}")
	case json.Valid([]byte(raw)):
		acc.input = json.RawMessage(append([]byte(nil), raw...))
	default:
		acc.input = nil
		t.emitAdapterError(fmt.Sprintf("invalid tool input json for %s", acc.toolName))
	}
	index, id, name, input := acc.index, acc.toolID, acc.toolName, acc.input
	t.emitEvent(EventToolCallReady, func(e *Event) {
		e.MessageID = messageID
		e.BlockIndex = index
		e.ToolUseID = id
		e.ToolName = name
		e.Input = input
	})
}

// toolCalls 按出现顺序把 assistant 消息里的 tool_use 转成待执行调用。
// Index 是整条回复中的全局位置，跨批次不复位。
func toolCalls(msg llm.Message) []tool.Call {
	var calls []tool.Call
	for _, b := range msg.Blocks {
		if b.Type != llm.BlockToolUse {
			continue
		}
		calls = append(calls, tool.Call{
			Index:     len(calls),
			ToolUseID: b.ToolUseID,
			Name:      b.ToolName,
			Input:     b.Input,
		})
	}
	return calls
}

// callPlan 是一个工具调用在权限预检后的执行计划。
type callPlan struct {
	tool      tool.Tool
	paths     []string
	readOnly  bool
	immediate *tool.Result
}

// precheck 在工具执行前完成未知工具、路径、参数与权限判定。
// allow 继续执行；deny 生成拒绝结果；ask 阻塞等待审批，不得默认放行。
func (t *turn) precheck(c tool.Call) (plan callPlan) {
	defer func() {
		if r := recover(); r != nil {
			res := errorResult("tool %s panicked during permission check: %v", c.Name, r)
			plan = callPlan{immediate: &res}
		}
	}()
	if t.ctx.Err() != nil {
		r := errorResult("canceled by user")
		plan.immediate = &r
		return plan
	}

	tl, ok := t.engine.cfg.Tools.Lookup(c.Name)
	if !ok {
		r := errorResult("unknown tool: %s", c.Name)
		plan.immediate = &r
		return plan
	}
	plan.tool = tl

	if pa, ok := tl.(tool.PathAware); ok {
		paths, err := pa.AffectedPaths(c.Input)
		if err != nil {
			r := errorResult("invalid path: %v", err)
			plan.immediate = &r
			return plan
		}
		plan.paths = paths
	}

	if !json.Valid(c.Input) {
		r := errorResult("invalid input json")
		plan.immediate = &r
		return plan
	}

	plan.readOnly = tl.ReadOnly(c.Input)
	noApproval := false
	if aa, ok := tl.(tool.AlwaysAllow); ok {
		noApproval = aa.AlwaysAllow()
	}
	req := permission.Request{
		SessionID:  t.engine.cfg.SessionID,
		ToolName:   c.Name,
		Input:      c.Input,
		ReadOnly:   plan.readOnly,
		WriteFiles: !plan.readOnly && len(plan.paths) > 0,
		NoApproval: noApproval,
		Paths:      plan.paths,
		WorkDir:    t.engine.cfg.WorkDir,
	}
	decision, err := t.engine.cfg.Permissions.Check(t.ctx, req)
	if err != nil {
		r := errorResult("permission check failed: %v", err)
		plan.immediate = &r
		return plan
	}
	switch decision.Effect {
	case permission.EffectAllow:
		return plan
	case permission.EffectDeny:
		r := errorResult("permission denied: %s", decision.Reason)
		plan.immediate = &r
		return plan
	default:
		allowed, reason := t.awaitApproval(c, tl, decision)
		if !allowed {
			r := errorResult("permission denied: %s", reason)
			plan.immediate = &r
		}
		return plan
	}
}

// awaitApproval 发出审批事件并在审批通道上等待 Decide；ctx 取消立即返回拒绝。
func (t *turn) awaitApproval(c tool.Call, tl tool.Tool, decision permission.Decision) (bool, string) {
	requestID := t.engine.cfg.NewID("perm")
	alwaysSafe := t.alwaysRuleSafe(c, tl)
	view := permission.ApprovalView{
		RequestID: requestID,
		SessionID: t.engine.cfg.SessionID,
		ToolName:  c.Name,
		Input:     c.Input,
		Reason:    decision.Reason,
		Options:   approvalOptions(alwaysSafe),
	}
	ch, cleanup := t.engine.cfg.Approvals.Prepare(view)
	defer cleanup()

	reqView := view
	t.emitEvent(EventPermissionRequest, func(ev *Event) { ev.Approval = &reqView })

	select {
	case resp, ok := <-ch:
		if !ok {
			t.emitPermissionResolved(requestID, "canceled")
			return false, "approval canceled"
		}
		t.emitPermissionResolved(requestID, resp.Option)
		switch resp.Option {
		case permission.OptionAllowOnce:
			return true, ""
		case permission.OptionAllowAlways:
			t.addAlwaysRule(c, tl, permission.EffectAllow)
			return true, ""
		case permission.OptionDenyAlways:
			t.addAlwaysRule(c, tl, permission.EffectDeny)
			return false, "denied by user"
		default:
			return false, "denied by user"
		}
	case <-t.ctx.Done():
		t.emitPermissionResolved(requestID, "canceled")
		return false, "canceled by user"
	}
}

// approvalOptions 返回审批选项。无法安全推导 always 规则时只给单次选项。
func approvalOptions(alwaysSafe bool) []permission.ApprovalOption {
	opts := []permission.ApprovalOption{{ID: permission.OptionAllowOnce, Label: "允许本次"}}
	if alwaysSafe {
		opts = append(opts, permission.ApprovalOption{ID: permission.OptionAllowAlways, Label: "总是允许"})
	}
	opts = append(opts, permission.ApprovalOption{ID: permission.OptionDenyOnce, Label: "拒绝本次"})
	if alwaysSafe {
		opts = append(opts, permission.ApprovalOption{ID: permission.OptionDenyAlways, Label: "总是拒绝"})
	}
	return opts
}

// alwaysRuleSafe 报告能否为该调用推导出安全的 always 规则。
// Bash 需要完整命令；文件工具的目标必须全部在工作目录内；其他工具整类放行。
func (t *turn) alwaysRuleSafe(c tool.Call, tl tool.Tool) bool {
	if c.Name == "Bash" {
		cmd, ok := commandOf(c.Input)
		return ok && cmd != ""
	}
	if _, ok := tl.(tool.PathAware); ok {
		paths, err := tl.(tool.PathAware).AffectedPaths(c.Input)
		if err != nil || len(paths) == 0 {
			return false
		}
		for _, p := range paths {
			if _, ok := relativeToWorkDir(t.engine.cfg.WorkDir, p); !ok {
				return false
			}
		}
		return true
	}
	return true
}

// addAlwaysRule 按选项推导并写入会话级规则。规则推导由 agent 负责，前端只传选项。
func (t *turn) addAlwaysRule(c tool.Call, tl tool.Tool, effect permission.Effect) {
	for _, rule := range t.rulesFor(c, tl, effect) {
		if _, err := t.engine.cfg.Permissions.AddRule(rule); err != nil {
			t.emitErrorView("permission", &ErrorView{Kind: "permission", Message: err.Error()})
		}
	}
}

// rulesFor 按工具类型推导 always 规则（§5.6）。
func (t *turn) rulesFor(c tool.Call, tl tool.Tool, effect permission.Effect) []permission.Rule {
	base := permission.Rule{Tool: c.Name, Effect: effect, Source: permission.SourceSession}
	if c.Name == "Bash" {
		if cmd, ok := commandOf(c.Input); ok && cmd != "" {
			rule := base
			rule.MatchMode = permission.MatchExact
			rule.Pattern = cmd
			return []permission.Rule{rule}
		}
		return nil
	}
	if pa, ok := tl.(tool.PathAware); ok {
		paths, err := pa.AffectedPaths(c.Input)
		if err != nil {
			return nil
		}
		rules := make([]permission.Rule, 0, len(paths))
		for _, p := range paths {
			rel, ok := relativeToWorkDir(t.engine.cfg.WorkDir, p)
			if !ok {
				return nil
			}
			rule := base
			rule.MatchMode = permission.MatchGlob
			rule.Pattern = rel
			rules = append(rules, rule)
		}
		return rules
	}
	rule := base
	rule.MatchMode = permission.MatchJSON
	rule.Pattern = ""
	return []permission.Rule{rule}
}

// commandOf 从工具输入中取出 command 字段。
func commandOf(input json.RawMessage) (string, bool) {
	if len(input) == 0 {
		return "", false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(input, &obj); err != nil {
		return "", false
	}
	raw, ok := obj["command"]
	if !ok {
		return "", false
	}
	var cmd string
	if err := json.Unmarshal(raw, &cmd); err != nil {
		return "", false
	}
	return cmd, true
}

// relativeToWorkDir 把绝对路径转成相对工作目录的斜杠路径。
func relativeToWorkDir(workDir, p string) (string, bool) {
	if !filepath.IsAbs(workDir) || !filepath.IsAbs(p) {
		return "", false
	}
	rel, err := filepath.Rel(filepath.Clean(workDir), filepath.Clean(p))
	if err != nil {
		return "", false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// toolDone 是一条工具执行完成的消息。
type toolDone struct {
	index  int
	result tool.Result
}

// progressMsg 是一条工具进度消息。
type progressMsg struct {
	index    int
	progress tool.Progress
}

// executeTools 执行本步全部 tool_use：只读并行（上限 MaxToolConcurrency），写工具串行。
// 每个 tool_use 恰好产生一个 tool_result，拒绝、异常、参数错误都转成结果。
func (t *turn) executeTools(calls []tool.Call) error {
	n := len(calls)
	results := make([]tool.Result, n)
	done := make([]bool, n)
	resolved := make([]tool.Tool, n)
	runnable := make([]bool, n)
	safe := make([]bool, n)

	for i, c := range calls {
		plan := t.precheck(c)
		resolved[i] = plan.tool
		if plan.immediate != nil {
			results[i] = *plan.immediate
			done[i] = true
			t.emitToolResult(c, results[i])
			continue
		}
		runnable[i] = true
		safe[i] = plan.tool.ConcurrencySafe(c.Input)
	}

	canceled := t.ctx.Err() != nil
	if !canceled {
		batches := groupBatches(calls, runnable, safe, t.engine.cfg.MaxToolConcurrency)
		doneCh := make(chan toolDone, n)
		progressCh := make(chan progressMsg, 64)

	outer:
		for _, batch := range batches {
			if batch.Concurrent {
				for _, c := range batch.Calls {
					t.startTool(c, resolved[c.Index], doneCh, progressCh)
				}
				for {
					pending := 0
					for _, c := range batch.Calls {
						if !done[c.Index] {
							pending++
						}
					}
					if pending == 0 {
						break
					}
					if t.drainTool(calls, results, done, doneCh, progressCh) {
						canceled = true
						break outer
					}
				}
				continue
			}
			for _, c := range batch.Calls {
				t.startTool(c, resolved[c.Index], doneCh, progressCh)
				for !done[c.Index] {
					if t.drainTool(calls, results, done, doneCh, progressCh) {
						canceled = true
						break outer
					}
				}
			}
		}
	}

	return t.finishToolExecution(calls, results, done, canceled)
}

// startTool 启动一个工具 goroutine。进度与结果都经 channel 汇入 turn goroutine，
// 因此事件只有一个发射者。doneCh 容量等于调用数，取消后的迟到写入不会阻塞。
func (t *turn) startTool(c tool.Call, tl tool.Tool, doneCh chan<- toolDone, progressCh chan<- progressMsg) {
	go func() {
		progress := func(p tool.Progress) {
			select {
			case progressCh <- progressMsg{index: c.Index, progress: p}:
			default:
			}
		}
		doneCh <- toolDone{index: c.Index, result: runTool(t.ctx, tl, c.Input, progress)}
	}()
}

// drainTool 处理一个就绪事件；返回 true 表示收到 ctx 取消。
func (t *turn) drainTool(calls []tool.Call, results []tool.Result, done []bool, doneCh <-chan toolDone, progressCh <-chan progressMsg) bool {
	select {
	case <-t.ctx.Done():
		return true
	case pm := <-progressCh:
		t.emitEvent(EventToolProgress, func(ev *Event) {
			ev.ToolUseID = calls[pm.index].ToolUseID
			ev.ToolName = calls[pm.index].Name
			ev.Text = pm.progress.Text
			ev.Step = t.step
		})
		return false
	case d := <-doneCh:
		results[d.index] = d.result
		done[d.index] = true
		t.emitToolResult(calls[d.index], d.result)
		return false
	}
}

// finishToolExecution 为未完成的调用补取消结果、落盘一条工具结果消息；
// 取消时随后结束本轮。
func (t *turn) finishToolExecution(calls []tool.Call, results []tool.Result, done []bool, canceled bool) error {
	for i, c := range calls {
		if done[i] {
			continue
		}
		results[i] = errorResult("canceled by user")
		done[i] = true
		t.emitToolResult(c, results[i])
	}

	msg := toolResultMessage(calls, results, t.engine.cfg.NewID("msg"), t.engine.now())
	if err := t.appendMessage(msg); err != nil {
		return err
	}
	t.appendHistory(msg)

	if canceled && !t.finished {
		t.end(StopCanceled, "")
	}
	return nil
}

// toolResultMessage 把全部结果装进一条 user 消息，顺序与 tool_use 一致。
func toolResultMessage(calls []tool.Call, results []tool.Result, id string, at time.Time) llm.Message {
	blocks := make([]llm.ContentBlock, len(calls))
	for i, c := range calls {
		text := results[i].Content
		if text == "" {
			text = emptyToolOutput
		}
		blocks[i] = llm.ContentBlock{
			Type:      llm.BlockToolResult,
			ToolUseID: c.ToolUseID,
			Text:      text,
			IsError:   results[i].IsError,
		}
	}
	return llm.Message{ID: id, Role: llm.RoleUser, Blocks: blocks, Time: at}
}

// runTool 执行一次工具调用并保证永不 panic：ctx 已取消不再执行；
// Run 的错误与 panic 都转成 IsError 结果。
func runTool(ctx context.Context, tl tool.Tool, input json.RawMessage, progress tool.ProgressFunc) (res tool.Result) {
	if err := ctx.Err(); err != nil {
		return errorResult("tool %s canceled", tl.Name())
	}
	defer func() {
		if r := recover(); r != nil {
			res = errorResult("tool %s panicked: %v", tl.Name(), r)
		}
	}()
	result, err := tl.Run(ctx, input, progress)
	if err != nil {
		return errorResult("tool %s: %v", tl.Name(), err)
	}
	return result
}

// errorResult 构造错误结果，格式串与 fmt.Errorf 相同。
func errorResult(format string, args ...any) tool.Result {
	return tool.Result{Content: fmt.Sprintf(format, args...), IsError: true}
}

// groupBatches 按「并发安全 + 可执行」切批：连续的安全调用合并成一批
// （每批不超过 limit），不安全调用各自成批并串行执行。
func groupBatches(calls []tool.Call, runnable, safe []bool, limit int) []tool.Batch {
	if limit <= 0 {
		limit = defaultMaxToolConcurrency
	}
	var batches []tool.Batch
	for i := 0; i < len(calls); i++ {
		if !runnable[i] {
			continue
		}
		if !safe[i] {
			batches = append(batches, tool.Batch{Calls: []tool.Call{calls[i]}})
			continue
		}
		batch := tool.Batch{Concurrent: true}
		j := i
		for ; j < len(calls) && len(batch.Calls) < limit; j++ {
			if !runnable[j] || !safe[j] {
				break
			}
			batch.Calls = append(batch.Calls, calls[j])
		}
		i = j - 1
		batches = append(batches, batch)
	}
	return batches
}

// appendRecord 在脱离取消的 persistCtx 下追加一条记录。
func (t *turn) appendRecord(rec session.Record) error {
	rec.SessionID = t.engine.cfg.SessionID
	if rec.Time.IsZero() {
		rec.Time = t.engine.now()
	}
	if err := t.engine.cfg.Store.Append(t.persistCtx, rec); err != nil {
		return fmt.Errorf("agent: append %s: %w", rec.Type, err)
	}
	return nil
}

// appendHistory 把一条消息追加到引擎内存历史。
func (t *turn) appendHistory(msg llm.Message) { t.engine.appendHistory(msg) }

// appendMessage 持久化一条消息；空消息不落盘。
func (t *turn) appendMessage(msg llm.Message) error {
	if len(msg.Blocks) == 0 {
		return nil
	}
	return t.appendRecord(session.Record{Type: session.RecordMessage, Message: &msg})
}

// appendTurnEnd 写入本轮结束记录。失败只上报，不覆盖已有的结束原因。
func (t *turn) appendTurnEnd() {
	if t.res.StopReason == "" {
		t.res.StopReason = StopError
	}
	rec := session.Record{
		Type: session.RecordTurnEnd,
		TurnEnd: &session.TurnEnd{
			TurnID:     t.in.TurnID,
			StopReason: string(t.res.StopReason),
			Error:      t.res.Err,
			Turns:      t.res.Turns,
		},
	}
	if err := t.appendRecord(rec); err != nil {
		t.emitErrorView("storage", &ErrorView{Kind: "storage", Message: err.Error()})
	}
}

// fail 记录内部故障：发出 error 事件并把本轮标记为 StopError。
func (t *turn) fail(kind string, err error) {
	t.emitErrorView(kind, &ErrorView{Kind: kind, Message: err.Error()})
	t.res.StopReason = StopError
	t.res.Err = err.Error()
}

// end 结束本轮状态；turn_end 事件由 finish 在记录落盘之后发出，
// 保证它是本轮最后一个事件。
func (t *turn) end(reason StopReason, errMsg string) {
	if t.finished {
		return
	}
	t.finished = true
	t.res.StopReason = reason
	if errMsg != "" && t.res.Err == "" {
		t.res.Err = errMsg
	}
}

// finish 保证任何路径（含 panic）都恰好发出一次 turn_end。
// 调用方必须先落盘 turn_end 记录，再调用本方法，这样存储错误不会排到 turn_end 之后。
func (t *turn) finish() {
	if t.res.StopReason == "" {
		if t.res.Err != "" {
			t.res.StopReason = StopError
		} else {
			t.res.StopReason = StopCompleted
		}
	}
	if t.turnEndSent {
		return
	}
	t.turnEndSent = true
	t.res.Usage = t.roundUsage
	usage := t.roundUsage
	contextTokens := t.contextTokens
	text := t.res.Text
	stopReason := t.res.StopReason
	t.emitEvent(EventTurnEnd, func(ev *Event) {
		ev.StopReason = stopReason
		ev.Usage = &usage
		ev.ContextTokens = contextTokens
		ev.Text = text
	})
}

// emitEvent 构造并发射一个事件；只在 turn goroutine 调用。
func (t *turn) emitEvent(typ EventType, mutate func(*Event)) {
	ev := Event{
		Type:      typ,
		SessionID: t.engine.cfg.SessionID,
		TurnID:    t.in.TurnID,
		Step:      t.step,
		TimeMS:    t.engine.now().UnixMilli(),
	}
	if mutate != nil {
		mutate(&ev)
	}
	t.emit(ev)
}

// emitToolResult 发出单个工具结束事件（完成即发，不整批聚集）。
func (t *turn) emitToolResult(c tool.Call, res tool.Result) {
	view := &ToolResultView{
		Preview:   toolPreview(res),
		IsError:   res.IsError,
		Truncated: res.Truncated,
		Bytes:     len(res.Content),
	}
	t.emitEvent(EventToolResult, func(ev *Event) {
		ev.ToolUseID = c.ToolUseID
		ev.ToolName = c.Name
		ev.Result = view
	})
}

// emitMessageDiscard 通知前端丢弃半截 assistant 消息。
func (t *turn) emitMessageDiscard(messageID string, cause error) {
	t.emitEvent(EventMessageDiscard, func(ev *Event) {
		ev.MessageID = messageID
		ev.Error = errorViewOf(cause)
	})
}

// emitPermissionResolved 通知前端审批已有结果。
func (t *turn) emitPermissionResolved(requestID, text string) {
	t.emitEvent(EventPermissionResolved, func(ev *Event) {
		ev.Approval = &permission.ApprovalView{RequestID: requestID}
		ev.Text = text
	})
}

// emitErrorView 发出可展示错误。
func (t *turn) emitErrorView(kind string, view *ErrorView) {
	if view == nil {
		view = &ErrorView{Kind: kind}
	}
	if view.Kind == "" {
		view.Kind = kind
	}
	t.emitEvent(EventError, func(ev *Event) { ev.Error = view })
}

// emitAdapterError 记录适配器违反块级事件契约的情况。
func (t *turn) emitAdapterError(message string) {
	t.emitEvent(EventError, func(ev *Event) {
		ev.Error = &ErrorView{Kind: "protocol", Message: message}
	})
}

// toolPreview 返回工具结果的前 2KB 预览（不切断 UTF-8）。
func toolPreview(res tool.Result) string {
	content := res.Display
	if content == "" {
		content = res.Content
	}
	if len(content) <= toolResultPreviewBytes {
		return content
	}
	cut := toolResultPreviewBytes
	for cut > 0 && !utf8.RuneStart(content[cut]) {
		cut--
	}
	return content[:cut]
}

// textOf 拼接消息中的文本块。
func textOf(msg llm.Message) string {
	var b strings.Builder
	for _, block := range msg.Blocks {
		if block.Type == llm.BlockText {
			b.WriteString(block.Text)
		}
	}
	return b.String()
}

// errorViewOf 把错误转换成前端视图；*llm.StreamError 保留分类与状态码。
func errorViewOf(err error) *ErrorView {
	if err == nil {
		return nil
	}
	var se *llm.StreamError
	if errors.As(err, &se) && se != nil {
		return &ErrorView{Kind: string(se.Kind), Message: se.Message, Status: se.Status}
	}
	return &ErrorView{Kind: "unknown", Message: err.Error()}
}

// isCanceled 报告错误是否来自调用方取消。
func isCanceled(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
