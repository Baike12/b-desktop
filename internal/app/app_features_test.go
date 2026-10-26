package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Baike12/b-desktop/internal/llm"
	"github.com/Baike12/b-desktop/internal/session"
)

// writeCommandFixture 在项目命令目录写一个 .md 命令。
func writeCommandFixture(t *testing.T, workDir, name, description, argumentHint, body string) {
	t.Helper()
	dir := filepath.Join(workDir, ".b-desktop", "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir command dir: %v", err)
	}
	text := "---\ndescription: " + description + "\n"
	if argumentHint != "" {
		text += "argument-hint: " + argumentHint + "\n"
	}
	text += "---\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(text), 0o644); err != nil {
		t.Fatalf("write command file: %v", err)
	}
}

// TestModelInfoOf 表内前缀、视觉关键词与纯文本关键词的判定。
func TestModelInfoOf(t *testing.T) {
	cases := []struct {
		model  string
		images bool
	}{
		{"claude-sonnet-4-20250514", true},
		{"gpt-4o-mini", true},
		{"deepseek-chat", false},
		{"deepseek-reasoner-v2", false},
		{"qwen3-max", false},
		{"qwen-vl-max", true},
		{"glm-4v-plus", true},
		{"totally-unknown-model", true},
	}
	for _, tc := range cases {
		if got := llm.ModelInfoOf(tc.model).SupportsImages; got != tc.images {
			t.Fatalf("ModelInfoOf(%q).SupportsImages = %v, want %v", tc.model, got, tc.images)
		}
	}
}

// TestSendMessageRejectsImagesForTextModel 不支持视觉的模型直接拒绝图片输入。
func TestSendMessageRejectsImagesForTextModel(t *testing.T) {
	cfg := testConfig(t)
	cfg.Model = "deepseek-chat"
	provider := newFakeProvider(textStep("m1", "ok"))
	application, _ := newTestAppWithConfig(t, cfg, provider)
	info := createTestSession(t, application)

	_, err := application.SendMessage(info.ID, "看图", []ImageInput{{MediaType: "image/png", Data: "aGk="}})
	if !errors.Is(err, ErrModelNoImageSupport) {
		t.Fatalf("err = %v, want ErrModelNoImageSupport", err)
	}
}

