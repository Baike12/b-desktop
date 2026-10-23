package permission

import (
	"context"
	"fmt"
	"sync"
)

// ParseMode 解析权限模式字符串；未知值返回错误。
func ParseMode(s string) (Mode, error) {
	switch m := Mode(s); m {
	case ModeDefault, ModeAcceptEdits, ModePlan, ModeBypassPermissions, ModeDontAsk:
		return m, nil
	}
	return "", fmt.Errorf("permission: unknown mode %q", s)
}

// Allowed 报告决策是否为 allow。
func (d Decision) Allowed() bool {
	return d.Effect == EffectAllow
}

// Config 是 Engine 的构造参数。
type Config struct {
	// Mode 是初始权限模式，必填且必须是五档之一。
	Mode Mode
	// WorkDir 是默认工作目录（绝对路径）。
	WorkDir string
	// Rules 是初始规则；规则为空 ID 时由引擎按加入顺序分配。
	Rules []Rule
}

// Engine 持有会话级规则与模式，所有方法并发安全。
type Engine struct {
	mu      sync.RWMutex
	mode    Mode
	workDir string
	rules   []Rule
	seq     uint64
}

// NewEngine 创建引擎。Mode 非法、规则非法（MatchMode 为空、MatchGlob 无 Pattern、
// Effect 未知、工具名为空、规则 ID 重复）时返回错误。
func NewEngine(cfg Config) (*Engine, error) {
	mode, err := ParseMode(string(cfg.Mode))
	if err != nil {
		return nil, err
	}
	e := &Engine{mode: mode, workDir: cfg.WorkDir}
	seen := make(map[string]struct{}, len(cfg.Rules))
	for i, r := range cfg.Rules {
		prepared, err := e.prepareRule(r)
		if err != nil {
			return nil, fmt.Errorf("permission: rule %d: %w", i, err)
		}
		if _, dup := seen[prepared.ID]; dup {
			return nil, fmt.Errorf("permission: rule %d: %w: duplicate id %q", i, ErrRuleInvalid, prepared.ID)
		}
		seen[prepared.ID] = struct{}{}
		e.rules = append(e.rules, prepared)
	}
	return e, nil
}

// prepareRule 校验规则并在 ID 为空时分配 ID。调用方必须持有写锁（构造期除外）。
func (e *Engine) prepareRule(r Rule) (Rule, error) {
	if err := validateRule(r); err != nil {
		return Rule{}, err
	}
	if r.ID == "" {
		e.seq++
		r.ID = fmt.Sprintf("rule_%d", e.seq)
	}
	return r, nil
}

// validateRule 校验一条规则。任何未知枚举都按 ErrRuleInvalid 拒绝。
func validateRule(r Rule) error {
	if r.Tool == "" {
		return fmt.Errorf("%w: empty tool", ErrRuleInvalid)
	}
	switch r.Effect {
	case EffectAllow, EffectAsk, EffectDeny:
	default:
		return fmt.Errorf("%w: unknown effect %q", ErrRuleInvalid, r.Effect)
	}
	switch r.MatchMode {
	case MatchExact, MatchPrefix, MatchJSON:
		return nil
	case MatchGlob:
		if r.Pattern == "" {
			return fmt.Errorf("%w: glob rule requires a pattern", ErrRuleInvalid)
		}
		if err := validateGlobPattern(r.Pattern); err != nil {
			return fmt.Errorf("%w: %v", ErrRuleInvalid, err)
		}
		return nil
	default:
		return fmt.Errorf("%w: unknown match mode %q", ErrRuleInvalid, r.MatchMode)
	}
}

// Mode 返回当前模式。
func (e *Engine) Mode() Mode {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.mode
}

// SetMode 切换模式；未知模式返回错误且保持原模式不变。
func (e *Engine) SetMode(m Mode) error {
	parsed, err := ParseMode(string(m))
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.mode = parsed
	return nil
}

// Rules 返回规则快照；修改返回值不影响引擎。
func (e *Engine) Rules() []Rule {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Rule, len(e.rules))
	copy(out, e.rules)
	return out
}

// AddRule 追加规则；ID 为空时分配 ID。规则非法或 ID 重复时返回 ErrRuleInvalid。
func (e *Engine) AddRule(r Rule) (Rule, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	prepared, err := e.prepareRule(r)
	if err != nil {
		return Rule{}, err
	}
	for _, existing := range e.rules {
		if existing.ID == prepared.ID {
			return Rule{}, fmt.Errorf("%w: duplicate id %q", ErrRuleInvalid, prepared.ID)
		}
	}
	e.rules = append(e.rules, prepared)
	return prepared, nil
}

