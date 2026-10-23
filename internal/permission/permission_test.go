package permission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// mustWorkDir 返回解析符号链接后的临时工作目录，与 agent 填 Request.WorkDir 的约定一致。
func mustWorkDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", dir, err)
	}
	return dir
}

// readRequest 构造只读调用（Read 读工作目录内文件）。
func readRequest(wd string) Request {
	return Request{
		SessionID: "sess",
		ToolName:  "Read",
		ReadOnly:  true,
		Paths:     []string{filepath.Join(wd, "main.go")},
		WorkDir:   wd,
	}
}

// writeRequest 构造写工作目录内文件的调用。
func writeRequest(wd, name string) Request {
	return Request{
		SessionID:  "sess",
		ToolName:   "Write",
		WriteFiles: true,
		Paths:      []string{filepath.Join(wd, name)},
		WorkDir:    wd,
	}
}

// bashRequest 构造 Bash 调用。
func bashRequest(cmd string) Request {
	input, err := json.Marshal(map[string]string{"command": cmd})
	if err != nil {
		panic(err)
	}
	return Request{SessionID: "sess", ToolName: "Bash", Input: input}
}

// newTestEngine 创建引擎，失败即终止测试。
func newTestEngine(t *testing.T, mode Mode, wd string, rules ...Rule) *Engine {
	t.Helper()
	e, err := NewEngine(Config{Mode: mode, WorkDir: wd, Rules: rules})
	if err != nil {
		t.Fatalf("NewEngine(mode=%s): %v", mode, err)
	}
	return e
}

// checkDecision 执行 Check 并校验三值与原因非空。
func checkDecision(t *testing.T, e *Engine, req Request) Decision {
	t.Helper()
	d, err := e.Check(context.Background(), req)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	switch d.Effect {
	case EffectAllow, EffectAsk, EffectDeny:
	default:
		t.Fatalf("Check() effect = %q, want allow/ask/deny", d.Effect)
	}
	if d.Reason == "" {
		t.Fatalf("Check() returned empty reason: %+v", d)
	}
	return d
}

// TestCheckModes 覆盖五档模式对只读、目录内写、目录外写、Bash、NoApproval 的判定。
func TestCheckModes(t *testing.T) {
	wd := mustWorkDir(t)
	outside := filepath.Join(filepath.Dir(wd), "outside.go")
	outsideWrite := Request{
		SessionID:  "sess",
		ToolName:   "Write",
		WriteFiles: true,
		Paths:      []string{outside},
		WorkDir:    wd,
	}
	noApproval := Request{
		SessionID:  "sess",
		ToolName:   "TodoWrite",
		NoApproval: true,
		WriteFiles: true,
		Paths:      []string{filepath.Join(wd, "todo.json")},
		WorkDir:    wd,
	}

	cases := []struct {
		name string
		mode Mode
		req  Request
		want Effect
	}{
		{"default read-only", ModeDefault, readRequest(wd), EffectAllow},
		{"default write inside", ModeDefault, writeRequest(wd, "a.go"), EffectAsk},
		{"default write outside", ModeDefault, outsideWrite, EffectAsk},
		{"default bash", ModeDefault, bashRequest("go test ./..."), EffectAsk},
		{"acceptEdits read-only", ModeAcceptEdits, readRequest(wd), EffectAllow},
		{"acceptEdits write inside", ModeAcceptEdits, writeRequest(wd, "a.go"), EffectAllow},
		{"acceptEdits write outside", ModeAcceptEdits, outsideWrite, EffectAsk},
		{"acceptEdits bash", ModeAcceptEdits, bashRequest("go test ./..."), EffectAsk},
		{"plan read-only", ModePlan, readRequest(wd), EffectAllow},
		{"plan write inside", ModePlan, writeRequest(wd, "a.go"), EffectDeny},
		{"plan bash", ModePlan, bashRequest("go test ./..."), EffectDeny},
		{"plan no-approval tool", ModePlan, noApproval, EffectAllow},
		{"bypass write", ModeBypassPermissions, writeRequest(wd, "a.go"), EffectAllow},
		{"bypass bash", ModeBypassPermissions, bashRequest("rm -rf ./build"), EffectAllow},
		{"dontAsk read-only", ModeDontAsk, readRequest(wd), EffectAllow},
		{"dontAsk write", ModeDontAsk, writeRequest(wd, "a.go"), EffectDeny},
		{"dontAsk bash", ModeDontAsk, bashRequest("go test ./..."), EffectDeny},
		{"dontAsk no-approval tool", ModeDontAsk, noApproval, EffectAllow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEngine(t, tc.mode, wd)
			d := checkDecision(t, e, tc.req)
			if d.Effect != tc.want {
				t.Fatalf("effect = %q, want %q (reason=%q)", d.Effect, tc.want, d.Reason)
			}
			if tc.mode == ModeDontAsk && d.Effect == EffectAsk {
				t.Fatalf("dontAsk returned ask: %+v", d)
			}
		})
	}
}

