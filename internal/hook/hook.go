// Package hook 实现 agent.Hooks：从用户级与项目级 hooks.json 读取 shell 命令钩子，
// 在 Agent 生命周期的固定事件上执行，并把事件 JSON 写到命令的标准输入。
//
// 配置文件位置：
//
//	~/.b-desktop/hooks.json          用户级
//	<workDir>/.b-desktop/hooks.json  项目级
//
// 文件结构：
//
//	{"hooks": {"PreToolUse": [{"matcher": "Bash", "command": "./check.sh"}]}}
//
// 也接受把事件直接放在根对象上的简写形式。两处配置同时生效，用户级在前、项目级在后；
// 完全相同的 (事件, matcher, command) 只执行一次。
//
// 命令的退出码约定与参考实现一致：0 表示成功；2 表示阻塞（PreToolUse 视为 deny）；
// 其他非零退出码是普通错误，由调用方上报但不改变权限判定。
package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Baike12/b-desktop/internal/agent"
	"github.com/Baike12/b-desktop/internal/permission"
	"github.com/Baike12/b-desktop/internal/tool"
)

// Event 是钩子事件名。
type Event string

// 支持的事件。键名与 hooks.json 中的键完全一致。
const (
	EventPreToolUse       Event = "PreToolUse"
	EventPostToolUse      Event = "PostToolUse"
	EventUserPromptSubmit Event = "UserPromptSubmit"
	EventSessionStart     Event = "SessionStart"
)

// eventOrder 是返回条目时的固定顺序，保证前端展示稳定。
var eventOrder = []Event{
	EventPreToolUse,
	EventPostToolUse,
	EventUserPromptSubmit,
	EventSessionStart,
}

// DefaultTimeout 是单条命令的默认执行上限。
const DefaultTimeout = 10 * time.Second

// 配置来源标签。
const (
	SourceUser    = "user"
	SourceProject = "project"
)

// 退出码 2 是参考实现约定的「阻塞」码。
const blockExitCode = 2

// ErrInvalidConfig 表示 hooks 配置文件无法解析或事件条目非法。
var ErrInvalidConfig = errors.New("hook: invalid config")

// Entry 是一条已加载的钩子配置。
type Entry struct {
	// Event 是触发事件。
	Event Event
	// Matcher 是工具名匹配模式：空或 * 匹配全部，| 分隔的精确列表或正则表达式也支持。
	Matcher string
	// Command 是交给系统 shell 执行的命令。
	Command string
	// Source 是配置来源：user 或 project。
	Source string
}

// Config 是 Manager 的构造参数。
type Config struct {
	// WorkDir 是项目根目录，同时作为命令的工作目录。必填。
	WorkDir string
	// UserPath 是用户级配置文件路径；空表示 ~/.b-desktop/hooks.json。
	UserPath string
	// ProjectPath 是项目级配置文件路径；空表示 <WorkDir>/.b-desktop/hooks.json。
	ProjectPath string
	// Timeout 是单条命令的执行上限；0 表示 DefaultTimeout。
	Timeout time.Duration
}

// FileEntry 是 hooks.json 中的一条命令配置。
type FileEntry struct {
	Matcher string `json:"matcher"`
	Command string `json:"command"`
}

// Manager 持有加载后的钩子配置并实现 agent.Hooks。
// 所有方法并发安全；配置通过 Reload 整体替换。
type Manager struct {
	workDir     string
	userPath    string
	projectPath string
	timeout     time.Duration

	mu      sync.RWMutex
	entries map[Event][]Entry
}

