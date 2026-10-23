package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Baike12/b-desktop/internal/permission"
	"github.com/Baike12/b-desktop/internal/session"
)

// 权限规则绑定层的错误哨兵。
var (
	// ErrInvalidRuleInput 表示规则入参非法：工具名为空，或 MatchMode / Effect / Source 不在允许集合内。
	ErrInvalidRuleInput = errors.New("app: invalid permission rule input")
	// ErrRuleNotFound 表示要删除的权限规则在该会话中不存在。
	ErrRuleNotFound = errors.New("app: permission rule not found")
)

// RuleDTO 是一条权限规则的前端视图。字段与 permission.Rule 一一对应，
// 不暴露内部类型，前端据此渲染规则列表。
type RuleDTO struct {
	ID        string `json:"id"`
	Tool      string `json:"tool"`
	Pattern   string `json:"pattern,omitempty"`
	MatchMode string `json:"matchMode"`
	Effect    string `json:"effect"`
	Source    string `json:"source"`
	Note      string `json:"note,omitempty"`
}

// RuleInput 是新增权限规则的入参。
// ID 可省略，由权限引擎按加入顺序分配；Source 省略时按会话级临时授权（session）写入。
type RuleInput struct {
	ID        string `json:"id,omitempty"`
	Tool      string `json:"tool"`
	Pattern   string `json:"pattern,omitempty"`
	MatchMode string `json:"matchMode"`
	Effect    string `json:"effect"`
	Source    string `json:"source,omitempty"`
	Note      string `json:"note,omitempty"`
}

// toRule 校验入参并转换成权限规则。
// 枚举值必须显式给出：空 MatchMode 或空 Effect 一律拒绝，避免把笔误当成默认放行/默认拒绝。
func (in RuleInput) toRule() (permission.Rule, error) {
	rule := permission.Rule{
		ID:      strings.TrimSpace(in.ID),
		Tool:    strings.TrimSpace(in.Tool),
		Pattern: in.Pattern,
		Note:    in.Note,
	}
	if rule.Tool == "" {
		return permission.Rule{}, fmt.Errorf("%w: tool is required", ErrInvalidRuleInput)
	}

	switch mode := permission.MatchMode(strings.TrimSpace(in.MatchMode)); mode {
	case permission.MatchExact, permission.MatchPrefix, permission.MatchGlob, permission.MatchJSON:
		rule.MatchMode = mode
	default:
		return permission.Rule{}, fmt.Errorf("%w: unknown match mode %q", ErrInvalidRuleInput, in.MatchMode)
	}

	switch effect := permission.Effect(strings.TrimSpace(in.Effect)); effect {
	case permission.EffectAllow, permission.EffectAsk, permission.EffectDeny:
		rule.Effect = effect
	default:
		return permission.Rule{}, fmt.Errorf("%w: unknown effect %q", ErrInvalidRuleInput, in.Effect)
	}

	switch source := permission.Source(strings.TrimSpace(in.Source)); source {
	case "":
		rule.Source = permission.SourceSession
	case permission.SourceBuiltin, permission.SourceUser, permission.SourceProject, permission.SourceSession:
		rule.Source = source
	default:
		return permission.Rule{}, fmt.Errorf("%w: unknown source %q", ErrInvalidRuleInput, in.Source)
	}
	return rule, nil
}

// toRuleDTO 把权限规则转换成前端视图。
func toRuleDTO(rule permission.Rule) RuleDTO {
	return RuleDTO{
		ID:        rule.ID,
		Tool:      rule.Tool,
		Pattern:   rule.Pattern,
		MatchMode: string(rule.MatchMode),
		Effect:    string(rule.Effect),
		Source:    string(rule.Source),
		Note:      rule.Note,
	}
}

// ListPermissionRules 返回会话当前生效的权限规则（按加入顺序）。
// 内置与 always allow/deny 写入的规则同样会出现；无规则时返回空切片而不是 nil。
func (a *App) ListPermissionRules(sessionID string) ([]RuleDTO, error) {
	if err := session.ValidateID(sessionID); err != nil {
		return nil, fmt.Errorf("app: list permission rules: %w", err)
	}
	engine, err := a.rulesEngine(a.lifeCtx(), sessionID)
	if err != nil {
		return nil, err
	}
	rules := engine.Rules()
	out := make([]RuleDTO, 0, len(rules))
	for _, rule := range rules {
		out = append(out, toRuleDTO(rule))
	}
	return out, nil
}

// AddPermissionRule 为会话追加一条权限规则，返回引擎分配的完整规则。
// 规则写入后立即对后续工具调用生效；ID 重复、glob 非法等由权限引擎拒绝，
// 错误包含 permission.ErrRuleInvalid，前端可用 errors.Is 判断。
func (a *App) AddPermissionRule(sessionID string, in RuleInput) (RuleDTO, error) {
	if err := session.ValidateID(sessionID); err != nil {
		return RuleDTO{}, fmt.Errorf("app: add permission rule: %w", err)
	}
	rule, err := in.toRule()
	if err != nil {
		return RuleDTO{}, fmt.Errorf("app: add permission rule: %w", err)
	}
	engine, err := a.rulesEngine(a.lifeCtx(), sessionID)
	if err != nil {
		return RuleDTO{}, err
	}
	added, err := engine.AddRule(rule)
	if err != nil {
		return RuleDTO{}, fmt.Errorf("app: add permission rule: %w", err)
	}
	return toRuleDTO(added), nil
}

// RemovePermissionRule 删除会话中指定 ID 的权限规则，立即生效。
// 规则不存在时返回 ErrRuleNotFound；空 ID 同样按不存在处理。
func (a *App) RemovePermissionRule(sessionID, ruleID string) error {
	if err := session.ValidateID(sessionID); err != nil {
		return fmt.Errorf("app: remove permission rule: %w", err)
	}
	id := strings.TrimSpace(ruleID)
	if id == "" {
		return fmt.Errorf("app: remove permission rule: %w", ErrRuleNotFound)
	}
	engine, err := a.rulesEngine(a.lifeCtx(), sessionID)
	if err != nil {
		return err
	}
	if !engine.RemoveRule(id) {
		return fmt.Errorf("app: remove permission rule: %w", ErrRuleNotFound)
	}
	return nil
}

// rulesEngine 取得会话正在使用的权限引擎，必要时先按当前配置构建会话引擎。
//
// 调用方必须已经完成 sessionID 校验。引擎构建与 SendMessage 共用 ensureEngineLocked，
// 因此规则改的是同一份内存状态：下一次工具调用判定直接看到新规则。
// 轮次运行期间不重建引擎（会关闭正在使用的实例），此时沿用已有引擎；
// 还没有引擎却已在运行时返回 ErrTurnRunning。
func (a *App) rulesEngine(ctx context.Context, sessionID string) (*permission.Engine, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("app: permission rules: %w", err)
	}
	rt, err := a.runtimeForSend(sessionID)
	if err != nil {
		return nil, err
	}
	cfg, gen := a.configSnapshot()

	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.closed {
		return nil, ErrSessionClosed
	}
	if rt.engine != nil && rt.engineGen == gen {
		return rt.engine.Permissions(), nil
	}
	if rt.running {
		if rt.engine != nil {
			return rt.engine.Permissions(), nil
		}
		return nil, fmt.Errorf("app: permission rules: %w", ErrTurnRunning)
	}
	engine, err := a.ensureEngineLocked(rt, cfg, gen)
	if err != nil {
		return nil, err
	}
	return engine.Permissions(), nil
}