// RemoveRule 删除指定 ID 的规则，返回是否命中。
func (e *Engine) RemoveRule(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, r := range e.rules {
		if r.ID == id {
			e.rules = append(e.rules[:i], e.rules[i+1:]...)
			return true
		}
	}
	return false
}

// Check 按固定优先级判定一次工具调用：内置硬拒绝 → deny 规则 → NoApproval →
// plan → ask 规则 → acceptEdits → bypassPermissions → allow 规则 → 模式默认 →
// dontAsk 收口。它不做阻塞等待，也不读取或修改审批状态。
//
// 返回 error 仅保留给未来可能的引擎级故障；当前任何输入都返回三值之一。
// ctx 已取消时 fail-closed 返回 deny，绝不返回 ask 之外的放行。
func (e *Engine) Check(ctx context.Context, req Request) (Decision, error) {
	if err := ctx.Err(); err != nil {
		return Decision{Effect: EffectDeny, Reason: "request canceled"}, nil
	}
	e.mu.RLock()
	mode := e.mode
	workDir := e.workDir
	rules := make([]Rule, len(e.rules))
	copy(rules, e.rules)
	e.mu.RUnlock()

	if req.WorkDir != "" {
		workDir = req.WorkDir
	}
	return decide(mode, workDir, rules, req), nil
}

// decide 是无锁的纯判定函数，保持 §5.2 的固定顺序。
func decide(mode Mode, workDir string, rules []Rule, req Request) Decision {
	if id, ok := builtinDeny(req.Input); ok {
		return Decision{Effect: EffectDeny, Reason: "blocked by built-in safety rule " + id, RuleID: id}
	}
	for _, r := range rules {
		if r.Effect == EffectDeny && matchRule(r, req, workDir) == matchYes {
			return ruleDecision(EffectDeny, "denied by", r)
		}
	}
	if req.NoApproval {
		return Decision{Effect: EffectAllow, Reason: "tool declares no approval required"}
	}
	if mode == ModePlan {
		if req.ReadOnly {
			return Decision{Effect: EffectAllow, Reason: "plan mode allows read-only tools"}
		}
		return Decision{Effect: EffectDeny, Reason: "plan mode blocks mutations"}
	}
	for _, r := range rules {
		if r.Effect == EffectAsk && matchRule(r, req, workDir) == matchYes {
			return collapseAsk(mode, ruleDecision(EffectAsk, "approval required by", r))
		}
	}
	if mode == ModeAcceptEdits && req.WriteFiles && pathsWithin(workDir, req.Paths) {
		return Decision{Effect: EffectAllow, Reason: "acceptEdits allows writes inside the working directory"}
	}
	if mode == ModeBypassPermissions {
		return Decision{Effect: EffectAllow, Reason: "bypassPermissions mode allows the call"}
	}
	if r, ok := decideAllow(rules, req, workDir); ok {
		return ruleDecision(EffectAllow, "allowed by", r)
	}
	d := Decision{Effect: EffectAsk, Reason: "approval required"}
	if req.ReadOnly {
		d = Decision{Effect: EffectAllow, Reason: "read-only calls are allowed"}
	}
	return collapseAsk(mode, d)
}

// collapseAsk 在 dontAsk 模式下把任何 ask 收口为 deny。
// 第 5 步的 ask 规则与第 9 步的模式默认都经过它，因此 dontAsk 永不返回 ask。
func collapseAsk(mode Mode, d Decision) Decision {
	if mode == ModeDontAsk && d.Effect == EffectAsk {
		return Decision{Effect: EffectDeny, Reason: "dontAsk mode converts ask to deny"}
	}
	return d
}

// decideAllow 实现「全部匹配才算 allow、任一无法判定则不放行」。
func decideAllow(rules []Rule, req Request, workDir string) (Rule, bool) {
	var hit Rule
	found := false
	undecidable := false
	for _, r := range rules {
		if r.Effect != EffectAllow || !toolMatches(r, req.ToolName) {
			continue
		}
		switch matchRule(r, req, workDir) {
		case matchUndecidable:
			undecidable = true
		case matchYes:
			if !found {
				hit, found = r, true
			}
		}
	}
	if undecidable {
		return Rule{}, false
	}
	return hit, found
}

// ruleDecision 构造带命中规则说明的决策。
func ruleDecision(effect Effect, verb string, r Rule) Decision {
	reason := verb + " rule " + r.ID
	if r.Note != "" {
		reason += ": " + r.Note
	}
	return Decision{
		Effect:         effect,
		Reason:         reason,
		RuleID:         r.ID,
		MatchedPattern: r.Pattern,
	}
}