// New 读取用户级与项目级配置并创建 Manager。
// 文件不存在视为没有对应配置；文件存在但无法解析时返回包装了 ErrInvalidConfig 的错误。
func New(cfg Config) (*Manager, error) {
	workDir := strings.TrimSpace(cfg.WorkDir)
	if workDir == "" {
		return nil, errors.New("hook: work dir is required")
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return nil, fmt.Errorf("hook: resolve work dir: %w", err)
	}
	workDir = abs
	userPath := cfg.UserPath
	if userPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("hook: resolve home dir: %w", err)
		}
		userPath = filepath.Join(home, ".b-desktop", "hooks.json")
	}
	projectPath := cfg.ProjectPath
	if projectPath == "" {
		projectPath = filepath.Join(workDir, ".b-desktop", "hooks.json")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	m := &Manager{
		workDir:     workDir,
		userPath:    userPath,
		projectPath: projectPath,
		timeout:     timeout,
	}
	if err := m.Reload(context.Background()); err != nil {
		return nil, err
	}
	return m, nil
}

// workDirValue 返回命令的工作目录。
func (m *Manager) workDirValue() string { return m.workDir }

// Reload 重新读取两处配置文件并整体替换内存中的钩子列表。
// 任一文件解析失败时保持旧配置不变并返回错误。
func (m *Manager) Reload(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := m.load()
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.entries = entries
	m.mu.Unlock()
	return nil
}

// load 依次读取用户级与项目级配置并去重。
func (m *Manager) load() (map[Event][]Entry, error) {
	out := make(map[Event][]Entry, len(eventOrder))
	seen := make(map[string]struct{})
	for _, source := range []struct {
		name string
		path string
	}{
		{SourceUser, m.userPath},
		{SourceProject, m.projectPath},
	} {
		loaded, err := readFile(source.path)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrInvalidConfig, source.path, err)
		}
		for _, ev := range eventOrder {
			for _, item := range loaded[ev] {
				key := string(ev) + "\x00" + item.Matcher + "\x00" + item.Command
				if _, dup := seen[key]; dup {
					continue
				}
				seen[key] = struct{}{}
				out[ev] = append(out[ev], Entry{
					Event:   ev,
					Matcher: item.Matcher,
					Command: item.Command,
					Source:  source.name,
				})
			}
		}
	}
	return out, nil
}

// readFile 读取并解析一个 hooks.json。文件不存在时返回空配置。
// 未知事件名被忽略，空命令条目被跳过，保证单个坏条目不会让整份配置失效。
func readFile(path string) (map[Event][]FileEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	raw, ok := root["hooks"]
	if !ok {
		// 简写形式：根对象直接是事件到条目数组的映射。
		raw = json.RawMessage(data)
	}
	var byEvent map[string][]FileEntry
	if err := json.Unmarshal(raw, &byEvent); err != nil {
		return nil, err
	}
	out := make(map[Event][]FileEntry, len(byEvent))
	for name, items := range byEvent {
		ev := Event(name)
		if !knownEvent(ev) {
			continue
		}
		for _, item := range items {
			if strings.TrimSpace(item.Command) == "" {
				continue
			}
			out[ev] = append(out[ev], item)
		}
	}
	return out, nil
}

// knownEvent 报告事件名是否受支持。
func knownEvent(ev Event) bool {
	for _, known := range eventOrder {
		if ev == known {
			return true
		}
	}
	return false
}

// Entries 返回当前生效的全部钩子条目，按事件固定顺序排列。
func (m *Manager) Entries() []Entry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Entry
	for _, ev := range eventOrder {
		out = append(out, m.entries[ev]...)
	}
	return out
}

// match 返回指定事件下匹配查询串的条目。query 为空时不做过滤。
func (m *Manager) match(ev Event, query string) []Entry {
	m.mu.RLock()
	snapshot := append([]Entry(nil), m.entries[ev]...)
	m.mu.RUnlock()
	if query == "" {
		return snapshot
	}
	out := snapshot[:0]
	for _, e := range snapshot {
		if matchPattern(query, e.Matcher) {
			out = append(out, e)
		}
	}
	return out
}