// TestCheckRulePriority 覆盖 deny > ask > mode > allow 的优先级与各类匹配。
func TestCheckRulePriority(t *testing.T) {
	wd := mustWorkDir(t)
	outside := filepath.Join(filepath.Dir(wd), "outside.go")

	allowEcho := Rule{ID: "allow-bash", Tool: "Bash", Pattern: "echo hi", MatchMode: MatchExact, Effect: EffectAllow, Source: SourceSession}
	denyEcho := Rule{ID: "deny-bash", Tool: "Bash", Pattern: "echo hi", MatchMode: MatchExact, Effect: EffectDeny, Source: SourceSession}
	askEcho := Rule{ID: "ask-bash", Tool: "Bash", Pattern: "echo hi", MatchMode: MatchExact, Effect: EffectAsk, Source: SourceSession}
	allowWrite := Rule{ID: "allow-write", Tool: "Write", MatchMode: MatchJSON, Effect: EffectAllow, Source: SourceSession}

	cases := []struct {
		name       string
		mode       Mode
		rules      []Rule
		req        Request
		want       Effect
		wantRuleID string
	}{
		{
			name:  "deny beats allow",
			mode:  ModeDefault,
			rules: []Rule{allowEcho, denyEcho},
			req:   bashRequest("echo hi"),
			want:  EffectDeny, wantRuleID: "deny-bash",
		},
		{
			name:  "wildcard deny matches every tool",
			mode:  ModeDefault,
			rules: []Rule{{ID: "deny-all", Tool: "*", MatchMode: MatchJSON, Effect: EffectDeny, Source: SourceBuiltin}},
			req:   bashRequest("echo hi"),
			want:  EffectDeny, wantRuleID: "deny-all",
		},
		{
			name:  "ask rule beats allow rule",
			mode:  ModeDefault,
			rules: []Rule{allowEcho, askEcho},
			req:   bashRequest("echo hi"),
			want:  EffectAsk, wantRuleID: "ask-bash",
		},
		{
			name:  "plan blocks mutation despite allow rule",
			mode:  ModePlan,
			rules: []Rule{allowWrite},
			req:   writeRequest(wd, "a.go"),
			want:  EffectDeny,
		},
		{
			name:  "allow rule exact command",
			mode:  ModeDefault,
			rules: []Rule{allowEcho},
			req:   bashRequest("echo hi"),
			want:  EffectAllow, wantRuleID: "allow-bash",
		},
		{
			name:  "allow rule exact mismatch falls through",
			mode:  ModeDefault,
			rules: []Rule{allowEcho},
			req:   bashRequest("echo bye"),
			want:  EffectAsk,
		},
		{
			name:  "prefix rule allows simple command",
			mode:  ModeDefault,
			rules: []Rule{{ID: "prefix-npm", Tool: "Bash", Pattern: "npm test", MatchMode: MatchPrefix, Effect: EffectAllow, Source: SourceSession}},
			req:   bashRequest("npm test --watch"),
			want:  EffectAllow, wantRuleID: "prefix-npm",
		},
		{
			name:  "prefix rule refuses composite command",
			mode:  ModeDefault,
			rules: []Rule{{ID: "prefix-npm", Tool: "Bash", Pattern: "npm test", MatchMode: MatchPrefix, Effect: EffectAllow, Source: SourceSession}},
			req:   bashRequest("npm test && rm -rf x"),
			want:  EffectAsk,
		},
		{
			name:  "glob rule allows all paths inside workdir",
			mode:  ModeDefault,
			rules: []Rule{{ID: "glob-src", Tool: "Write", Pattern: "src/**/*.go", MatchMode: MatchGlob, Effect: EffectAllow, Source: SourceSession}},
			req:   Request{SessionID: "sess", ToolName: "Write", WriteFiles: true, WorkDir: wd, Paths: []string{filepath.Join(wd, "src", "a", "b.go"), filepath.Join(wd, "src", "c.go")}},
			want:  EffectAllow, wantRuleID: "glob-src",
		},
		{
			name:  "glob rule requires every path to match",
			mode:  ModeDefault,
			rules: []Rule{{ID: "glob-src", Tool: "Write", Pattern: "src/**/*.go", MatchMode: MatchGlob, Effect: EffectAllow, Source: SourceSession}},
			req:   Request{SessionID: "sess", ToolName: "Write", WriteFiles: true, WorkDir: wd, Paths: []string{filepath.Join(wd, "src", "a.go"), filepath.Join(wd, "other", "b.go")}},
			want:  EffectAsk,
		},
		{
			name:  "glob rule ignores paths outside workdir",
			mode:  ModeDefault,
			rules: []Rule{{ID: "glob-src", Tool: "Write", Pattern: "**/*.go", MatchMode: MatchGlob, Effect: EffectAllow, Source: SourceSession}},
			req:   Request{SessionID: "sess", ToolName: "Write", WriteFiles: true, WorkDir: wd, Paths: []string{outside}},
			want:  EffectAsk,
		},
		{
			name:  "deny glob rule needs all paths to match",
			mode:  ModeDefault,
			rules: []Rule{{ID: "deny-secret", Tool: "Read", Pattern: "secret/**", MatchMode: MatchGlob, Effect: EffectDeny, Source: SourceUser}},
			req:   Request{SessionID: "sess", ToolName: "Read", ReadOnly: true, WorkDir: wd, Paths: []string{filepath.Join(wd, "secret", "a"), filepath.Join(wd, "public", "b")}},
			want:  EffectAllow,
		},
		{
			name:  "json rule with empty pattern allows whole tool",
			mode:  ModeDefault,
			rules: []Rule{{ID: "json-webfetch", Tool: "WebFetch", MatchMode: MatchJSON, Effect: EffectAllow, Source: SourceSession}},
			req:   Request{SessionID: "sess", ToolName: "WebFetch", Input: json.RawMessage(`{"url":"https://example.com"}`)},
			want:  EffectAllow, wantRuleID: "json-webfetch",
		},
		{
			name:  "deny rule beats bypassPermissions",
			mode:  ModeBypassPermissions,
			rules: []Rule{denyEcho},
			req:   bashRequest("echo hi"),
			want:  EffectDeny, wantRuleID: "deny-bash",
		},
		{
			name: "built-in hard deny beats bypassPermissions",
			mode: ModeBypassPermissions,
			req:  bashRequest("sudo rm -rf /*"),
			want: EffectDeny, wantRuleID: "builtin:rm-root",
		},
		{
			name:  "ask rule beats acceptEdits auto-allow",
			mode:  ModeAcceptEdits,
			rules: []Rule{{ID: "ask-write", Tool: "Write", MatchMode: MatchJSON, Effect: EffectAsk, Source: SourceProject}},
			req:   writeRequest(wd, "a.go"),
			want:  EffectAsk, wantRuleID: "ask-write",
		},
		{
			name:  "dontAsk collapses ask rule to deny",
			mode:  ModeDontAsk,
			rules: []Rule{askEcho},
			req:   bashRequest("echo hi"),
			want:  EffectDeny,
		},
		{
			name:  "allow rule with wildcard tool",
			mode:  ModeDefault,
			rules: []Rule{{ID: "allow-all-read", Tool: "*", MatchMode: MatchJSON, Effect: EffectAllow, Source: SourceUser}},
			req:   bashRequest("go build ./..."),
			want:  EffectAllow, wantRuleID: "allow-all-read",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEngine(t, tc.mode, wd, tc.rules...)
			d := checkDecision(t, e, tc.req)
			if d.Effect != tc.want {
				t.Fatalf("effect = %q, want %q (reason=%q)", d.Effect, tc.want, d.Reason)
			}
			if tc.wantRuleID != "" {
				if d.RuleID != tc.wantRuleID {
					t.Fatalf("ruleID = %q, want %q", d.RuleID, tc.wantRuleID)
				}
				if !strings.Contains(d.Reason, tc.wantRuleID) {
					t.Fatalf("reason %q does not mention rule %q", d.Reason, tc.wantRuleID)
				}
			}
		})
	}
}

