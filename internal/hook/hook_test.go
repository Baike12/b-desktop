package hook

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Baike12/b-desktop/internal/agent"
	"github.com/Baike12/b-desktop/internal/permission"
	"github.com/Baike12/b-desktop/internal/tool"
)

// writeScript 在 dir 下写一个可执行的 sh 脚本并返回其绝对路径。
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}

// writeJSON 写入一份 JSON 文件。
func writeJSON(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write json: %v", err)
	}
}

// newManager 用临时目录构造 Manager，返回管理器与项目目录。
func newManager(t *testing.T, userConfig, projectConfig string) (*Manager, string, string) {
	t.Helper()
	root := t.TempDir()
	workDir := filepath.Join(root, "project")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	userPath := filepath.Join(root, "user", "hooks.json")
	projectPath := filepath.Join(workDir, ".b-desktop", "hooks.json")
	if userConfig != "" {
		writeJSON(t, userPath, userConfig)
	}
	if projectConfig != "" {
		writeJSON(t, projectPath, projectConfig)
	}
	m, err := New(Config{WorkDir: workDir, UserPath: userPath, ProjectPath: projectPath, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m, root, workDir
}

func TestLoadMergesSourcesAndDedupes(t *testing.T) {
	m, _, _ := newManager(t,
		`{"hooks":{"PreToolUse":[{"matcher":"Bash","command":"a"},{"matcher":"Read","command":"b"}]}}`,
		`{"PreToolUse":[{"matcher":"Bash","command":"a"},{"matcher":"Write","command":"c"}]}`,
	)
	entries := m.Entries()
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3: %+v", len(entries), entries)
	}
	want := []struct {
		matcher string
		command string
		source  string
	}{
		{"Bash", "a", SourceUser},
		{"Read", "b", SourceUser},
		{"Write", "c", SourceProject},
	}
	for i, w := range want {
		got := entries[i]
		if got.Matcher != w.matcher || got.Command != w.command || got.Source != w.source {
			t.Fatalf("entry %d = %+v, want %+v", i, got, w)
		}
		if got.Event != EventPreToolUse {
			t.Fatalf("entry %d event = %q", i, got.Event)
		}
	}
}

func TestLoadMissingFilesIsEmpty(t *testing.T) {
	m, _, _ := newManager(t, "", "")
	if got := m.Entries(); len(got) != 0 {
		t.Fatalf("entries = %+v, want empty", got)
	}
}

func TestLoadInvalidJSONReturnsError(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "hooks.json")
	writeJSON(t, path, "{not json")
	_, err := New(Config{WorkDir: root, UserPath: path, ProjectPath: filepath.Join(root, "missing.json")})
	if err == nil || !strings.Contains(err.Error(), "invalid config") {
		t.Fatalf("err = %v, want invalid config", err)
	}
}

func TestMatchPattern(t *testing.T) {
	cases := []struct {
		query, matcher string
		want           bool
	}{
		{"Bash", "", true},
		{"Bash", "*", true},
		{"Bash", "Bash", true},
		{"Bash", "Read", false},
		{"Edit", "Read|Edit|Write", true},
		{"Edit", "Read|Write", false},
		{"NotebookEdit", "^Note", true},
		{"Bash", "^Note", false},
		{"Bash", "[", false},
	}
	for _, c := range cases {
		if got := matchPattern(c.query, c.matcher); got != c.want {
			t.Fatalf("matchPattern(%q, %q) = %v, want %v", c.query, c.matcher, got, c.want)
		}
	}
}

func TestPreToolUseExitCodeTwoDenies(t *testing.T) {
	m, root, _ := newManager(t, "", "")
	script := writeScript(t, root, "deny.sh", `echo "not allowed" >&2; exit 2`)
	writeJSON(t, filepath.Join(root, "project", ".b-desktop", "hooks.json"),
		`{"hooks":{"PreToolUse":[{"matcher":"Bash","command":"`+script+`"}]}}`)
	if err := m.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	effect, reason, err := m.PreToolUse(context.Background(), agent.ToolEvent{SessionID: "s1", ToolName: "Bash"})
	if err != nil {
		t.Fatalf("PreToolUse: %v", err)
	}
	if effect != permission.EffectDeny {
		t.Fatalf("effect = %q, want deny", effect)
	}
	if reason != "not allowed" {
		t.Fatalf("reason = %q, want not allowed", reason)
	}
}