// matchPattern 按参考实现语义匹配工具名：
// 空或 * 匹配全部；只含字母、数字、下划线与竖线的模式按 | 分隔的精确名匹配；
// 其余按 Go 正则匹配（非法正则视为不匹配）。
func matchPattern(query, matcher string) bool {
	matcher = strings.TrimSpace(matcher)
	if matcher == "" || matcher == "*" {
		return true
	}
	if exactPattern.MatchString(matcher) {
		for _, part := range strings.Split(matcher, "|") {
			if strings.TrimSpace(part) == query {
				return true
			}
		}
		return false
	}
	re, err := regexp.Compile(matcher)
	if err != nil {
		return false
	}
	return re.MatchString(query)
}

// exactPattern 判断 matcher 是否是精确名（含 | 列表）而非正则。
var exactPattern = regexp.MustCompile(`^[A-Za-z0-9_|]+$`)

// SessionStart 执行 SessionStart 钩子。工作目录作为事件 cwd 传给命令；
// 事件 source 固定为 startup，因此 matcher 按 "startup" 匹配。
func (m *Manager) SessionStart(ctx context.Context, sessionID, workDir string) error {
	cwd := strings.TrimSpace(workDir)
	if cwd == "" {
		cwd = m.workDirValue()
	}
	payload := map[string]any{
		"hook_event_name": string(EventSessionStart),
		"session_id":      sessionID,
		"cwd":             cwd,
		"source":          "startup",
	}
	var errs []error
	for _, e := range m.match(EventSessionStart, "startup") {
		res, err := m.run(ctx, e, payload, cwd)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if res.exitCode == blockExitCode {
			errs = append(errs, fmt.Errorf("hook: session start blocked: %s", blockReason(res)))
		}
	}
	return errors.Join(errs...)
}

// UserPromptSubmit 执行 UserPromptSubmit 钩子，把各命令返回的 additionalContext
// 用空行拼接后返回。退出码 2 的命令视为阻塞并计入错误。
func (m *Manager) UserPromptSubmit(ctx context.Context, sessionID, prompt string) (string, error) {
	payload := map[string]any{
		"hook_event_name": string(EventUserPromptSubmit),
		"session_id":      sessionID,
		"cwd":             m.workDirValue(),
		"prompt":          prompt,
	}
	var (
		parts []string
		errs  []error
	)
	for _, e := range m.match(EventUserPromptSubmit, "") {
		res, err := m.run(ctx, e, payload, m.workDirValue())
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if res.exitCode == blockExitCode {
			errs = append(errs, fmt.Errorf("hook: prompt blocked: %s", blockReason(res)))
			continue
		}
		if res.exitCode != 0 {
			errs = append(errs, fmt.Errorf("hook: %s exited %d: %s", e.Command, res.exitCode, strings.TrimSpace(res.stderr)))
			continue
		}
		if text := additionalContext(res.stdout); strings.TrimSpace(text) != "" {
			parts = append(parts, strings.TrimSpace(text))
		}
	}
	return strings.Join(parts, "\n\n"), errors.Join(errs...)
}

// PreToolUse 执行 PreToolUse 钩子并返回合并后的权限判定。
// 第一条 deny 立即短路返回；ask 与 allow 按出现顺序取第一条；没有判定时返回空 Effect。
// 单条命令失败会继续执行其余命令，全部结束后以 errors.Join 返回。
func (m *Manager) PreToolUse(ctx context.Context, ev agent.ToolEvent) (permission.Effect, string, error) {
	payload := map[string]any{
		"hook_event_name": string(EventPreToolUse),
		"session_id":      ev.SessionID,
		"cwd":             m.workDirValue(),
		"tool_name":       ev.ToolName,
		"tool_input":      ev.Input,
		"paths":           ev.Paths,
	}
	var (
		effect permission.Effect
		reason string
		errs   []error
	)
	for _, e := range m.match(EventPreToolUse, ev.ToolName) {
		res, err := m.run(ctx, e, payload, m.workDirValue())
		if err != nil {
			errs = append(errs, err)
			continue
		}
		got, why := preToolEffect(res)
		switch got {
		case permission.EffectDeny:
			return permission.EffectDeny, why, nil
		case permission.EffectAsk, permission.EffectAllow:
			if effect == "" {
				effect, reason = got, why
			}
		default:
			// 非 2 的非零退出码是普通错误：上报，但不改变既有判定。
			if res.exitCode != 0 {
				errs = append(errs, fmt.Errorf("hook: %s exited %d: %s", e.Command, res.exitCode, strings.TrimSpace(res.stderr)))
			}
		}
	}
	return effect, reason, errors.Join(errs...)
}

