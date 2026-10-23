package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"anc-dsk/internal/agent"
	"anc-dsk/internal/llm"
	"anc-dsk/internal/permission"
	"anc-dsk/internal/session"
)

// testConfig 返回隔离的测试配置：临时工作目录 + 允许写操作的模式。
func testConfig(t *testing.T) Config {
	t.Helper()
	dir := t.TempDir()
	return Config{
		Provider:           "anthropic",
		APIKey:             "sk-test-1234",
		Model:              "test-model",
		PermissionMode:     string(permission.ModeBypassPermissions),
		WorkDir:            dir,
		DataDir:            filepath.Join(dir, "data"),
		ContextWindow:      200000,
		MaxOutputTokens:    4096,
		MaxTurns:           10,
		MaxToolConcurrency: 4,
	}
}

// fakeStep 是一次 Stream 调用的脚本。
type fakeStep struct {
	events []llm.StreamEvent
	err    error
	block  bool
}

// fakeProvider 按队列产出脚本化响应。
type fakeProvider struct {
	mu    sync.Mutex
	steps []fakeStep
	calls int
}

// newFakeProvider 创建脚本化 Provider。
func newFakeProvider(steps ...fakeStep) *fakeProvider {
	return &fakeProvider{steps: steps}
}

// Name 实现 llm.Provider。
func (p *fakeProvider) Name() string { return "fake" }

// Stream 实现 llm.Provider。
func (p *fakeProvider) Stream(context.Context, llm.Request) (llm.Stream, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	var step fakeStep
	if len(p.steps) > 0 {
		step = p.steps[0]
		p.steps = p.steps[1:]
	}
	return &fakeStream{step: step}, nil
}

// callCount 返回已发生的模型调用次数。
func (p *fakeProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// fakeStream 逐个返回脚本事件；block 为 true 时在事件耗尽后等待 ctx 取消。
type fakeStream struct {
	step fakeStep
	i    int
}

// Recv 实现 llm.Stream。
func (s *fakeStream) Recv(ctx context.Context) (llm.StreamEvent, error) {
	if s.i < len(s.step.events) {
		ev := s.step.events[s.i]
		s.i++
		return ev, nil
	}
	if s.step.block {
		<-ctx.Done()
		return llm.StreamEvent{}, ctx.Err()
	}
	if s.step.err != nil {
		return llm.StreamEvent{}, s.step.err
	}
	return llm.StreamEvent{}, io.EOF
}

// Close 实现 llm.Stream。
func (s *fakeStream) Close() error { return nil }

// eventCollector 捕获 Sender 收到的载荷。
type eventCollector struct {
	mu     sync.Mutex
	events []agent.Event
	names  []string
	ch     chan agent.Event
}

// newEventCollector 创建采集器。
func newEventCollector() *eventCollector {
	return &eventCollector{ch: make(chan agent.Event, 1024)}
}

// send 实现 Sender。
func (c *eventCollector) send(_ context.Context, name string, payload any) {
	ev, ok := payload.(agent.Event)
	if !ok {
		return
	}
	c.mu.Lock()
	c.events = append(c.events, ev)
	c.names = append(c.names, name)
	c.mu.Unlock()
	select {
	case c.ch <- ev:
	default:
	}
}

// snapshot 返回事件快照。
func (c *eventCollector) snapshot() []agent.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]agent.Event, len(c.events))
	copy(out, c.events)
	return out
}

// eventNames 返回事件通道名快照。
func (c *eventCollector) eventNames() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.names))
	copy(out, c.names)
	return out
}

// waitFor 等待指定类型的事件。
func (c *eventCollector) waitFor(t *testing.T, typ agent.EventType, timeout time.Duration) agent.Event {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev := <-c.ch:
			if ev.Type == typ {
				return ev
			}
		case <-deadline:
			t.Fatalf("timeout waiting for event %s", typ)
			return agent.Event{}
		}
	}
}