// TestBuiltinDeny 覆盖内置硬拒绝的常见字面形式及其不误伤场景。
func TestBuiltinDeny(t *testing.T) {
	cases := []struct {
		cmd      string
		wantID   string
		wantDeny bool
	}{
		{"rm -rf /", "builtin:rm-root", true},
		{"RM -RF /", "builtin:rm-root", true},
		{"rm -fr /", "builtin:rm-root", true},
		{"rm -r -f /", "builtin:rm-root", true},
		{"sudo rm -rf /*", "builtin:rm-root", true},
		{"rm -rf ./build", "", false},
		{"rm -rf /tmp/x", "", false},
		{"rm -f /", "", false},
		{"mkfs.ext4 /dev/sda1", "builtin:mkfs", true},
		{"dd if=/dev/zero of=/dev/sda", "builtin:dd-device", true},
		{"dd if=/dev/zero of=/tmp/x", "", false},
		{"echo rm -rf /", "", false},
		{"go test ./...", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			input, err := json.Marshal(map[string]string{"command": tc.cmd})
			if err != nil {
				t.Fatal(err)
			}
			id, denied := builtinDeny(input)
			if denied != tc.wantDeny || id != tc.wantID {
				t.Fatalf("builtinDeny(%q) = (%q, %v), want (%q, %v)", tc.cmd, id, denied, tc.wantID, tc.wantDeny)
			}
		})
	}
}