// PostToolUse 执行 PostToolUse 钩子。接口没有返回值，命令失败只被忽略；
// 钩子崩溃不会影响已经产生的工具结果。
func (m *Manager) PostToolUse(ctx context.Context, ev agent.ToolEvent, res tool.Result) {
	payload := map[string]any{
		"hook_event_name": string(EventPostToolUse),
		"session_id":      ev.SessionID,
		"cwd":             m.workDirValue(),
		"tool_name":       ev.ToolName,
		"tool_input":      ev.Input,
		"paths":           ev.Paths,
		"tool_response": map[string]any{
			"content":  res.Content,
			"is_error": res.IsError,
		},
	}
	for _, e := range m.match(EventPostToolUse, ev.ToolName) {
		_, _ = m.run(ctx, e, payload, m.workDirValue())
	}
}

// execResult 是一次命令执行的结果。
type execResult struct {
	stdout   string
	stderr   string
	exitCode int
}

// run 执行一条钩子命令。payload 序列化成单行 JSON 写入标准输入。
// 返回的 error 只表示命令无法启动、超时或被取消；非零退出码通过 exitCode 表达。
func (m *Manager) run(ctx context.Context, e Entry, payload any, cwd string) (execResult, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return execResult{}, fmt.Errorf("hook: marshal %s event: %w", e.Event, err)
	}
	runCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	shell, args := shellCommand()
	cmd := exec.CommandContext(runCtx, shell, append(args, e.Command)...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	// 进程被杀后它派生的子进程可能仍持有输出管道；给一段收敛时间，
	// 避免超时钩子把整个轮次拖住。
	cmd.WaitDelay = time.Second
	cmd.Env = append(os.Environ(),
		"BDESKTOP_PROJECT_DIR="+m.workDirValue(),
		"BDESKTOP_HOOK_EVENT="+string(e.Event),
	)
	cmd.Stdin = bytes.NewReader(append(data, '\n'))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	res := execResult{stdout: stdout.String(), stderr: stderr.String()}
	if err == nil {
		return res, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.exitCode = exitErr.ExitCode()
		if runCtx.Err() != nil {
			return res, fmt.Errorf("hook: %s command timed out after %s: %w", e.Event, m.timeout, runCtx.Err())
		}
		return res, nil
	}
	return res, fmt.Errorf("hook: run %s command: %w", e.Event, err)
}

// shellCommand 返回执行钩子命令的 shell 与固定参数。
// Windows 使用 cmd.exe，其余平台使用 /bin/sh。
func shellCommand() (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd.exe", []string{"/C"}
	}
	return "/bin/sh", []string{"-c"}
}

// preToolEffect 把一次命令结果转换为权限判定。
func preToolEffect(res execResult) (permission.Effect, string) {
	if res.exitCode == blockExitCode {
		return permission.EffectDeny, blockReason(res)
	}
	if res.exitCode != 0 {
		return "", ""
	}
	out := parseOutput(res.stdout)
	if out == nil {
		return "", ""
	}
	decision := out.HookSpecificOutput.PermissionDecision
	if decision == "" {
		decision = out.Decision
	}
	reason := out.HookSpecificOutput.PermissionDecisionReason
	if reason == "" {
		reason = out.Reason
	}
	switch strings.ToLower(strings.TrimSpace(decision)) {
	case "deny", "block":
		if strings.TrimSpace(reason) == "" {
			reason = "denied by hook"
		}
		return permission.EffectDeny, reason
	case "ask":
		if strings.TrimSpace(reason) == "" {
			reason = "requires approval by hook"
		}
		return permission.EffectAsk, reason
	case "allow", "approve":
		return permission.EffectAllow, reason
	}
	return "", ""
}

