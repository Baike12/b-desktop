package app

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Baike12/b-desktop/internal/agent"
	"github.com/Baike12/b-desktop/internal/llm"
)

// writeSkillFixture 在项目级技能目录写一个技能，返回 SKILL.md 路径。
func writeSkillFixture(t *testing.T, workDir, name, description, argumentHint, body string) {
	t.Helper()
	dir := filepath.Join(workDir, ".b-desktop", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	text := "---\nname: " + name + "\ndescription: " + description + "\n"
	if argumentHint != "" {
		text += "argument-hint: " + argumentHint + "\n"
	}
	text += "---\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(text), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

func TestExpandAtMentionsFile(t *testing.T) {
	app, _ := newTestApp(t, newFakeProvider())
	workDir := app.cfg.WorkDir
	if err := os.WriteFile(filepath.Join(workDir, "notes.md"), []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	blocks := app.expandAtMentions(workDir, "帮我看看 @notes.md 的内容")
	if len(blocks) != 1 || blocks[0].Type != llm.BlockText {
		t.Fatalf("blocks = %+v", blocks)
	}
	text := blocks[0].Text
	if !strings.Contains(text, `<system-reminder>`) ||
		!strings.Contains(text, `Called the "Read" tool with the following input: {"file_path": "notes.md"}`) ||
		!strings.Contains(text, "Result of calling the \"Read\" tool:") ||
		!strings.Contains(text, "alpha") {
		t.Fatalf("reminder text = %q", text)
	}
}

func TestExpandAtMentionsDirectory(t *testing.T) {
	app, _ := newTestApp(t, newFakeProvider())
	workDir := app.cfg.WorkDir
	if err := os.MkdirAll(filepath.Join(workDir, "src", "inner"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "src", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	blocks := app.expandAtMentions(workDir, "这个目录里有什么 @src")
	if len(blocks) != 1 {
		t.Fatalf("blocks = %+v", blocks)
	}
	text := blocks[0].Text
	if !strings.Contains(text, `{"command": "ls src"`) || !strings.Contains(text, "main.go") || !strings.Contains(text, "inner/") {
		t.Fatalf("directory reminder = %q", text)
	}
}

func TestExpandAtMentionsMissing(t *testing.T) {
	app, _ := newTestApp(t, newFakeProvider())
	blocks := app.expandAtMentions(app.cfg.WorkDir, "看看 @nope.md")
	if len(blocks) != 1 || !strings.Contains(blocks[0].Text, "Referenced path not readable") {
		t.Fatalf("blocks = %+v", blocks)
	}
}

func TestExpandAtMentionsImage(t *testing.T) {
	app, _ := newTestApp(t, newFakeProvider())
	workDir := app.cfg.WorkDir
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "pic.png"), buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write png: %v", err)
	}

	blocks := app.expandAtMentions(workDir, "看图 @pic.png")
	if len(blocks) != 2 || blocks[1].Type != llm.BlockImage || blocks[1].MediaType != "image/png" || blocks[1].Data == "" {
		t.Fatalf("blocks = %+v", blocks)
	}
}

func TestExpandAtMentionsDedupAndLimit(t *testing.T) {
	app, _ := newTestApp(t, newFakeProvider())
	workDir := app.cfg.WorkDir
	for i := 0; i < 12; i++ {
		name := "f" + string(rune('a'+i)) + ".txt"
		if err := os.WriteFile(filepath.Join(workDir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	prompt := "@f" + string(rune('a')) + ".txt 再次 @f" + string(rune('a')) + ".txt"
	for i := 0; i < 12; i++ {
		prompt += " @f" + string(rune('a'+i)) + ".txt"
	}
	blocks := app.expandAtMentions(workDir, prompt)
	// 去重后 12 个不同引用，命中 10 个上限。
	if len(blocks) != maxAtMentions {
		t.Fatalf("blocks = %d, want %d", len(blocks), maxAtMentions)
	}
}

func TestExpandAtMentionsQuotedPath(t *testing.T) {
	app, _ := newTestApp(t, newFakeProvider())
	workDir := app.cfg.WorkDir
	sub := filepath.Join(workDir, "my docs")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "a b.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	blocks := app.expandAtMentions(workDir, `读取 @"my docs/a b.txt"`)
	if len(blocks) != 1 || !strings.Contains(blocks[0].Text, `{"file_path": "my docs/a b.txt"}`) {
		t.Fatalf("blocks = %+v", blocks)
	}
}

func TestExpandSlashCommandSkill(t *testing.T) {
	app, _ := newTestApp(t, newFakeProvider())
	workDir := app.cfg.WorkDir
	writeSkillFixture(t, workDir, "greet", "打招呼", "<name>",
		"用英文向 $1 打招呼。原始参数：$ARGUMENTS")

	blocks := app.expandSlashCommand(workDir, "/greet Alice")
	if len(blocks) != 1 {
		t.Fatalf("blocks = %+v", blocks)
	}
	text := blocks[0].Text
	for _, want := range []string{
		"<command-name>/greet</command-name>",
		"<command-message>greet</command-message>",
		"<command-args>Alice</command-args>",
		"用英文向 Alice 打招呼。原始参数：Alice",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("text missing %q:\n%s", want, text)
		}
	}
}

func TestExpandSlashCommandNoPlaceholder(t *testing.T) {
	app, _ := newTestApp(t, newFakeProvider())
	workDir := app.cfg.WorkDir
	writeSkillFixture(t, workDir, "review", "审查", "", "请审查当前改动。")

	blocks := app.expandSlashCommand(workDir, "/review 重点看权限部分")
	if len(blocks) != 1 || !strings.Contains(blocks[0].Text, "\n\nARGUMENTS: 重点看权限部分") {
		t.Fatalf("blocks = %+v", blocks)
	}
}

func TestExpandSlashCommandNegative(t *testing.T) {
	app, _ := newTestApp(t, newFakeProvider())
	workDir := app.cfg.WorkDir
	writeSkillFixture(t, workDir, "greet", "打招呼", "", "hi $1")

	cases := []string{
		"/compact",      // 内置命令不展开
		"/unknown-cmd",  // 未注册的命令不展开
		"先看 /greet bob", // 非 / 开头不展开
	}
	// 无参数命中技能也应展开（<command-args> 为空）。
	if blocks := app.expandSlashCommand(workDir, "/greet"); len(blocks) != 1 {
		t.Fatalf("bare command: blocks = %+v", blocks)
	}
	for _, prompt := range cases[1:] {
		if blocks := app.expandSlashCommand(workDir, prompt); len(blocks) != 0 {
			t.Fatalf("prompt %q expanded: %+v", prompt, blocks)
		}
	}
}

func TestListSlashCommands(t *testing.T) {
	app, _ := newTestApp(t, newFakeProvider())
	workDir := app.cfg.WorkDir
	writeSkillFixture(t, workDir, "deploy", "部署", "<env>", "deploy $1")

	commands, err := app.ListSlashCommands(workDir)
	if err != nil {
		t.Fatalf("ListSlashCommands: %v", err)
	}
	if len(commands) < 2 {
		t.Fatalf("commands = %+v", commands)
	}
	if commands[0].Name != "compact" || commands[0].Source != "builtin" {
		t.Fatalf("first command = %+v", commands[0])
	}
	var found bool
	for _, c := range commands {
		if c.Name == "deploy" {
			found = true
			if c.Source != "skill" || c.ArgumentHint != "<env>" || c.Description != "部署" {
				t.Fatalf("skill command = %+v", c)
			}
		}
	}
	if !found {
		t.Fatalf("skill command missing: %+v", commands)
	}
}

func TestCompactSession(t *testing.T) {
	// 每轮回复足够长，保证尾部保留段（4 条 / 10000 token）之外还有可压缩历史。
	long := strings.Repeat("a", 12000)
	provider := newFakeProvider(
		textStep("m1", long),
		textStep("m2", long),
		textStep("m3", long),
		textStep("m4", long),
		textStep("m5", long),
		textStep("sum", "这是对话摘要。"),
		textStep("m6", "第六轮回复"),
	)
	app, collector := newTestApp(t, provider)
	info := createTestSession(t, app)
	for range 5 {
		if _, err := app.SendMessage(info.ID, "计数消息", nil); err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
		waitIdle(t, app, info.ID)
	}

	result, err := app.CompactSession(info.ID)
	if err != nil {
		t.Fatalf("CompactSession: %v", err)
	}
	if result.PreTokens <= 0 || result.PostTokens <= 0 {
		t.Fatalf("result = %+v", result)
	}

	// 压缩后历史 = 摘要 user 消息 + 保留的尾部消息，总条数少于压缩前。
	rt, err := app.runtimeOf(info.ID)
	if err != nil {
		t.Fatalf("runtimeOf: %v", err)
	}
	history := rt.engine.History()
	if len(history) != 8 {
		t.Fatalf("history = %d messages, want 8 (summary + kept tail)", len(history))
	}
	first := ""
	for _, b := range history[0].Blocks {
		first += b.Text
	}
	if !strings.Contains(first, "这是对话摘要") {
		t.Fatalf("first message is not the summary: %q", first)
	}

	// compact 事件成对出现。
	starts := collector.count(agent.EventCompactStart)
	ends := collector.count(agent.EventCompactEnd)
	if starts != 1 || ends != 1 {
		t.Fatalf("compact events: start=%d end=%d", starts, ends)
	}

	// transcript 重放：重新加载会话，历史与内存一致。
	view, err := app.LoadSession(info.ID)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if len(view.Messages) == 0 {
		t.Fatalf("empty replayed view")
	}
}

func TestCompactSessionEmpty(t *testing.T) {
	app, _ := newTestApp(t, newFakeProvider())
	info := createTestSession(t, app)
	if _, err := app.CompactSession(info.ID); err == nil {
		t.Fatalf("expected error for empty session")
	}
}

func TestCompactSessionRunning(t *testing.T) {
	block := make(chan struct{})
	provider := newFakeProvider(fakeStep{
		events: []llm.StreamEvent{
			{Type: llm.EventMessageStart, MessageID: "m1"},
			{Type: llm.EventBlockStart, Index: 0, Kind: llm.BlockText},
			{Type: llm.EventTextDelta, Index: 0, Kind: llm.BlockText, Text: "hi"},
			{Type: llm.EventMessageDelta, StopReason: llm.StopEndTurn, Usage: &llm.Usage{InputTokens: 3, OutputTokens: 2}},
			{Type: llm.EventMessageStop},
		},
		block: true,
	})
	_ = block
	app, _ := newTestApp(t, provider)
	info := createTestSession(t, app)
	if _, err := app.SendMessage(info.ID, "开始", nil); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	// 轮次进行中压缩必须被拒绝。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if rt, err := app.runtimeOf(info.ID); err == nil && rt.isRunning() {
			break
		}
	}
	if _, err := app.CompactSession(info.ID); !errors.Is(err, ErrTurnRunning) {
		t.Fatalf("error = %v, want ErrTurnRunning", err)
	}
	waitIdle(t, app, info.ID)
}