// TestParseMode 覆盖合法与非法模式字符串。
func TestParseMode(t *testing.T) {
	valid := []Mode{ModeDefault, ModeAcceptEdits, ModePlan, ModeBypassPermissions, ModeDontAsk}
	for _, m := range valid {
		got, err := ParseMode(string(m))
		if err != nil || got != m {
			t.Fatalf("ParseMode(%q) = (%q, %v), want (%q, nil)", m, got, err, m)
		}
	}
	for _, s := range []string{"", "auto", "Default", "bypass", "unknown"} {
		if _, err := ParseMode(s); err == nil {
			t.Fatalf("ParseMode(%q) returned nil error, want error", s)
		}
	}
}

// TestRuleValidation 覆盖规则与构造参数校验。
func TestRuleValidation(t *testing.T) {
	valid := Rule{Tool: "Bash", Pattern: "ls", MatchMode: MatchExact, Effect: EffectAllow, Source: SourceUser}

	cases := []struct {
		name string
		rule Rule
	}{
		{"empty tool", Rule{MatchMode: MatchExact, Effect: EffectAllow}},
		{"empty match mode", Rule{Tool: "Bash", Effect: EffectAllow}},
		{"unknown match mode", Rule{Tool: "Bash", MatchMode: "regex", Effect: EffectAllow}},
		{"unknown effect", Rule{Tool: "Bash", MatchMode: MatchExact, Effect: "permit"}},
		{"glob without pattern", Rule{Tool: "Bash", MatchMode: MatchGlob, Effect: EffectAllow}},
		{"invalid glob", Rule{Tool: "Bash", Pattern: "[", MatchMode: MatchGlob, Effect: EffectAllow}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewEngine(Config{Mode: ModeDefault, Rules: []Rule{tc.rule}}); !errors.Is(err, ErrRuleInvalid) {
				t.Fatalf("NewEngine() error = %v, want ErrRuleInvalid", err)
			}
			e := newTestEngine(t, ModeDefault, "")
			if _, err := e.AddRule(tc.rule); !errors.Is(err, ErrRuleInvalid) {
				t.Fatalf("AddRule() error = %v, want ErrRuleInvalid", err)
			}
			if got := len(e.Rules()); got != 0 {
				t.Fatalf("rules after invalid AddRule = %d, want 0", got)
			}
		})
	}

	if _, err := NewEngine(Config{Mode: ""}); err == nil {
		t.Fatal("NewEngine with empty mode returned nil error")
	}
	if _, err := NewEngine(Config{Mode: "auto"}); err == nil {
		t.Fatal("NewEngine with unknown mode returned nil error")
	}
	dupA := valid
	dupA.ID = "dup"
	dupB := valid
	dupB.ID = "dup"
	if _, err := NewEngine(Config{Mode: ModeDefault, Rules: []Rule{dupA, dupB}}); err == nil {
		t.Fatal("NewEngine with duplicate IDs returned nil error")
	}

	e := newTestEngine(t, ModeDefault, "")
	r1, err := e.AddRule(valid)
	if err != nil {
		t.Fatalf("AddRule() error = %v", err)
	}
	if r1.ID != "rule_1" {
		t.Fatalf("assigned ID = %q, want rule_1", r1.ID)
	}
	r2, err := e.AddRule(valid)
	if err != nil {
		t.Fatalf("AddRule() error = %v", err)
	}
	if r2.ID != "rule_2" {
		t.Fatalf("assigned ID = %q, want rule_2", r2.ID)
	}
	if _, err := e.AddRule(Rule{ID: "rule_1", Tool: "Read", MatchMode: MatchExact, Effect: EffectAllow}); !errors.Is(err, ErrRuleInvalid) {
		t.Fatalf("duplicate AddRule error = %v, want ErrRuleInvalid", err)
	}
}