// blockReason 提取阻塞原因：优先 stderr，其次 stdout，最后退回固定文案。
func blockReason(res execResult) string {
	for _, text := range []string{res.stderr, res.stdout} {
		if trimmed := strings.TrimSpace(text); trimmed != "" {
			return trimmed
		}
	}
	return "blocked by hook"
}

// hookOutput 是命令 stdout 中可识别的 JSON 输出。
type hookOutput struct {
	Decision           string `json:"decision"`
	Reason             string `json:"reason"`
	AdditionalContext  string `json:"additionalContext"`
	HookSpecificOutput struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
		AdditionalContext        string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// parseOutput 解析以 { 开头的 stdout；不是 JSON 时返回 nil。
func parseOutput(stdout string) *hookOutput {
	trimmed := strings.TrimSpace(stdout)
	if !strings.HasPrefix(trimmed, "{") {
		return nil
	}
	var out hookOutput
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return nil
	}
	return &out
}

// additionalContext 从 stdout 中取出要追加到系统提示的文本。
func additionalContext(stdout string) string {
	out := parseOutput(stdout)
	if out == nil {
		return ""
	}
	if strings.TrimSpace(out.HookSpecificOutput.AdditionalContext) != "" {
		return out.HookSpecificOutput.AdditionalContext
	}
	return out.AdditionalContext
}

// Paths 返回用户级与项目级配置文件路径，供设置页读写配置。
func (m *Manager) Paths() (userPath, projectPath string) {
	return m.userPath, m.projectPath
}

// ValidEvent 报告事件名是否受支持。
func ValidEvent(name string) bool {
	return knownEvent(Event(strings.TrimSpace(name)))
}

// ReadEntries 读取一份 hooks.json 并按事件分组。
// 文件不存在或内容为空时返回空映射；未知事件与空命令条目被忽略。
func ReadEntries(path string) (map[Event][]FileEntry, error) {
	return readFile(path)
}

// WriteEntries 原子写入一份 hooks.json（0600），父目录按 0700 创建。
// 空事件列表会被省略；事件顺序按 eventOrder 固定，保证文件稳定可读。
func WriteEntries(path string, entries map[Event][]FileEntry) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%w: empty config path", ErrInvalidConfig)
	}
	payload := make(map[string][]FileEntry, len(eventOrder))
	for _, ev := range eventOrder {
		items := entries[ev]
		if len(items) == 0 {
			continue
		}
		cleaned := make([]FileEntry, 0, len(items))
		for _, item := range items {
			command := strings.TrimSpace(item.Command)
			if command == "" {
				continue
			}
			cleaned = append(cleaned, FileEntry{Matcher: strings.TrimSpace(item.Matcher), Command: command})
		}
		if len(cleaned) > 0 {
			payload[string(ev)] = cleaned
		}
	}
	data, err := json.MarshalIndent(struct {
		Hooks map[string][]FileEntry `json:"hooks"`
	}{Hooks: payload}, "", "  ")
	if err != nil {
		return fmt.Errorf("hook: encode config: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("hook: create config dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".hooks-*.tmp")
	if err != nil {
		return fmt.Errorf("hook: create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("hook: chmod temp config: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("hook: write temp config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("hook: sync temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("hook: close temp config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("hook: replace config: %w", err)
	}
	return nil
}

// 编译期断言：Manager 必须满足 agent.Hooks。
var _ agent.Hooks = (*Manager)(nil)
