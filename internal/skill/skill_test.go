package skill

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Baike12/b-desktop/internal/agent"
	"github.com/Baike12/b-desktop/internal/tool"
)

// Loader 必须可注入 agent 的上下文提供者列表。
var _ agent.ContextProvider = (*Loader)(nil)

// writeSkill 创建 <dir>/<name>/SKILL.md。
func writeSkill(t *testing.T, dir, name, content string) {
	t.Helper()
	sub := filepath.Join(dir, name)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", sub, err)
	}
	if err := os.WriteFile(filepath.Join(sub, SkillFileName), []byte(content), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
}

func newTestLoader(t *testing.T, userDir, projectDir string) *Loader {
	t.Helper()
	dirs := []string{userDir}
	sources := []string{SourceUser}
	if projectDir != "" {
		dirs = append(dirs, projectDir)
		sources = append(sources, SourceProject)
	}
	l, err := NewLoaderAt(dirs, sources)
	if err != nil {
		t.Fatalf("NewLoaderAt: %v", err)
	}
	return l
}

func TestLoaderParsesFrontmatterAndSorts(t *testing.T) {
	userDir := t.TempDir()
	writeSkill(t, userDir, "zeta", "---\nname: zeta\ndescription: 处理压缩包\n---\n\n正文 Z\n")
	writeSkill(t, userDir, "alpha", "---\nname: alpha\ndescription: \"生成图表\"\nignored: yes\n---\n\n正文 A\n")

	l := newTestLoader(t, userDir, "")
	list := l.List()
	if len(list) != 2 {
		t.Fatalf("list len = %d, want 2", len(list))
	}
	if list[0].Name != "alpha" || list[1].Name != "zeta" {
		t.Fatalf("order = %q,%q, want alpha,zeta", list[0].Name, list[1].Name)
	}
	if list[0].Description != "生成图表" {
		t.Fatalf("description = %q", list[0].Description)
	}
	if !strings.HasSuffix(list[0].Path, filepath.Join("alpha", SkillFileName)) {
		t.Fatalf("path = %q", list[0].Path)
	}
	if list[0].Source != SourceUser {
		t.Fatalf("source = %q", list[0].Source)
	}
}

func TestLoaderFallsBackToDirectoryAndInlineDescription(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "no-frontmatter", "# 标题\n\n处理日志的说明。\n")
	l := newTestLoader(t, dir, "")
	s, ok := l.Lookup("no-frontmatter")
	if !ok {
		t.Fatal("skill not loaded")
	}
	if s.Description != "处理日志的说明。" {
		t.Fatalf("description = %q", s.Description)
	}

	writeSkill(t, dir, "partial", "---\nname: partial\n---\n\n# 标题\n\n从正文补描述。\n")
	if err := l.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	s, ok = l.Lookup("partial")
	if !ok {
		t.Fatal("partial skill not loaded")
	}
	if s.Description != "从正文补描述。" {
		t.Fatalf("partial description = %q", s.Description)
	}
}

func TestLoaderProjectOverridesUser(t *testing.T) {
	userDir := t.TempDir()
	projectDir := t.TempDir()
	writeSkill(t, userDir, "review", "---\nname: review\ndescription: 用户级\n---\n\n用户正文\n")
	writeSkill(t, projectDir, "review-dir", "---\nname: review\ndescription: 项目级\n---\n\n项目正文\n")

	l := newTestLoader(t, userDir, projectDir)
	s, ok := l.Lookup("review")
	if !ok {
		t.Fatal("skill not loaded")
	}
	if s.Description != "项目级" || s.Source != SourceProject {
		t.Fatalf("skill = %+v, want project override", s)
	}
	body, err := l.Body(context.Background(), "review")
	if err != nil {
		t.Fatalf("Body: %v", err)
	}
	if !strings.Contains(body, "项目正文") || strings.Contains(body, "---") {
		t.Fatalf("body = %q", body)
	}
}