// TestSessionAuthorization 覆盖会话级 always allow/deny 授权表与模式切换。
func TestSessionAuthorization(t *testing.T) {
	wd := mustWorkDir(t)
	e := newTestEngine(t, ModeDefault, wd)

	if d := checkDecision(t, e, bashRequest("go test ./...")); d.Effect != EffectAsk {
		t.Fatalf("baseline effect = %q, want ask", d.Effect)
	}

	allow, err := e.AddRule(Rule{
		Tool: "Bash", Pattern: "go test ./...", MatchMode: MatchExact,
		Effect: EffectAllow, Source: SourceSession, Note: "always allow tests",
	})
	if err != nil {
		t.Fatalf("AddRule(allow) error = %v", err)
	}
	if allow.Source != SourceSession {
		t.Fatalf("source = %q, want session", allow.Source)
	}
	d := checkDecision(t, e, bashRequest("go test ./..."))
	if d.Effect != EffectAllow || d.RuleID != allow.ID {
		t.Fatalf("after always-allow: effect=%q ruleID=%q, want allow/%q", d.Effect, d.RuleID, allow.ID)
	}
	if !strings.Contains(d.Reason, "always allow tests") {
		t.Fatalf("reason %q does not carry rule note", d.Reason)
	}

	snapshot := e.Rules()
	if len(snapshot) != 1 || snapshot[0].ID != allow.ID {
		t.Fatalf("Rules() = %+v, want the added rule", snapshot)
	}
	snapshot[0].Effect = EffectDeny
	if again := e.Rules(); again[0].Effect != EffectAllow {
		t.Fatal("mutating Rules() snapshot changed engine state")
	}

	deny, err := e.AddRule(Rule{
		Tool: "Bash", Pattern: "curl", MatchMode: MatchPrefix,
		Effect: EffectDeny, Source: SourceSession,
	})
	if err != nil {
		t.Fatalf("AddRule(deny) error = %v", err)
	}
	if d := checkDecision(t, e, bashRequest("curl https://example.com")); d.Effect != EffectDeny || d.RuleID != deny.ID {
		t.Fatalf("deny_always: effect=%q ruleID=%q, want deny/%q", d.Effect, d.RuleID, deny.ID)
	}

	if !e.RemoveRule(allow.ID) {
		t.Fatal("RemoveRule(existing) = false, want true")
	}
	if e.RemoveRule(allow.ID) {
		t.Fatal("RemoveRule(removed) = true, want false")
	}
	if d := checkDecision(t, e, bashRequest("go test ./...")); d.Effect != EffectAsk {
		t.Fatalf("after removal effect = %q, want ask", d.Effect)
	}

	if err := e.SetMode(ModeAcceptEdits); err != nil {
		t.Fatalf("SetMode(acceptEdits) error = %v", err)
	}
	if d := checkDecision(t, e, writeRequest(wd, "a.go")); d.Effect != EffectAllow {
		t.Fatalf("acceptEdits effect = %q, want allow", d.Effect)
	}
	if err := e.SetMode("nope"); err == nil {
		t.Fatal("SetMode(invalid) returned nil error")
	}
	if got := e.Mode(); got != ModeAcceptEdits {
		t.Fatalf("mode after failed SetMode = %q, want acceptEdits", got)
	}
}