// count 返回指定类型事件的数量。
func (c *eventCollector) count(typ agent.EventType) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, ev := range c.events {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

// newTestApp 创建不启动 Wails 的应用实例。
func newTestApp(t *testing.T, provider llm.Provider) (*App, *eventCollector) {
	t.Helper()
	collector := newEventCollector()
	factory := func(Config) (llm.Provider, error) { return provider, nil }
	app, err := NewWithDeps(testConfig(t), collector.send, session.NewMemStore(), factory)
	if err != nil {
		t.Fatalf("NewWithDeps: %v", err)
	}
	return app, collector
}

// textStep 构造一段纯文本响应。
func textStep(messageID, text string) fakeStep {
	return fakeStep{events: []llm.StreamEvent{
		{Type: llm.EventMessageStart, MessageID: messageID},
		{Type: llm.EventBlockStart, Index: 0, Kind: llm.BlockText},
		{Type: llm.EventTextDelta, Index: 0, Kind: llm.BlockText, Text: text},
		{Type: llm.EventMessageDelta, StopReason: llm.StopEndTurn, Usage: &llm.Usage{InputTokens: 3, OutputTokens: 2}},
		{Type: llm.EventMessageStop},
	}}
}

// createTestSession 创建会话。
func createTestSession(t *testing.T, app *App) SessionInfoDTO {
	t.Helper()
	info, err := app.CreateSession(CreateSessionInput{Title: "测试会话"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return info
}

// waitIdle 等待会话的轮次结束（仅测试用；生产环境前端不需要轮询）。
func waitIdle(t *testing.T, app *App, sessionID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if rt, err := app.runtimeOf(sessionID); err == nil && !rt.isRunning() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("session did not become idle")
}

func TestProjectOpenListAndPersistence(t *testing.T) {
	cfg := testConfig(t)
	projectDir := filepath.Join(cfg.WorkDir, "demo-project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	realDir, err := filepath.EvalSymlinks(projectDir)
	if err != nil {
		t.Fatal(err)
	}

	buildApp := func() *App {
		t.Helper()
		app, err := NewWithDeps(cfg, func(context.Context, string, any) {}, nil, nil)
		if err != nil {
			t.Fatalf("NewWithDeps: %v", err)
		}
		app.pickDir = func(context.Context) (string, error) { return projectDir, nil }
		return app
	}

	app := buildApp()
	items, err := app.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("初始项目列表应为空: %+v", items)
	}

	opened, err := app.OpenProject()
	if err != nil {
		t.Fatalf("OpenProject: %v", err)
	}
	if opened.Path != realDir || opened.Name != "demo-project" || opened.AddedAtMS == 0 {
		t.Fatalf("unexpected project: %+v", opened)
	}

	// 重复打开同一项目不产生重复条目。
	again, err := app.OpenProject()
	if err != nil {
		t.Fatalf("OpenProject again: %v", err)
	}
	if again.AddedAtMS != opened.AddedAtMS {
		t.Fatalf("重复打开不应改变条目: %+v vs %+v", again, opened)
	}
	items, err = app.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("重复打开后条目数 = %d, want 1", len(items))
	}

	// 重新创建 App 实例后仍能读回（projects.json 落盘）。
	reopened, err := buildApp().ListProjects()
	if err != nil {
		t.Fatalf("ListProjects after restart: %v", err)
	}
	if len(reopened) != 1 || reopened[0].Path != realDir {
		t.Fatalf("重启后项目列表 = %+v", reopened)
	}
}

func TestOpenProjectCanceled(t *testing.T) {
	app, err := NewWithDeps(testConfig(t), func(context.Context, string, any) {}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	app.pickDir = func(context.Context) (string, error) { return "", nil }
	if _, err := app.OpenProject(); !errors.Is(err, ErrProjectDialogCanceled) {
		t.Fatalf("取消打开项目应返回 ErrProjectDialogCanceled, got %v", err)
	}
}

func TestAddProjectValidation(t *testing.T) {
	app, err := NewWithDeps(testConfig(t), func(context.Context, string, any) {}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.AddProject(""); err == nil {
		t.Fatal("空路径应返回错误")
	}
	file := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := app.AddProject(file); err == nil {
		t.Fatal("文件路径应返回错误")
	}
}

func TestProjectSessionUsesProjectWorkDir(t *testing.T) {
	app, _ := newTestApp(t, newFakeProvider())
	projectDir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	realDir, err := filepath.EvalSymlinks(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := app.CreateSession(CreateSessionInput{Title: "新会话", WorkDir: projectDir})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if info.WorkDir != realDir {
		t.Fatalf("session workDir = %q, want %q", info.WorkDir, realDir)
	}
}

func TestGreet(t *testing.T) {
	got := New().Greet("ANC")
	want := "Hello ANC, ANC DSK is running."
	if got != want {
		t.Fatalf("Greet() = %q, want %q", got, want)
	}
}

func TestSessionViewContextTokens(t *testing.T) {
	loaded := session.Loaded{
		Meta: session.Meta{ID: "s1", Title: "t", WorkDir: "/tmp", CreatedAt: time.Now()},
		LastUsage: llm.Usage{
			InputTokens:              10,
			OutputTokens:             2,
			CacheReadInputTokens:     30,
			CacheCreationInputTokens: 4,
		},
	}
	dto := toSessionViewDTO(loaded, "m", "default", false)
	if dto.ContextTokens != 46 {
		t.Fatalf("ContextTokens = %d, want 46", dto.ContextTokens)
	}
}

func TestDTOConversion(t *testing.T) {
	at := time.UnixMilli(1700000000123)
	msg := llm.Message{
		ID:         "m1",
		Role:       llm.RoleAssistant,
		StopReason: llm.StopToolUse,
		Time:       at,
		Blocks: []llm.ContentBlock{
			{Type: llm.BlockText, Text: "hi"},
			{Type: llm.BlockThinking, Thinking: "why", Signature: "sig"},
			{Type: llm.BlockToolUse, ToolUseID: "t1", ToolName: "Read", Input: json.RawMessage(`{"file_path":"a.go","limit":2}`)},
			{Type: llm.BlockToolResult, ToolUseID: "t1", Text: "ok", IsError: true},
		},
	}

	dto := toMessageDTO(msg)
	if dto.ID != "m1" || dto.Role != "assistant" || dto.StopReason != "tool_use" {
		t.Fatalf("message dto = %+v", dto)
	}
	if dto.TimeMS != at.UnixMilli() {
		t.Fatalf("TimeMS = %d, want %d", dto.TimeMS, at.UnixMilli())
	}
	if len(dto.Blocks) != 4 {
		t.Fatalf("blocks = %d, want 4", len(dto.Blocks))
	}
	input, ok := dto.Blocks[2].Input.(map[string]any)
	if !ok {
		t.Fatalf("tool input type = %T, want map[string]any (Wails 需要对象而不是 number[])", dto.Blocks[2].Input)
	}
	if input["file_path"] != "a.go" {
		t.Fatalf("tool input = %#v", input)
	}
	if !dto.Blocks[3].IsError || dto.Blocks[3].Text != "ok" {
		t.Fatalf("tool result dto = %+v", dto.Blocks[3])
	}

	if v := decodeJSONValue(nil); v != nil {
		t.Fatalf("empty input = %#v, want nil", v)
	}
	if v := decodeJSONValue(json.RawMessage(`{broken`)); v != nil {
		t.Fatalf("invalid input = %#v, want nil", v)
	}

	usage := toUsageDTO(llm.Usage{InputTokens: 10, OutputTokens: 4, CacheReadInputTokens: 2, CacheCreationInputTokens: 1})
	want := UsageDTO{InputTokens: 10, OutputTokens: 4, CacheReadInputTokens: 2, CacheCreationInputTokens: 1}
	if usage != want {
		t.Fatalf("usage dto = %+v, want %+v", usage, want)
	}

	view := toSessionViewDTO(session.Loaded{
		Messages: []llm.Message{msg},
		Meta:     session.Meta{ID: "s1", Title: "t", WorkDir: "/tmp", CreatedAt: at},
		Usage:    llm.Usage{InputTokens: 1},
	}, "model-x", "default", true)
	if view.ID != "s1" || view.Model != "model-x" || !view.Running || len(view.Messages) != 1 {
		t.Fatalf("session view = %+v", view)
	}
	if view.CreatedAtMS != at.UnixMilli() {
		t.Fatalf("CreatedAtMS = %d", view.CreatedAtMS)
	}

	// 所有绑定返回的 DTO 必须可被 JSON 序列化（Wails 的编码路径）。
	for name, value := range map[string]any{
		"session info":      toSessionInfoDTO(session.Info{ID: "s1", UpdatedAt: at}, true),
		"session view":      view,
		"message":           dto,
		"send message":      SendMessageResult{SessionID: "s1", TurnID: "turn_1", Running: true},
		"provider config":   toProviderConfigDTO(testConfig(t), nil),
		"create session in": CreateSessionInput{SessionID: "s1", Title: "t"},
	} {
		if _, err := marshalDTO(value); err != nil {
			t.Fatalf("%s is not JSON serializable: %v", name, err)
		}
	}
}

func TestMaskAPIKeyAndNoLeak(t *testing.T) {
	cases := map[string]string{
		"":               "",
		"abcd":           "****",
		"sk-secret-9876": "****9876",
		"中文密钥-最后四位":      "****最后四位",
	}
	for key, want := range cases {
		if got := maskAPIKey(key); got != want {
			t.Fatalf("maskAPIKey(%q) = %q, want %q", key, got, want)
		}
	}

	cfg := testConfig(t)
	cfg.APIKey = "sk-super-secret-4321"
	app, err := NewWithDeps(cfg, func(context.Context, string, any) {}, session.NewMemStore(), nil)
	if err != nil {
		t.Fatalf("NewWithDeps: %v", err)
	}
	dto := app.GetProviderConfig()
	if !dto.HasAPIKey {
		t.Fatal("HasAPIKey = false, want true")
	}
	if dto.APIKeyMasked != "****4321" {
		t.Fatalf("APIKeyMasked = %q", dto.APIKeyMasked)
	}
	raw, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "sk-super-secret") {
		t.Fatalf("provider config leaks the api key: %s", raw)
	}
}

func TestSetProviderConfigValidation(t *testing.T) {
	app, err := NewWithDeps(testConfig(t), func(context.Context, string, any) {}, session.NewMemStore(), nil)
	if err != nil {
		t.Fatalf("NewWithDeps: %v", err)
	}

	in := ProviderConfigInput{Provider: "openai", BaseURL: "https://example.com/v1", Model: "gpt-test"}
	if err := app.SetProviderConfig(in); err != nil {
		t.Fatalf("SetProviderConfig: %v", err)
	}
	got := app.GetProviderConfig()
	if got.Provider != "openai" || got.BaseURL != "https://example.com/v1" || got.Model != "gpt-test" {
		t.Fatalf("config = %+v", got)
	}
	if !got.HasAPIKey || got.APIKeyMasked != "****1234" {
		t.Fatalf("empty APIKey must keep the existing key, got %+v", got)
	}

	if err := app.SetProviderConfig(ProviderConfigInput{Provider: "bogus"}); !errors.Is(err, ErrInvalidProviderConfig) {
		t.Fatalf("unknown provider error = %v", err)
	}
	if err := app.SetProviderConfig(ProviderConfigInput{Provider: "openai", BaseURL: "example.com"}); !errors.Is(err, ErrInvalidProviderConfig) {
		t.Fatalf("missing scheme error = %v", err)
	}
	if err := app.SetProviderConfig(ProviderConfigInput{Provider: "openai", ContextWindow: -1}); !errors.Is(err, ErrInvalidProviderConfig) {
		t.Fatalf("negative window error = %v", err)
	}
	if err := app.SetProviderConfig(ProviderConfigInput{Provider: ""}); !errors.Is(err, ErrInvalidProviderConfig) {
		t.Fatalf("empty provider error = %v", err)
	}

	if err := app.SetProviderConfig(ProviderConfigInput{Provider: "anthropic", APIKey: "sk-new-5678"}); err != nil {
		t.Fatalf("rotate key: %v", err)
	}
	if got := app.GetProviderConfig(); got.APIKeyMasked != "****5678" {
		t.Fatalf("rotated mask = %q", got.APIKeyMasked)
	}
	if got := app.GetProviderConfig(); got.ContextWindow != defaultContextWindow {
		t.Fatalf("context window = %d, want default %d", got.ContextWindow, defaultContextWindow)
	}
}

func TestSetProviderConfigPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "config.json")
	cfg := testConfig(t)
	cfg.ConfigPath = path
	app, err := NewWithDeps(cfg, func(context.Context, string, any) {}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := app.SetProviderConfig(ProviderConfigInput{
		Provider:        "openai",
		BaseURL:         "https://api.example.com/v1",
		Model:           "gpt-test",
		APIKey:          "sk-persist",
		ContextWindow:   64000,
		MaxOutputTokens: 4096,
	}); err != nil {
		t.Fatalf("SetProviderConfig: %v", err)
	}

	loaded, err := loadConfigFile(path)
	if err != nil {
		t.Fatalf("loadConfigFile: %v", err)
	}
	if loaded.Provider != "openai" || loaded.Model != "gpt-test" || loaded.APIKey != "sk-persist" {
		t.Fatalf("persisted config = %+v", loaded)
	}
	if loaded.ContextWindow != 64000 || loaded.MaxOutputTokens != 4096 {
		t.Fatalf("persisted limits = %+v", loaded)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %o, want 600", info.Mode().Perm())
	}

	// 空 APIKey 表示保留现有密钥，落盘后也不能丢失。
	if err := app.SetProviderConfig(ProviderConfigInput{Provider: "openai", Model: "gpt-next"}); err != nil {
		t.Fatalf("second SetProviderConfig: %v", err)
	}
	loaded, err = loadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.APIKey != "sk-persist" || loaded.Model != "gpt-next" {
		t.Fatalf("after empty key update = %+v", loaded)
	}
}

func TestCreateListLoadSession(t *testing.T) {
	app, _ := newTestApp(t, newFakeProvider())

	info := createTestSession(t, app)
	if info.ID == "" {
		t.Fatal("empty session id")
	}
	if info.Title != "测试会话" || info.Messages != 0 || info.Running {
		t.Fatalf("info = %+v", info)
	}
	if info.UpdatedAtMS == 0 {
		t.Fatal("UpdatedAtMS = 0")
	}

	list, err := app.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 1 || list[0].ID != info.ID {
		t.Fatalf("list = %+v", list)
	}

	view, err := app.LoadSession(info.ID)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if view.ID != info.ID || view.Model != "test-model" || view.Running {
		t.Fatalf("view = %+v", view)
	}
	if view.PermissionMode != string(permission.ModeBypassPermissions) {
		t.Fatalf("permission mode = %q", view.PermissionMode)
	}
	if len(view.Messages) != 0 {
		t.Fatalf("messages = %+v, want empty", view.Messages)
	}
	if view.CreatedAtMS == 0 {
		t.Fatal("CreatedAtMS = 0")
	}

	// 追加一条消息后，LoadSession 必须把它重放成 DTO。
	msg := llm.Message{
		ID:     "m1",
		Role:   llm.RoleAssistant,
		Time:   time.Now(),
		Blocks: []llm.ContentBlock{{Type: llm.BlockText, Text: "hello"}},
	}
	store, _, err := app.sessionStore()
	if err != nil {
		t.Fatalf("sessionStore: %v", err)
	}
	if err := store.Append(context.Background(), session.Record{
		Type: session.RecordMessage, SessionID: info.ID, Message: &msg,
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	view, err = app.LoadSession(info.ID)
	if err != nil {
		t.Fatalf("LoadSession after append: %v", err)
	}
	if len(view.Messages) != 1 || view.Messages[0].Blocks[0].Text != "hello" {
		t.Fatalf("messages = %+v", view.Messages)
	}

	list, err = app.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if list[0].Messages != 1 {
		t.Fatalf("list messages = %d, want 1", list[0].Messages)
	}
}

func TestSessionValidation(t *testing.T) {
	app, _ := newTestApp(t, newFakeProvider())

	if _, err := app.CreateSession(CreateSessionInput{SessionID: "../escape"}); !errors.Is(err, session.ErrInvalidID) {
		t.Fatalf("invalid id error = %v", err)
	}
	if _, err := app.CreateSession(CreateSessionInput{WorkDir: filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("missing work dir must fail")
	}

	info := createTestSession(t, app)
	if _, err := app.CreateSession(CreateSessionInput{SessionID: info.ID}); !errors.Is(err, session.ErrSessionExists) {
		t.Fatalf("duplicate error = %v", err)
	}

	if _, err := app.LoadSession("missing-session"); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("missing load error = %v", err)
	}
	if _, err := app.LoadSession("bad/id"); !errors.Is(err, session.ErrInvalidID) {
		t.Fatalf("invalid load id error = %v", err)
	}
}

func TestSendMessageValidation(t *testing.T) {
	app, _ := newTestApp(t, newFakeProvider())

	if _, err := app.SendMessage("s1", "   "); !errors.Is(err, ErrEmptyPrompt) {
		t.Fatalf("empty prompt error = %v", err)
	}
	if _, err := app.SendMessage("bad/id", "hi"); !errors.Is(err, session.ErrInvalidID) {
		t.Fatalf("invalid session id error = %v", err)
	}
	if _, err := app.SendMessage("missing-session", "hi"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("missing session error = %v", err)
	}
	if _, err := app.SendMessage("s1", strings.Repeat("x", maxPromptBytes+1)); !errors.Is(err, ErrPromptTooLong) {
		t.Fatalf("long prompt error = %v", err)
	}

	info := createTestSession(t, app)
	if err := app.CancelTurn(info.ID); err != nil {
		t.Fatalf("CancelTurn idle: %v", err)
	}
	if err := app.CancelTurn("missing-session"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("CancelTurn missing error = %v", err)
	}

	if err := app.ApproveTool(info.ID, "req_x", "maybe"); !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("invalid option error = %v", err)
	}
	if err := app.ApproveTool(info.ID, "req_x", permission.OptionAllowOnce); !errors.Is(err, ErrApprovalNotFound) {
		t.Fatalf("unknown approval error = %v", err)
	}
	if err := app.ApproveTool(info.ID, "  ", permission.OptionAllowOnce); !errors.Is(err, ErrApprovalNotFound) {
		t.Fatalf("blank approval error = %v", err)
	}
}

func TestSendMessageRequiresModel(t *testing.T) {
	cfg := testConfig(t)
	cfg.Model = ""
	app, err := NewWithDeps(cfg, func(context.Context, string, any) {}, session.NewMemStore(), nil)
	if err != nil {
		t.Fatalf("NewWithDeps: %v", err)
	}
	info := createTestSession(t, app)
	if _, err := app.SendMessage(info.ID, "hi"); !errors.Is(err, ErrModelNotConfigured) {
		t.Fatalf("error = %v, want ErrModelNotConfigured", err)
	}
}

func TestSendMessageStreamsEvents(t *testing.T) {
	provider := newFakeProvider(textStep("m1", "你好"))
	app, collector := newTestApp(t, provider)
	info := createTestSession(t, app)

	result, err := app.SendMessage(info.ID, "hi")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if !result.Running || result.TurnID == "" || result.SessionID != info.ID {
		t.Fatalf("result = %+v", result)
	}

	end := collector.waitFor(t, agent.EventTurnEnd, 3*time.Second)
	if end.StopReason != agent.StopCompleted {
		t.Fatalf("stop reason = %q", end.StopReason)
	}
	if end.SessionID != info.ID || end.TurnID != result.TurnID {
		t.Fatalf("turn_end ids = %+v", end)
	}

	events := collector.snapshot()
	if len(events) < 2 || events[0].Type != agent.EventTurnStart {
		t.Fatalf("first event = %+v", events[0])
	}
	last := events[len(events)-1]
	if last.Type != agent.EventTurnEnd {
		t.Fatalf("last event = %+v", last)
	}
	for i, ev := range events {
		if ev.Seq <= 0 {
			t.Fatalf("event %d has no seq: %+v", i, ev)
		}
		if i > 0 && ev.Seq <= events[i-1].Seq {
			t.Fatalf("seq not increasing at %d: %d then %d", i, events[i-1].Seq, ev.Seq)
		}
	}
	if collector.count(agent.EventTextDelta) != 1 {
		t.Fatalf("text_delta count = %d", collector.count(agent.EventTextDelta))
	}
	for _, name := range collector.eventNames() {
		if name != EventNameAgent {
			t.Fatalf("event name = %q, want %q", name, EventNameAgent)
		}
	}

	// 事件载荷必须与 internal/agent/event.go 的字段一致（Wails 直接序列化 agent.Event）。
	raw, err := json.Marshal(end)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	for _, key := range []string{"type", "sessionId", "turnId", "seq", "timeMs", "stopReason"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("event json missing %q: %s", key, raw)
		}
	}

	waitIdle(t, app, info.ID)
	view, err := app.LoadSession(info.ID)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if len(view.Messages) != 2 {
		t.Fatalf("persisted messages = %d, want 2", len(view.Messages))
	}
	if view.Usage.OutputTokens != 2 {
		t.Fatalf("usage = %+v", view.Usage)
	}
}

func TestEngineLifecycleAndRebuild(t *testing.T) {
	provider := newFakeProvider(
		textStep("m1", "one"),
		textStep("m2", "two"),
		textStep("m3", "three"),
	)
	var mu sync.Mutex
	builds := 0
	factory := func(Config) (llm.Provider, error) {
		mu.Lock()
		builds++
		mu.Unlock()
		return provider, nil
	}
	collector := newEventCollector()
	app, err := NewWithDeps(testConfig(t), collector.send, session.NewMemStore(), factory)
	if err != nil {
		t.Fatalf("NewWithDeps: %v", err)
	}
	info := createTestSession(t, app)

	send := func() {
		t.Helper()
		if _, err := app.SendMessage(info.ID, "hi"); err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
		collector.waitFor(t, agent.EventTurnEnd, 3*time.Second)
		waitIdle(t, app, info.ID)
	}

	send()
	if builds != 1 {
		t.Fatalf("provider builds = %d, want 1", builds)
	}
	send()
	if builds != 1 {
		t.Fatalf("provider builds after second turn = %d, want 1 (engine reused)", builds)
	}

	if err := app.SetProviderConfig(ProviderConfigInput{
		Provider: "anthropic", Model: "test-model-2", ContextWindow: 100000, MaxOutputTokens: 2048,
	}); err != nil {
		t.Fatalf("SetProviderConfig: %v", err)
	}
	send()
	if builds != 2 {
		t.Fatalf("provider builds after config change = %d, want 2", builds)
	}
	if provider.callCount() != 3 {
		t.Fatalf("model calls = %d, want 3", provider.callCount())
	}
}

func TestShutdownCancelsRunningTurn(t *testing.T) {
	blocked := fakeStep{
		events: []llm.StreamEvent{
			{Type: llm.EventMessageStart, MessageID: "m1"},
			{Type: llm.EventBlockStart, Index: 0, Kind: llm.BlockText},
			{Type: llm.EventTextDelta, Index: 0, Kind: llm.BlockText, Text: "partial"},
		},
		block: true,
	}
	app, collector := newTestApp(t, newFakeProvider(blocked))
	info := createTestSession(t, app)

	if _, err := app.SendMessage(info.ID, "hi"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	collector.waitFor(t, agent.EventTextDelta, 3*time.Second)
	waitUntilRunning(t, app, info.ID)

	app.Shutdown(context.Background())

	end := collector.waitFor(t, agent.EventTurnEnd, time.Second)
	if end.StopReason != agent.StopCanceled {
		t.Fatalf("stop reason = %q, want canceled", end.StopReason)
	}
	if collector.count(agent.EventMessageDiscard) != 1 {
		t.Fatalf("message_discard count = %d, want 1", collector.count(agent.EventMessageDiscard))
	}
	if _, err := app.SendMessage(info.ID, "hi"); !errors.Is(err, ErrClosed) {
		t.Fatalf("SendMessage after shutdown error = %v", err)
	}
	if _, err := app.ListSessions(); !errors.Is(err, ErrClosed) {
		t.Fatalf("ListSessions after shutdown error = %v", err)
	}
}

// waitUntilRunning 等待轮次真正开始。
func waitUntilRunning(t *testing.T, app *App, sessionID string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if rt, err := app.runtimeOf(sessionID); err == nil && rt.isRunning() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("turn did not start")
}

func TestApproveToolDeliversResponse(t *testing.T) {
	app, _ := newTestApp(t, newFakeProvider())
	first := createTestSession(t, app)
	second := createTestSession(t, app)

	rt, err := app.runtimeOf(first.ID)
	if err != nil {
		t.Fatalf("runtimeOf: %v", err)
	}
	ch, cleanup := rt.approvals.Prepare(permission.ApprovalView{
		RequestID: "req_1",
		SessionID: first.ID,
		ToolName:  "Write",
		Reason:    "approval required",
	})
	defer cleanup()

	if err := app.ApproveTool(second.ID, "req_1", permission.OptionAllowOnce); !errors.Is(err, ErrApprovalNotFound) {
		t.Fatalf("cross-session approval error = %v", err)
	}
	if err := app.ApproveTool(first.ID, "req_1", permission.OptionAllowAlways); err != nil {
		t.Fatalf("ApproveTool: %v", err)
	}
	select {
	case resp, ok := <-ch:
		if !ok {
			t.Fatal("approval channel closed without response")
		}
		if resp.Option != permission.OptionAllowAlways {
			t.Fatalf("response = %+v", resp)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for approval response")
	}
	if err := app.ApproveTool(first.ID, "req_1", permission.OptionAllowOnce); !errors.Is(err, ErrApprovalNotFound) {
		t.Fatalf("answered approval error = %v", err)
	}
}

// TestSetPermissionModePersistsPerSession 验证权限模式是会话级的：
// 切换后 LoadSession 返回新值、transcript 写入 mode 记录，全局配置不变。
func TestSetPermissionModePersistsPerSession(t *testing.T) {
	provider := newFakeProvider(textStep("m1", "ok"))
	app, _ := newTestApp(t, provider)
	ctx := context.Background()
	app.Startup(ctx)
	defer app.Shutdown(ctx)

	info := createTestSession(t, app)
	globalBefore := app.GetProviderConfig().PermissionMode
	if err := app.SetPermissionMode(info.ID, string(permission.ModePlan)); err != nil {
		t.Fatalf("SetPermissionMode: %v", err)
	}

	view, err := app.LoadSession(info.ID)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if view.PermissionMode != string(permission.ModePlan) {
		t.Fatalf("session mode = %q, want plan", view.PermissionMode)
	}
	if got := app.GetProviderConfig().PermissionMode; got != globalBefore {
		t.Fatalf("global default mode = %q, want unchanged %q", got, globalBefore)
	}

	store, _, err := app.sessionStore()
	if err != nil {
		t.Fatalf("sessionStore: %v", err)
	}
	recs, err := store.Load(ctx, info.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	modes := 0
	for _, rec := range recs {
		if rec.Type == session.RecordMode && rec.Meta["mode"] == string(permission.ModePlan) {
			modes++
		}
	}
	if modes != 1 {
		t.Fatalf("mode records = %d, want 1", modes)
	}

	if err := app.SetPermissionMode(info.ID, "bogus"); err == nil {
		t.Fatal("invalid mode must fail")
	}
}

func TestLoadConfig(t *testing.T) {
	env := map[string]string{"ANC_CONFIG": filepath.Join(t.TempDir(), "missing.json")}
	cfg, err := LoadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("LoadConfig defaults: %v", err)
	}
	if cfg.Provider != defaultProvider {
		t.Fatalf("provider = %q", cfg.Provider)
	}
	if cfg.PermissionMode != string(permission.ModeDefault) {
		t.Fatalf("permission mode = %q", cfg.PermissionMode)
	}
	if cfg.ContextWindow != defaultContextWindow || cfg.MaxOutputTokens != defaultMaxOutputTokens {
		t.Fatalf("budget defaults = %+v", cfg)
	}
	if cfg.MaxTurns != defaultMaxTurns || cfg.MaxToolConcurrency != defaultMaxToolConcurrency {
		t.Fatalf("limits = %+v", cfg)
	}
	if !filepath.IsAbs(cfg.WorkDir) || !filepath.IsAbs(cfg.DataDir) {
		t.Fatalf("dirs must be absolute: %+v", cfg)
	}

	env["ANC_MAX_TURNS"] = "not-a-number"
	if _, err := LoadConfig(func(key string) string { return env[key] }); err == nil {
		t.Fatal("invalid ANC_MAX_TURNS must fail")
	}

	env = map[string]string{
		"ANC_CONFIG":   filepath.Join(t.TempDir(), "missing.json"),
		"ANC_PROVIDER": "bogus",
	}
	if _, err := LoadConfig(func(key string) string { return env[key] }); !errors.Is(err, ErrInvalidProviderConfig) {
		t.Fatalf("invalid provider error = %v", err)
	}
}

// TestLoadConfigFromFile 验证配置文件生效，且 ANC_* 环境变量覆盖同名字段。
func TestLoadConfigFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{
		"provider": "openai",
		"apiKey": "sk-from-file",
		"baseUrl": "http://127.0.0.1:9999",
		"model": "model-from-file",
		"permissionMode": "plan",
		"dataDir": "` + filepath.Join(dir, "data") + `",
		"workDir": "` + filepath.Join(dir, "work") + `",
		"contextWindow": 32000,
		"maxOutputTokens": 1024,
		"maxTurns": 7,
		"maxToolConcurrency": 3
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	env := map[string]string{"ANC_CONFIG": path}
	cfg, err := LoadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Provider != "openai" || cfg.APIKey != "sk-from-file" || cfg.BaseURL != "http://127.0.0.1:9999" {
		t.Fatalf("provider fields = %+v", cfg)
	}
	if cfg.Model != "model-from-file" || cfg.PermissionMode != "plan" {
		t.Fatalf("model fields = %+v", cfg)
	}
	if cfg.ContextWindow != 32000 || cfg.MaxOutputTokens != 1024 || cfg.MaxTurns != 7 || cfg.MaxToolConcurrency != 3 {
		t.Fatalf("limits = %+v", cfg)
	}

	env["ANC_MODEL"] = "model-from-env"
	env["ANC_MAX_TURNS"] = "9"
	cfg, err = LoadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("LoadConfig with env override: %v", err)
	}
	if cfg.Model != "model-from-env" || cfg.MaxTurns != 9 {
		t.Fatalf("env override not applied: %+v", cfg)
	}
	if cfg.APIKey != "sk-from-file" {
		t.Fatalf("file value must survive unrelated env overrides: %+v", cfg)
	}
}

// TestLoadConfigInvalidFile 验证配置文件损坏时返回错误，但仍给出可用默认值。
func TestLoadConfigInvalidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"provider":`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	env := map[string]string{"ANC_CONFIG": path}
	cfg, err := LoadConfig(func(key string) string { return env[key] })
	if err == nil {
		t.Fatal("corrupt config must return an error")
	}
	if cfg.Provider != defaultProvider {
		t.Fatalf("defaults must still apply, got provider %q", cfg.Provider)
	}
}

// TestCorruptTailKeepsSessionUsable 验证 transcript 末尾损坏时，
// LoadSession 与 runtimeForSend 使用 Store 返回的可用记录继续工作。
func TestCorruptTailKeepsSessionUsable(t *testing.T) {
	dir := t.TempDir()
	store, err := session.NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	at := time.Unix(1700000000, 0)
	if err := store.CreateSession(ctx, session.Meta{ID: "s1", WorkDir: dir, CreatedAt: at}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	msg := &llm.Message{
		ID:     "m1",
		Role:   llm.RoleUser,
		Blocks: []llm.ContentBlock{{Type: llm.BlockText, Text: "hi"}},
		Time:   at.Add(time.Second),
	}
	if err := store.Append(ctx, session.Record{
		Type:      session.RecordMessage,
		SessionID: "s1",
		Time:      msg.Time,
		Message:   msg,
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "sessions", "s1.jsonl"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	if _, err := f.WriteString(`{"type":"message","sessionId":"s1"`); err != nil {
		t.Fatalf("write truncated line: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}

	collector := newEventCollector()
	factory := func(Config) (llm.Provider, error) { return newFakeProvider(), nil }
	app, err := NewWithDeps(testConfig(t), collector.send, store, factory)
	if err != nil {
		t.Fatalf("NewWithDeps: %v", err)
	}

	view, err := app.LoadSession("s1")
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if len(view.Messages) != 1 || view.Messages[0].Blocks[0].Text != "hi" {
		t.Fatalf("LoadSession messages = %+v, want 1 usable message", view.Messages)
	}
	if _, err := app.runtimeForSend("s1"); err != nil {
		t.Fatalf("runtimeForSend: %v", err)
	}
}