// TestExpandAtMentionsLineRange #L 范围转成 Read 的 offset/limit。
func TestExpandAtMentionsLineRange(t *testing.T) {
	cfg := testConfig(t)
	readme := filepath.Join(cfg.WorkDir, "readme.md")
	body := strings.Repeat("line\n", 30)
	if err := os.WriteFile(readme, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	application, err := NewWithDeps(cfg, func(context.Context, string, any) {}, session.NewMemStore(), nil)
	if err != nil {
		t.Fatalf("NewWithDeps: %v", err)
	}

	blocks := application.expandAtMentions(cfg.WorkDir, "看这段 @readme.md#L10-20")
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	text := blocks[0].Text
	if !strings.Contains(text, `"offset":9`) || !strings.Contains(text, `"limit":11`) {
		t.Fatalf("range not applied: %s", text)
	}
	if !strings.Contains(text, "10\tline") {
		t.Fatalf("expected line 10 in rendered content, got: %s", text)
	}
	// 非法范围（0 行号）按字面路径处理（与 cc 一致）：读不到即注入提示块。
	blocks = application.expandAtMentions(cfg.WorkDir, "@readme.md#L0")
	if len(blocks) != 1 || !strings.Contains(blocks[0].Text, "Referenced path not readable") {
		t.Fatalf("invalid range should fall back to literal path, got %+v", blocks)
	}
}

// TestListSlashCommandsIncludesCommands 命令目录与内置命令出现在 / 面板。
func TestListSlashCommandsIncludesCommands(t *testing.T) {
	cfg := testConfig(t)
	writeSkillFixture(t, cfg.WorkDir, "review", "审查代码", "<文件>", "审查 %s")
	writeCommandFixture(t, cfg.WorkDir, "deploy", "部署到测试环境", "<env>", "部署到 $1")
	application, err := NewWithDeps(cfg, func(context.Context, string, any) {}, session.NewMemStore(), nil)
	if err != nil {
		t.Fatalf("NewWithDeps: %v", err)
	}

	list, err := application.ListSlashCommands(cfg.WorkDir)
	if err != nil {
		t.Fatalf("ListSlashCommands: %v", err)
	}
	byName := map[string]SlashCommandDTO{}
	for _, item := range list {
		byName[item.Name] = item
	}
	for _, name := range []string{"compact", "clear", "model", "export"} {
		if byName[name].Source != "builtin" {
			t.Fatalf("builtin %s missing: %+v", name, byName[name])
		}
	}
	if byName["deploy"].Source != "command" || byName["deploy"].ArgumentHint != "<env>" {
		t.Fatalf("command deploy = %+v", byName["deploy"])
	}
	if byName["review"].Source != "skill" {
		t.Fatalf("skill review = %+v", byName["review"])
	}
}

// TestExpandSlashCommandPrefersCommands 同名时命令目录覆盖技能。
func TestExpandSlashCommandPrefersCommands(t *testing.T) {
	cfg := testConfig(t)
	writeSkillFixture(t, cfg.WorkDir, "greet", "技能版", "", "SKILL-BODY")
	writeCommandFixture(t, cfg.WorkDir, "greet", "命令版", "", "COMMAND-BODY $ARGUMENTS")
	application, err := NewWithDeps(cfg, func(context.Context, string, any) {}, session.NewMemStore(), nil)
	if err != nil {
		t.Fatalf("NewWithDeps: %v", err)
	}

	blocks := application.expandSlashCommand(cfg.WorkDir, "/greet world")
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	text := blocks[0].Text
	if !strings.Contains(text, "COMMAND-BODY world") || strings.Contains(text, "SKILL-BODY") {
		t.Fatalf("command should win over skill: %s", text)
	}
	// 内置命令不展开。
	if blocks := application.expandSlashCommand(cfg.WorkDir, "/clear"); len(blocks) != 0 {
		t.Fatalf("builtin /clear should not expand, got %+v", blocks)
	}
}

// TestRewindSession 两轮对话回溯到第一条：transcript 截断、内容回填、引擎重建。
func TestRewindSession(t *testing.T) {
	provider := newFakeProvider(textStep("m1", "回复一"), textStep("m2", "回复二"))
	application, _ := newTestApp(t, provider)
	info := createTestSession(t, application)

	if _, err := application.SendMessage(info.ID, "第一条", nil); err != nil {
		t.Fatalf("SendMessage 1: %v", err)
	}
	waitIdle(t, application, info.ID)
	if _, err := application.SendMessage(info.ID, "第二条", nil); err != nil {
		t.Fatalf("SendMessage 2: %v", err)
	}
	waitIdle(t, application, info.ID)

	points, err := application.ListRewindPoints(info.ID)
	if err != nil {
		t.Fatalf("ListRewindPoints: %v", err)
	}
	if len(points) != 2 || points[0].Preview != "第一条" || points[1].Preview != "第二条" {
		t.Fatalf("points = %+v", points)
	}

	result, err := application.RewindSession(info.ID, points[0].MessageID)
	if err != nil {
		t.Fatalf("RewindSession: %v", err)
	}
	if result.Text != "第一条" {
		t.Fatalf("rewound text = %q", result.Text)
	}
	view, err := application.LoadSession(info.ID)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if len(view.Messages) != 0 {
		t.Fatalf("messages after rewind = %d, want 0", len(view.Messages))
	}
	// 回溯后继续对话：引擎按截断后的历史重建，首轮请求只有新输入。
	if _, err := application.SendMessage(info.ID, "重新开始", nil); err != nil {
		t.Fatalf("SendMessage after rewind: %v", err)
	}
	waitIdle(t, application, info.ID)
	requests := provider.requests()
	last := requests[len(requests)-1]
	if len(last.Messages) != 1 {
		t.Fatalf("rebuilt request has %d messages, want 1 (fresh history)", len(last.Messages))
	}
}

// TestRewindSessionRunningRejected 运行中回溯被拒绝。
func TestRewindSessionRunningRejected(t *testing.T) {
	blocking := fakeStep{block: true, events: []llm.StreamEvent{
		{Type: llm.EventMessageStart, MessageID: "m1"},
		{Type: llm.EventBlockStart, Index: 0, Kind: llm.BlockText},
		{Type: llm.EventTextDelta, Index: 0, Kind: llm.BlockText, Text: "ing"},
	}}
	application, _ := newTestApp(t, newFakeProvider(blocking))
	info := createTestSession(t, application)
	if _, err := application.SendMessage(info.ID, "第一条", nil); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	// 消息由轮次 goroutine 异步落盘：轮询直到锚点可见。
	var points []RewindPointDTO
	var err error
	for i := 0; i < 200; i++ {
		points, err = application.ListRewindPoints(info.ID)
		if err == nil && len(points) == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err != nil || len(points) != 1 {
		t.Fatalf("points = %+v err = %v", points, err)
	}
	_, err = application.RewindSession(info.ID, points[0].MessageID)
	if !errors.Is(err, ErrTurnRunning) {
		t.Fatalf("err = %v, want ErrTurnRunning", err)
	}
	if err := application.CancelTurn(info.ID); err != nil {
		t.Fatalf("CancelTurn: %v", err)
	}
	waitIdle(t, application, info.ID)
}

// TestExportSession 导出 Markdown 并写入文件。
func TestExportSession(t *testing.T) {
	application, _ := newTestApp(t, newFakeProvider(textStep("m1", "这是回复")))
	info := createTestSession(t, application)
	if _, err := application.SendMessage(info.ID, "帮我导出", nil); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	waitIdle(t, application, info.ID)

	result, err := application.ExportSession(info.ID, "")
	if err != nil {
		t.Fatalf("ExportSession: %v", err)
	}
	if result.MessageCount != 2 {
		t.Fatalf("MessageCount = %d, want 2", result.MessageCount)
	}
	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "# 测试会话") || !strings.Contains(text, "帮我导出") || !strings.Contains(text, "这是回复") {
		t.Fatalf("markdown missing content: %s", text)
	}
	// 自定义路径。
	custom := filepath.Join(t.TempDir(), "out.md")
	again, err := application.ExportSession(info.ID, custom)
	if err != nil || again.Path != custom {
		t.Fatalf("custom export = %+v err = %v", again, err)
	}
}

// TestTodoUpdateEventFlowsThroughApp TodoWrite 的 todo_update 事件经绑定层透传。
func TestTodoUpdateEventFlowsThroughApp(t *testing.T) {
	todoCall, _ := json.Marshal(map[string]any{
		"todos": []map[string]string{{"content": "步骤", "status": "in_progress"}},
	})
	provider := newFakeProvider(
		fakeStep{events: []llm.StreamEvent{
			{Type: llm.EventMessageStart, MessageID: "m1"},
			{Type: llm.EventToolUseStart, Index: 0, Kind: llm.BlockToolUse, ToolUseID: "t1", ToolName: "TodoWrite"},
			{Type: llm.EventToolInputDelta, Index: 0, Kind: llm.BlockToolUse, PartialJSON: string(todoCall)},
			{Type: llm.EventToolUseStop, Index: 0, Kind: llm.BlockToolUse},
			{Type: llm.EventMessageDelta, StopReason: llm.StopToolUse, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 2}},
			{Type: llm.EventMessageStop},
		}},
		textStep("m2", "完成"),
	)
	application, collector := newTestApp(t, provider)
	info := createTestSession(t, application)
	if _, err := application.SendMessage(info.ID, "做计划", nil); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	waitIdle(t, application, info.ID)

	for _, ev := range collector.snapshot() {
		if ev.Type != "todo_update" {
			continue
		}
		if ev.Todos == nil || len(ev.Todos.Items) != 1 || ev.Todos.Items[0].Content != "步骤" {
			t.Fatalf("todos = %+v", ev.Todos)
		}
		return
	}
	t.Fatal("no todo_update event observed")
}

