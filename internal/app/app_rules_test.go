package app

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Baike12/b-desktop/internal/llm"
	"github.com/Baike12/b-desktop/internal/permission"
	"github.com/Baike12/b-desktop/internal/session"
)

// rulesStubProvider 是权限规则测试用的空 Provider：引擎构建只要求 Provider 非 nil，
// 这些用例不发起模型请求。
type rulesStubProvider struct{}

// Name 实现 llm.Provider。
func (rulesStubProvider) Name() string { return "stub" }

// Stream 实现 llm.Provider；测试不会调用。
func (rulesStubProvider) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, errors.New("stub provider does not stream")
}

// rulesTestConfig 返回隔离的测试配置，权限模式由用例指定。
func rulesTestConfig(t *testing.T, mode permission.Mode) Config {
	t.Helper()
	dir := t.TempDir()
	return Config{
		Provider:           "anthropic",
		APIKey:             "sk-test-1234",
		Model:              "test-model",
		PermissionMode:     string(mode),
		WorkDir:            dir,
		DataDir:            filepath.Join(dir, "data"),
		ContextWindow:      200000,
		MaxOutputTokens:    4096,
		MaxTurns:           10,
		MaxToolConcurrency: 4,
	}
}

// newRulesTestApp 创建不启动 Wails 的应用实例。
func newRulesTestApp(t *testing.T, mode permission.Mode) *App {
	t.Helper()
	factory := func(Config) (llm.Provider, error) { return rulesStubProvider{}, nil }
	app, err := NewWithDeps(rulesTestConfig(t, mode), func(context.Context, string, any) {}, session.NewMemStore(), factory)
	if err != nil {
		t.Fatalf("NewWithDeps: %v", err)
	}
	return app
}