func TestLoaderMissingDirectoriesAreEmpty(t *testing.T) {
	l, err := NewLoaderAt([]string{filepath.Join(t.TempDir(), "nope")}, []string{SourceUser})
	if err != nil {
		t.Fatalf("NewLoaderAt: %v", err)
	}
	if got := l.List(); len(got) != 0 {
		t.Fatalf("list = %v, want empty", got)
	}
	if got := l.ExtraContext(context.Background(), "s", ".", "hi"); got != "" {
		t.Fatalf("extra context = %q, want empty", got)
	}
}

func TestLoaderBodyNotFoundAndEmptyName(t *testing.T) {
	l := newTestLoader(t, t.TempDir(), "")
	if _, err := l.Body(context.Background(), "missing"); !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("err = %v, want ErrSkillNotFound", err)
	}
	if _, err := l.Body(context.Background(), "   "); !errors.Is(err, ErrEmptyName) {
		t.Fatalf("err = %v, want ErrEmptyName", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := l.Body(ctx, "any"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestLoaderReloadPicksUpNewSkill(t *testing.T) {
	dir := t.TempDir()
	l := newTestLoader(t, dir, "")
	if len(l.List()) != 0 {
		t.Fatal("expected empty loader")
	}
	writeSkill(t, dir, "later", "---\nname: later\ndescription: 后加\n---\n\n正文\n")
	if err := l.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if len(l.List()) != 1 {
		t.Fatalf("list len = %d, want 1", len(l.List()))
	}
}

func TestExtraContextListsNamesWithoutBodiesAndCapsAt50(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 55; i++ {
		name := "s" + strings.Repeat("x", 2) + string(rune('a'+i%26)) + string(rune('a'+i/26))
		writeSkill(t, dir, name, "---\nname: "+name+"\ndescription: 描述-"+name+"\n---\n\nSECRET-BODY-"+name+"\n")
	}
	l := newTestLoader(t, dir, "")
	text := l.ExtraContext(context.Background(), "sess", ".", "prompt")
	if strings.Contains(text, "SECRET-BODY") {
		t.Fatal("extra context must not include skill bodies")
	}
	if got := strings.Count(text, "\n- "); got != 50 {
		t.Fatalf("listed skills = %d, want 50", got)
	}
	if !strings.Contains(text, "## 可用技能") || !strings.Contains(text, "描述-") {
		t.Fatalf("unexpected context: %q", text)
	}
}

func TestSkillToolReturnsBody(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "deploy", "---\nname: deploy\ndescription: 发布流程\n---\n\n# 发布\n\n执行 make release。\n")
	l := newTestLoader(t, dir, "")
	tools := l.Tools()
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(tools))
	}
	tl := tools[0]
	if tl.Name() != ToolNameSkill {
		t.Fatalf("name = %q", tl.Name())
	}
	if !tl.ReadOnly(nil) || !tl.ConcurrencySafe(nil) {
		t.Fatal("skill tool must be read-only and concurrency safe")
	}
	var schema map[string]any
	if err := json.Unmarshal(tl.InputSchema(), &schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	var reported []tool.Progress
	res, err := tl.Run(context.Background(), json.RawMessage(`{"name":"deploy"}`), func(p tool.Progress) {
		reported = append(reported, p)
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Content, "执行 make release。") || strings.Contains(res.Content, "description:") {
		t.Fatalf("content = %q", res.Content)
	}
	if res.Meta["skill"] != "deploy" {
		t.Fatalf("meta = %v", res.Meta)
	}
	if len(reported) != 1 {
		t.Fatalf("progress = %v", reported)
	}
}

func TestSkillToolRejectsUnknownAndBadInput(t *testing.T) {
	l := newTestLoader(t, t.TempDir(), "")
	tl := l.Tools()[0]
	if _, err := tl.Run(context.Background(), json.RawMessage(`{"name":"nope"}`), nil); !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("err = %v, want ErrSkillNotFound", err)
	}
	if _, err := tl.Run(context.Background(), json.RawMessage(`{`), nil); err == nil {
		t.Fatal("expected decode error")
	}
	if _, err := tl.Run(context.Background(), json.RawMessage(`{"name":"  "}`), nil); !errors.Is(err, ErrEmptyName) {
		t.Fatalf("err = %v, want ErrEmptyName", err)
	}
}