func TestPreToolUseJSONDeny(t *testing.T) {
	m, root, _ := newManager(t, "", "")
	script := writeScript(t, root, "json.sh", `printf '{"decision":"deny","reason":"blocked by policy"}'`)
	writeJSON(t, filepath.Join(root, "project", ".b-desktop", "hooks.json"),
		`{"hooks":{"PreToolUse":[{"matcher":"*","command":"`+script+`"}]}}`)
	if err := m.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	effect, reason, err := m.PreToolUse(context.Background(), agent.ToolEvent{ToolName: "Write"})
	if err != nil {
		t.Fatalf("PreToolUse: %v", err)
	}
	if effect != permission.EffectDeny || reason != "blocked by policy" {
		t.Fatalf("decision = %q %q, want deny/blocked by policy", effect, reason)
	}
}

func TestPreToolUsePermissionDecisionInHookSpecificOutput(t *testing.T) {
	m, root, _ := newManager(t, "", "")
	script := writeScript(t, root, "ask.sh",
		`printf '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask","permissionDecisionReason":"confirm it"}}'`)
	writeJSON(t, filepath.Join(root, "project", ".b-desktop", "hooks.json"),
		`{"hooks":{"PreToolUse":[{"matcher":"Bash","command":"`+script+`"}]}}`)
	if err := m.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	effect, reason, err := m.PreToolUse(context.Background(), agent.ToolEvent{ToolName: "Bash"})
	if err != nil {
		t.Fatalf("PreToolUse: %v", err)
	}
	if effect != permission.EffectAsk || reason != "confirm it" {
		t.Fatalf("decision = %q %q, want ask/confirm it", effect, reason)
	}
}

func TestPreToolUseDenyShortCircuits(t *testing.T) {
	m, root, _ := newManager(t, "", "")
	deny := writeScript(t, root, "deny.sh", `exit 2`)
	record := filepath.Join(root, "ran")
	after := writeScript(t, root, "after.sh", `echo x >> `+record)
	// 顺序：先 allow，再 deny，最后一条不应执行。
	writeJSON(t, filepath.Join(root, "project", ".b-desktop", "hooks.json"),
		`{"hooks":{"PreToolUse":[{"matcher":"Bash","command":"`+after+`"},{"matcher":"Bash","command":"`+deny+`"},{"matcher":"Bash","command":"`+after+`"}]}}`)
	if err := m.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	effect, _, err := m.PreToolUse(context.Background(), agent.ToolEvent{ToolName: "Bash"})
	if err != nil {
		t.Fatalf("PreToolUse: %v", err)
	}
	if effect != permission.EffectDeny {
		t.Fatalf("effect = %q, want deny", effect)
	}
	// 第一条会执行一次；deny 之后的第三条不会执行。
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	if string(raw) != "x\n" {
		t.Fatalf("record = %q, want one run", raw)
	}
}

func TestPreToolUseNonZeroExitIsErrorAndKeepsDecision(t *testing.T) {
	m, root, _ := newManager(t, "", "")
	script := writeScript(t, root, "fail.sh", `echo boom >&2; exit 3`)
	writeJSON(t, filepath.Join(root, "project", ".b-desktop", "hooks.json"),
		`{"hooks":{"PreToolUse":[{"matcher":"Bash","command":"`+script+`"}]}}`)
	if err := m.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	effect, _, err := m.PreToolUse(context.Background(), agent.ToolEvent{ToolName: "Bash"})
	if err == nil {
		t.Fatal("expected error for exit code 3")
	}
	if effect != "" {
		t.Fatalf("effect = %q, want empty", effect)
	}
}