// createRulesSession 创建一个规则测试会话。
func createRulesSession(t *testing.T, app *App) SessionInfoDTO {
	t.Helper()
	info, err := app.CreateSession(CreateSessionInput{Title: "规则测试"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return info
}

// rulesLiveEngine 返回会话当前使用的权限引擎。
func rulesLiveEngine(t *testing.T, app *App, sessionID string) *permission.Engine {
	t.Helper()
	rt, err := app.runtimeOf(sessionID)
	if err != nil {
		t.Fatalf("runtimeOf: %v", err)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.engine == nil {
		t.Fatal("engine not built")
	}
	return rt.engine.Permissions()
}

func TestPermissionRulesAddListRemove(t *testing.T) {
	app := newRulesTestApp(t, permission.ModeDefault)
	info := createRulesSession(t, app)

	rules, err := app.ListPermissionRules(info.ID)
	if err != nil {
		t.Fatalf("ListPermissionRules: %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("初始规则应为空: %+v", rules)
	}

	added, err := app.AddPermissionRule(info.ID, RuleInput{
		Tool:      " Bash ",
		Pattern:   "go test ./...",
		MatchMode: "exact",
		Effect:    "allow",
		Note:      "允许运行测试",
	})
	if err != nil {
		t.Fatalf("AddPermissionRule: %v", err)
	}
	if added.ID == "" {
		t.Fatal("引擎应分配规则 ID")
	}
	if added.Tool != "Bash" {
		t.Fatalf("Tool = %q, want Bash（应去掉首尾空白）", added.Tool)
	}
	if added.Source != string(permission.SourceSession) {
		t.Fatalf("Source = %q, want session（省略时默认会话级）", added.Source)
	}
	if added.MatchMode != "exact" || added.Effect != "allow" || added.Note != "允许运行测试" {
		t.Fatalf("规则字段未原样保留: %+v", added)
	}

	rules, err = app.ListPermissionRules(info.ID)
	if err != nil {
		t.Fatalf("ListPermissionRules: %v", err)
	}
	if len(rules) != 1 || rules[0] != added {
		t.Fatalf("列表 = %+v, want 仅 %+v", rules, added)
	}

	if err := app.RemovePermissionRule(info.ID, added.ID); err != nil {
		t.Fatalf("RemovePermissionRule: %v", err)
	}
	rules, err = app.ListPermissionRules(info.ID)
	if err != nil {
		t.Fatalf("ListPermissionRules: %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("删除后规则应为空: %+v", rules)
	}

	if err := app.RemovePermissionRule(info.ID, added.ID); !errors.Is(err, ErrRuleNotFound) {
		t.Fatalf("重复删除 = %v, want ErrRuleNotFound", err)
	}
	if err := app.RemovePermissionRule(info.ID, "  "); !errors.Is(err, ErrRuleNotFound) {
		t.Fatalf("空 ID = %v, want ErrRuleNotFound", err)
	}
}

func TestPermissionRulesValidation(t *testing.T) {
	app := newRulesTestApp(t, permission.ModeDefault)
	info := createRulesSession(t, app)

	cases := []struct {
		name string
		in   RuleInput
		want error
	}{
		{name: "空工具名", in: RuleInput{MatchMode: "exact", Effect: "allow"}, want: ErrInvalidRuleInput},
		{name: "空匹配模式", in: RuleInput{Tool: "Bash", Effect: "allow"}, want: ErrInvalidRuleInput},
		{name: "未知匹配模式", in: RuleInput{Tool: "Bash", MatchMode: "regexp", Effect: "allow"}, want: ErrInvalidRuleInput},
		{name: "空效果", in: RuleInput{Tool: "Bash", MatchMode: "exact"}, want: ErrInvalidRuleInput},
		{name: "未知效果", in: RuleInput{Tool: "Bash", MatchMode: "exact", Effect: "permit"}, want: ErrInvalidRuleInput},
		{name: "未知来源", in: RuleInput{Tool: "Bash", MatchMode: "exact", Effect: "allow", Source: "plugin"}, want: ErrInvalidRuleInput},
		{name: "glob 缺 pattern", in: RuleInput{Tool: "Read", MatchMode: "glob", Effect: "allow"}, want: permission.ErrRuleInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := app.AddPermissionRule(info.ID, tc.in); !errors.Is(err, tc.want) {
				t.Fatalf("AddPermissionRule = %v, want %v", err, tc.want)
			}
		})
	}

	rules, err := app.ListPermissionRules(info.ID)
	if err != nil {
		t.Fatalf("ListPermissionRules: %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("非法入参不应写入规则: %+v", rules)
	}

	first, err := app.AddPermissionRule(info.ID, RuleInput{
		ID: "dup", Tool: "Bash", MatchMode: "exact", Effect: "allow",
	})
	if err != nil {
		t.Fatalf("AddPermissionRule: %v", err)
	}
	if _, err := app.AddPermissionRule(info.ID, RuleInput{
		ID: first.ID, Tool: "Read", MatchMode: "exact", Effect: "deny",
	}); !errors.Is(err, permission.ErrRuleInvalid) {
		t.Fatalf("重复 ID = %v, want permission.ErrRuleInvalid", err)
	}

	if _, err := app.ListPermissionRules("bad id"); !errors.Is(err, session.ErrInvalidID) {
		t.Fatalf("非法会话 ID = %v, want session.ErrInvalidID", err)
	}
	if _, err := app.ListPermissionRules("s_missing"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("不存在的会话 = %v, want ErrSessionNotFound", err)
	}
	if _, err := app.AddPermissionRule("s_missing", RuleInput{
		Tool: "Bash", MatchMode: "exact", Effect: "allow",
	}); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("不存在的会话上新增 = %v, want ErrSessionNotFound", err)
	}
	if err := app.RemovePermissionRule("s_missing", "rule_1"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("不存在的会话上删除 = %v, want ErrSessionNotFound", err)
	}
}

func TestPermissionRuleTakesEffectImmediately(t *testing.T) {
	app := newRulesTestApp(t, permission.ModeDefault)
	info := createRulesSession(t, app)
	// ListPermissionRules 会按需构建会话引擎，之后的判定都基于同一实例。
	if _, err := app.ListPermissionRules(info.ID); err != nil {
		t.Fatalf("ListPermissionRules: %v", err)
	}
	engine := rulesLiveEngine(t, app, info.ID)
	ctx := context.Background()
	req := permission.Request{
		SessionID: info.ID,
		ToolName:  "Bash",
		Input:     json.RawMessage(`{"command":"echo hi"}`),
	}

	before, err := engine.Check(ctx, req)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if before.Effect != permission.EffectAsk {
		t.Fatalf("默认模式下未匹配规则应 ask, got %+v", before)
	}

	added, err := app.AddPermissionRule(info.ID, RuleInput{
		Tool: "Bash", Pattern: "echo hi", MatchMode: "exact", Effect: "allow",
	})
	if err != nil {
		t.Fatalf("AddPermissionRule: %v", err)
	}
	after, err := engine.Check(ctx, req)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if after.Effect != permission.EffectAllow || after.RuleID != added.ID {
		t.Fatalf("新增规则后应立即 allow, got %+v", after)
	}

	if err := app.RemovePermissionRule(info.ID, added.ID); err != nil {
		t.Fatalf("RemovePermissionRule: %v", err)
	}
	restored, err := engine.Check(ctx, req)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if restored.Effect != permission.EffectAsk {
		t.Fatalf("删除规则后应回到 ask, got %+v", restored)
	}
}