// newTestAppWithConfig 用指定配置创建应用（区分模型能力的用例）。
func newTestAppWithConfig(t *testing.T, cfg Config, provider llm.Provider) (*App, *eventCollector) {
	t.Helper()
	collector := newEventCollector()
	factory := func(Config) (llm.Provider, error) { return provider, nil }
	application, err := NewWithDeps(cfg, collector.send, session.NewMemStore(), factory)
	if err != nil {
		t.Fatalf("NewWithDeps: %v", err)
	}
	return application, collector
}

// TestForkSession 复制会话：新会话拥有完整历史、可独立继续对话，源会话不受影响。
func TestForkSession(t *testing.T) {
	provider := newFakeProvider(textStep("m1", "回复一"), textStep("m2", "分叉回复"), textStep("m3", "源继续"))
	application, _ := newTestApp(t, provider)
	info := createTestSession(t, application)
	if _, err := application.SendMessage(info.ID, "第一条", nil); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	waitIdle(t, application, info.ID)

	forked, err := application.ForkSession(info.ID)
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	if forked.ID == info.ID {
		t.Fatal("fork 返回了源会话 ID")
	}
	if forked.Title != info.Title+" (fork)" {
		t.Fatalf("fork 标题 = %q, want %q", forked.Title, info.Title+" (fork)")
	}
	srcNow, err := application.LoadSession(info.ID)
	if err != nil {
		t.Fatalf("LoadSession(src): %v", err)
	}
	if forked.Messages != len(srcNow.Messages) {
		t.Fatalf("fork messages = %d, want %d", forked.Messages, len(srcNow.Messages))
	}

	// 新会话视图与源一致，且可独立继续对话。
	view, err := application.LoadSession(forked.ID)
	if err != nil {
		t.Fatalf("LoadSession(fork): %v", err)
	}
	srcView, err := application.LoadSession(info.ID)
	if err != nil {
		t.Fatalf("LoadSession(src): %v", err)
	}
	if len(view.Messages) != len(srcView.Messages) {
		t.Fatalf("fork 消息数 = %d, want %d", len(view.Messages), len(srcView.Messages))
	}
	if messageText(view.Messages[0]) != messageText(srcView.Messages[0]) {
		t.Fatalf("fork 首条消息 = %q, want %q", messageText(view.Messages[0]), messageText(srcView.Messages[0]))
	}

	// 分叉线程独立推进，不写回源会话。
	if _, err := application.SendMessage(forked.ID, "分叉后的问题", nil); err != nil {
		t.Fatalf("SendMessage(fork): %v", err)
	}
	waitIdle(t, application, forked.ID)
	forkedView, err := application.LoadSession(forked.ID)
	if err != nil {
		t.Fatalf("reload fork: %v", err)
	}
	if len(forkedView.Messages) != len(view.Messages)+2 {
		t.Fatalf("fork 新增消息 = %d, want 2", len(forkedView.Messages)-len(view.Messages))
	}
	afterSrc, err := application.LoadSession(info.ID)
	if err != nil {
		t.Fatalf("reload src: %v", err)
	}
	if len(afterSrc.Messages) != len(srcView.Messages) {
		t.Fatalf("源会话被污染：messages = %d, want %d", len(afterSrc.Messages), len(srcView.Messages))
	}

	// 会话列表同时包含两条线程。
	sessions, err := application.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	ids := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		ids[s.ID] = true
	}
	if !ids[info.ID] || !ids[forked.ID] {
		t.Fatalf("列表缺少源/分叉会话: %v", ids)
	}

	// 源不存在时报错。
	if _, err := application.ForkSession("s_missing"); err == nil {
		t.Fatal("fork missing session should fail")
	}
}

// messageText 拼接消息 DTO 的文本块，供断言比较。
func messageText(m MessageDTO) string {
	var b strings.Builder
	for _, block := range m.Blocks {
		if block.Type == "text" {
			b.WriteString(block.Text)
		}
	}
	return b.String()
}