func TestPreToolUseEventJSONOnStdin(t *testing.T) {
	m, root, workDir := newManager(t, "", "")
	capture := filepath.Join(root, "capture.json")
	script := writeScript(t, root, "capture.sh", `cat > `+capture)
	writeJSON(t, filepath.Join(workDir, ".b-desktop", "hooks.json"),
		`{"hooks":{"PreToolUse":[{"matcher":"Write","command":"`+script+`"}]}}`)
	if err := m.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	input := json.RawMessage(`{"file_path":"/tmp/a.txt"}`)
	if _, _, err := m.PreToolUse(context.Background(), agent.ToolEvent{
		SessionID: "session-1",
		ToolName:  "Write",
		Input:     input,
		Paths:     []string{"/tmp/a.txt"},
	}); err != nil {
		t.Fatalf("PreToolUse: %v", err)
	}
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	var got struct {
		HookEventName string          `json:"hook_event_name"`
		SessionID     string          `json:"session_id"`
		Cwd           string          `json:"cwd"`
		ToolName      string          `json:"tool_name"`
		ToolInput     json.RawMessage `json:"tool_input"`
		Paths         []string        `json:"paths"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal capture %q: %v", raw, err)
	}
	if got.HookEventName != "PreToolUse" || got.SessionID != "session-1" || got.ToolName != "Write" {
		t.Fatalf("capture = %+v", got)
	}
	if got.Cwd != workDir {
		t.Fatalf("cwd = %q, want %q", got.Cwd, workDir)
	}
	if string(got.ToolInput) != string(input) {
		t.Fatalf("tool_input = %s, want %s", got.ToolInput, input)
	}
	if len(got.Paths) != 1 || got.Paths[0] != "/tmp/a.txt" {
		t.Fatalf("paths = %v", got.Paths)
	}
}

func TestPostToolUseReceivesResponse(t *testing.T) {
	m, root, _ := newManager(t, "", "")
	capture := filepath.Join(root, "post.json")
	script := writeScript(t, root, "post.sh", `cat > `+capture)
	writeJSON(t, filepath.Join(root, "project", ".b-desktop", "hooks.json"),
		`{"hooks":{"PostToolUse":[{"matcher":"Bash","command":"`+script+`"}]}}`)
	if err := m.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	m.PostToolUse(context.Background(), agent.ToolEvent{SessionID: "s", ToolName: "Bash"},
		tool.Result{Content: "hello", IsError: false})
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	var got struct {
		HookEventName string `json:"hook_event_name"`
		ToolResponse  struct {
			Content string `json:"content"`
			IsError bool   `json:"is_error"`
		} `json:"tool_response"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.HookEventName != "PostToolUse" || got.ToolResponse.Content != "hello" || got.ToolResponse.IsError {
		t.Fatalf("capture = %+v", got)
	}
}

func TestUserPromptSubmitAppendsContext(t *testing.T) {
	m, root, _ := newManager(t, "", "")
	one := writeScript(t, root, "ctx1.sh", `printf '{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":"first"}}'`)
	two := writeScript(t, root, "ctx2.sh", `printf '{"additionalContext":"second"}'`)
	writeJSON(t, filepath.Join(root, "project", ".b-desktop", "hooks.json"),
		`{"hooks":{"UserPromptSubmit":[{"matcher":"Bash","command":"`+one+`"},{"command":"`+two+`"}]}}`)
	if err := m.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	text, err := m.UserPromptSubmit(context.Background(), "s", "hi")
	if err != nil {
		t.Fatalf("UserPromptSubmit: %v", err)
	}
	if text != "first\n\nsecond" {
		t.Fatalf("text = %q, want first\\n\\nsecond", text)
	}
}

func TestUserPromptSubmitExitTwoIsError(t *testing.T) {
	m, root, _ := newManager(t, "", "")
	script := writeScript(t, root, "block.sh", `echo "no prompts" >&2; exit 2`)
	writeJSON(t, filepath.Join(root, "project", ".b-desktop", "hooks.json"),
		`{"hooks":{"UserPromptSubmit":[{"command":"`+script+`"}]}}`)
	if err := m.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	text, err := m.UserPromptSubmit(context.Background(), "s", "hi")
	if err == nil || !strings.Contains(err.Error(), "no prompts") {
		t.Fatalf("err = %v, want blocked message", err)
	}
	if text != "" {
		t.Fatalf("text = %q, want empty", text)
	}
}

func TestSessionStartRunsOnlyStartupMatch(t *testing.T) {
	m, root, _ := newManager(t, "", "")
	hit := filepath.Join(root, "started")
	script := writeScript(t, root, "start.sh", `touch `+hit)
	writeJSON(t, filepath.Join(root, "project", ".b-desktop", "hooks.json"),
		`{"hooks":{"SessionStart":[{"matcher":"resume","command":"`+script+`"}]}}`)
	if err := m.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if err := m.SessionStart(context.Background(), "s", root); err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	if _, err := os.Stat(hit); err == nil {
		t.Fatal("resume hook should not run for startup")
	}
}

func TestSessionStartRuns(t *testing.T) {
	m, root, _ := newManager(t, "", "")
	hit := filepath.Join(root, "started")
	script := writeScript(t, root, "start.sh", `touch `+hit)
	writeJSON(t, filepath.Join(root, "project", ".b-desktop", "hooks.json"),
		`{"hooks":{"SessionStart":[{"matcher":"startup","command":"`+script+`"}]}}`)
	if err := m.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if err := m.SessionStart(context.Background(), "s", root); err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	if _, err := os.Stat(hit); err != nil {
		t.Fatalf("startup hook did not run: %v", err)
	}
}

func TestSessionStartExitTwoIsError(t *testing.T) {
	m, root, _ := newManager(t, "", "")
	script := writeScript(t, root, "block.sh", `echo "startup refused" >&2; exit 2`)
	writeJSON(t, filepath.Join(root, "project", ".b-desktop", "hooks.json"),
		`{"hooks":{"SessionStart":[{"matcher":"startup","command":"`+script+`"}]}}`)
	if err := m.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	err := m.SessionStart(context.Background(), "s", root)
	if err == nil || !strings.Contains(err.Error(), "startup refused") {
		t.Fatalf("err = %v, want blocking error", err)
	}
}

func TestTimeoutReturnsError(t *testing.T) {
	m, root, _ := newManager(t, "", "")
	script := writeScript(t, root, "slow.sh", `exec sleep 5`)
	writeJSON(t, filepath.Join(root, "project", ".b-desktop", "hooks.json"),
		`{"hooks":{"PreToolUse":[{"matcher":"Bash","command":"`+script+`"}]}}`)
	m.timeout = 100 * time.Millisecond
	if err := m.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	started := time.Now()
	_, _, err := m.PreToolUse(context.Background(), agent.ToolEvent{ToolName: "Bash"})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
}

func TestReloadReplacesEntries(t *testing.T) {
	m, _, workDir := newManager(t, "", "")
	path := filepath.Join(workDir, ".b-desktop", "hooks.json")
	writeJSON(t, path, `{"hooks":{"PreToolUse":[{"matcher":"Bash","command":"one"}]}}`)
	if err := m.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	writeJSON(t, path, `{"hooks":{"PostToolUse":[{"matcher":"Read","command":"two"}]}}`)
	if err := m.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	entries := m.Entries()
	if len(entries) != 1 || entries[0].Event != EventPostToolUse || entries[0].Command != "two" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestUserPromptSubmitPayloadContainsPrompt(t *testing.T) {
	m, root, _ := newManager(t, "", "")
	capture := filepath.Join(root, "prompt.json")
	script := writeScript(t, root, "prompt.sh", `cat > `+capture)
	writeJSON(t, filepath.Join(root, "project", ".b-desktop", "hooks.json"),
		`{"hooks":{"UserPromptSubmit":[{"command":"`+script+`"}]}}`)
	if err := m.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if _, err := m.UserPromptSubmit(context.Background(), "s", "hello world"); err != nil {
		t.Fatalf("UserPromptSubmit: %v", err)
	}
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	var got struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Prompt != "hello world" {
		t.Fatalf("prompt = %q", got.Prompt)
	}
}