// TestGlobMatch 覆盖 glob 匹配的 `**`、`*`、`?`、字符类与非法模式。
func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"**/*.go", "a.go", true},
		{"**/*.go", "a/b.go", true},
		{"*.go", "a/b.go", false},
		{"*.go", "b.go", true},
		{"src/**/test.go", "src/test.go", true},
		{"src/**/test.go", "src/a/b/test.go", true},
		{"src/*/test.go", "src/a/b/test.go", false},
		{"src/*/test.go", "src/a/test.go", true},
		{"?.go", "a.go", true},
		{"?.go", "ab.go", false},
		{"[a-c].go", "b.go", true},
		{"[a-c].go", "d.go", false},
		{"[!a-c].go", "b.go", false},
		{"[!a-c].go", "d.go", true},
		{"**", "a/b", true},
		{"", "a", false},
		{"[", "a", false},
		{`a\*b`, "a*b", true},
	}
	for _, tc := range cases {
		t.Run(tc.pattern+"/"+tc.path, func(t *testing.T) {
			if got := matchGlob(tc.pattern, tc.path); got != tc.want {
				t.Fatalf("matchGlob(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
			}
		})
	}
}

// TestPathsWithin 覆盖工作目录包含判定，无法证明时 fail-closed。
func TestPathsWithin(t *testing.T) {
	wd := mustWorkDir(t)
	cases := []struct {
		name  string
		wd    string
		paths []string
		want  bool
	}{
		{"all inside", wd, []string{filepath.Join(wd, "a.go"), filepath.Join(wd, "sub", "b.go")}, true},
		{"one outside", wd, []string{filepath.Join(wd, "a.go"), filepath.Join(filepath.Dir(wd), "b.go")}, false},
		{"empty paths", wd, nil, false},
		{"relative path", wd, []string{"a.go"}, false},
		{"relative workdir", "relative", []string{filepath.Join(wd, "a.go")}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pathsWithin(tc.wd, tc.paths); got != tc.want {
				t.Fatalf("pathsWithin(%q, %v) = %v, want %v", tc.wd, tc.paths, got, tc.want)
			}
		})
	}
}

// TestCheckCanceledContext 覆盖 ctx 取消时 fail-closed 为 deny。
func TestCheckCanceledContext(t *testing.T) {
	wd := mustWorkDir(t)
	e := newTestEngine(t, ModeBypassPermissions, wd)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d, err := e.Check(ctx, readRequest(wd))
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if d.Effect != EffectDeny {
		t.Fatalf("canceled ctx effect = %q, want deny", d.Effect)
	}
}

// TestDecisionAllowed 覆盖 Decision.Allowed。
func TestDecisionAllowed(t *testing.T) {
	for _, tc := range []struct {
		effect Effect
		want   bool
	}{{EffectAllow, true}, {EffectAsk, false}, {EffectDeny, false}, {"", false}} {
		if got := (Decision{Effect: tc.effect}).Allowed(); got != tc.want {
			t.Fatalf("Allowed(%q) = %v, want %v", tc.effect, got, tc.want)
		}
	}
}

// TestConcurrentCheck 在并发读写规则与模式的同时反复判定，配合 -race 验证并发安全。
func TestConcurrentCheck(t *testing.T) {
	wd := mustWorkDir(t)
	e := newTestEngine(t, ModeDefault, wd)

	const goroutines = 16
	const iterations = 200
	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				switch (g + i) % 5 {
				case 0:
					if _, err := e.AddRule(Rule{
						Tool: "Bash", Pattern: "echo ok", MatchMode: MatchExact,
						Effect: EffectAllow, Source: SourceSession,
					}); err != nil {
						errCh <- fmt.Errorf("AddRule: %w", err)
						return
					}
				case 1:
					e.RemoveRule(fmt.Sprintf("rule_%d", i+1))
				case 2:
					if err := e.SetMode(ModeDefault); err != nil {
						errCh <- fmt.Errorf("SetMode(default): %w", err)
						return
					}
					if err := e.SetMode(ModeAcceptEdits); err != nil {
						errCh <- fmt.Errorf("SetMode(acceptEdits): %w", err)
						return
					}
				case 3:
					_ = e.Rules()
					_ = e.Mode()
				}

				d, err := e.Check(context.Background(), readRequest(wd))
				if err != nil {
					errCh <- fmt.Errorf("Check(read): %w", err)
					return
				}
				if d.Effect != EffectAllow {
					errCh <- fmt.Errorf("read-only effect = %q, want allow (reason=%q)", d.Effect, d.Reason)
					return
				}
				d, err = e.Check(context.Background(), writeRequest(wd, "x.go"))
				if err != nil {
					errCh <- fmt.Errorf("Check(write): %w", err)
					return
				}
				switch d.Effect {
				case EffectAllow, EffectAsk, EffectDeny:
				default:
					errCh <- fmt.Errorf("write effect = %q", d.Effect)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}
