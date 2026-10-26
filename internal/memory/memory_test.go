package memory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Baike12/b-desktop/internal/tool"
)

// mustTime 解析测试用时间戳。
func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse time: %v", err)
	}
	return parsed
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func saveEntry(t *testing.T, s *Store, name, description, typ, body string) Memory {
	t.Helper()
	mem, created, err := s.Save(context.Background(), EntryInput{
		Name: name, Description: description, Type: typ, Body: body,
	})
	if err != nil {
		t.Fatalf("Save %s: %v", name, err)
	}
	if !created {
		t.Fatalf("Save %s: want created", name)
	}
	return mem
}

func TestNewStoreCreatesIndex(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "memory")
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if s.Dir() != dir {
		t.Fatalf("dir = %q, want %q", s.Dir(), dir)
	}
	data, err := os.ReadFile(filepath.Join(dir, IndexFileName))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if !strings.Contains(string(data), "# 记忆索引") {
		t.Fatalf("index = %q", data)
	}
	if _, err := NewStore("  "); err == nil {
		t.Fatal("expected error for empty dir")
	}
}

func TestDefaultDirUnderSettings(t *testing.T) {
	dir, err := DefaultDir()
	if err != nil {
		t.Fatalf("DefaultDir: %v", err)
	}
	if !strings.HasSuffix(filepath.ToSlash(dir), "/.b-desktop/memory") {
		t.Fatalf("dir = %q", dir)
	}
}

func TestSaveRoundTripFullFields(t *testing.T) {
	s := newTestStore(t)
	created := mustTime(t, "2026-01-02T08:00:00+08:00")
	in := EntryInput{
		Name:        "回复风格",
		Description: "用户偏好简洁回答",
		Type:        "feedback",
		Keywords:    []string{"风格", "简洁"},
		Importance:  8,
		Body:        "不要总结，用户自己看 diff。",
	}
	mem, ok, err := s.Save(context.Background(), in)
	if err != nil || !ok {
		t.Fatalf("Save: %v created=%v", err, ok)
	}
	// 时间戳与访问计数由 Save 写入；这里覆盖为固定值以验证序列化往返。
	mem.CreatedAt = created
	mem.UpdatedAt = created
	mem.LastAccessedAt = created
	mem.AccessCount = 3
	if err := os.WriteFile(filepath.Join(s.Dir(), mem.FileName), []byte(serializeMemory(mem)), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(s.Dir(), mem.FileName))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, want := range []string{
		"name: 回复风格", "description: 用户偏好简洁回答", "type: feedback",
		"keywords: [风格, 简洁]", "importance: 8",
		"createdAt: 2026-01-02T08:00:00+08:00", "accessCount: 3", "hash: ",
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("file missing %q:\n%s", want, data)
		}
	}

	got, err := s.Get(mem.FileName)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != in.Name || got.Description != in.Description || got.Type != "feedback" {
		t.Fatalf("got = %+v", got)
	}
	if len(got.Keywords) != 2 || got.Importance != 8 || got.Status != StatusActive {
		t.Fatalf("got = %+v", got)
	}
	if !got.CreatedAt.Equal(created) || !got.UpdatedAt.Equal(created) {
		t.Fatalf("timestamps = %+v", got)
	}
	if got.Hash == "" || got.Hash != bodyHash(in.Body) {
		t.Fatalf("hash = %q", got.Hash)
	}
}

func TestParseLegacyFrontmatter(t *testing.T) {
	// v1 文件只有 name/description/type，缺省字段必须可用。
	dir := t.TempDir()
	legacy := "---\nname: 旧记忆\ndescription: 旧版格式\ntype: user\n---\n\n旧正文\n"
	if err := os.WriteFile(filepath.Join(dir, "旧记忆.md"), []byte(legacy), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Name != "旧记忆" || list[0].Type != "user" {
		t.Fatalf("list = %+v", list)
	}
	if list[0].Status != StatusActive || list[0].Importance != 0 {
		t.Fatalf("defaults wrong: %+v", list[0])
	}
}

func TestSaveDefaultsAndValidation(t *testing.T) {
	s := newTestStore(t)
	mem, _, err := s.Save(context.Background(), EntryInput{Name: "事实", Body: "正文第一行\n第二行"})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if mem.Type != "project" || mem.Description != "正文第一行" {
		t.Fatalf("mem = %+v", mem)
	}
	cases := []struct {
		name string
		in   EntryInput
		want error
	}{
		{"empty name", EntryInput{Name: " ", Body: "b"}, ErrEmptyName},
		{"empty body", EntryInput{Name: "n", Body: "  "}, ErrEmptyBody},
		{"bad type", EntryInput{Name: "n", Type: "Bad Type", Body: "b"}, ErrInvalidType},
		{"bad importance", EntryInput{Name: "n", Importance: 11, Body: "b"}, ErrInvalidImportance},
	}
	for _, tc := range cases {
		if _, _, err := s.Save(context.Background(), tc.in); !errors.Is(err, tc.want) {
			t.Fatalf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestSaveIdempotentByHash(t *testing.T) {
	s := newTestStore(t)
	first, created, err := s.Save(context.Background(), EntryInput{Name: "偏好", Body: "深色主题"})
	if err != nil || !created {
		t.Fatalf("first save: %v created=%v", err, created)
	}
	second, created, err := s.Save(context.Background(), EntryInput{Name: "偏好二", Body: "深色主题"})
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	if created {
		t.Fatal("same body must dedupe")
	}
	if second.FileName != first.FileName {
		t.Fatalf("dedupe returned %q, want existing %q", second.FileName, first.FileName)
	}
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list len = %d, want 1", len(list))
	}
}

func TestSaveDeduplicatesFileNames(t *testing.T) {
	s := newTestStore(t)
	first := saveEntry(t, s, "same name", "d", "project", "a")
	second, created, err := s.Save(context.Background(), EntryInput{Name: "same name", Body: "b"})
	if err != nil || !created {
		t.Fatalf("Save: %v created=%v", err, created)
	}
	if first.FileName == second.FileName {
		t.Fatalf("file names collide: %q", first.FileName)
	}
	if second.FileName != "same-name-2.md" {
		t.Fatalf("second = %q", second.FileName)
	}
}

func TestUpdateMergeAndNoOp(t *testing.T) {
	s := newTestStore(t)
	mem := saveEntry(t, s, "发布流程", "冻结时间", "project", "三月起冻结。")

	updated, changed, err := s.Update(context.Background(), mem.FileName, EntryInput{Description: "冻结时间与窗口", Importance: 7})
	if err != nil || !changed {
		t.Fatalf("Update: %v changed=%v", err, changed)
	}
	if updated.Description != "冻结时间与窗口" || updated.Importance != 7 || updated.Body != mem.Body {
		t.Fatalf("updated = %+v", updated)
	}
	if !updated.UpdatedAt.After(mem.UpdatedAt) && !updated.UpdatedAt.Equal(updated.UpdatedAt) {
		t.Fatalf("updatedAt not advanced: %+v", updated)
	}
	// 空字段表示保持不变：无变化时不落盘。
	same, changed, err := s.Update(context.Background(), mem.FileName, EntryInput{Body: "三月起冻结。"})
	if changed {
		t.Fatal("identical body must be a no-op")
	}
	if same.Hash != mem.Hash {
		t.Fatal("no-op must keep hash")
	}
	if _, _, err := s.Update(context.Background(), "missing.md", EntryInput{Name: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestArchiveExcludesFromListRecallIndex(t *testing.T) {
	s := newTestStore(t)
	mem := saveEntry(t, s, "数据库", "不要 mock", "feedback", "直连真实库。")
	if _, err := s.Archive(context.Background(), mem.FileName); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("archived entry still listed: %+v", list)
	}
	hits, err := s.Recall(context.Background(), "数据库 mock", 5, 1)
	if err != nil || len(hits) != 0 {
		t.Fatalf("recall = %+v err=%v, want empty", hits, err)
	}
	index, err := s.Index(context.Background())
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if strings.Contains(index, "数据库") {
		t.Fatalf("archived entry still indexed: %q", index)
	}
	got, err := s.Get(mem.FileName)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusArchived {
		t.Fatalf("status = %q, want archived", got.Status)
	}
}

func TestIndexTextTruncation(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("# 记忆索引\n\n")
	for i := 0; i < 300; i++ {
		b.WriteString("- [project] 条目 - i\n")
	}
	path := filepath.Join(dir, IndexFileName)
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write index: %v", err)
	}
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	text := s.IndexText(context.Background())
	lines := strings.Split(text, "\n")
	if len(lines) > indexMaxLines+3 {
		t.Fatalf("index not truncated: %d lines", len(lines))
	}
	if !strings.Contains(text, "WARNING") {
		t.Fatalf("missing truncation warning: %q", text[len(text)-100:])
	}
}

func seedMemories(t *testing.T, s *Store) {
	t.Helper()
	saveEntry(t, s, "数据库", "集成测试必须连接真实数据库", "feedback", "不要 mock 数据库。")
	saveEntry(t, s, "发布流程", "移动端发版冻结时间", "project", "2026-03-05 起冻结非关键合并。")
	saveEntry(t, s, "Grafana 面板", "oncall 延迟监控面板地址", "reference", "grafana.internal/d/api-latency。")
}

func TestRecallScoresKeywordOverlap(t *testing.T) {
	s := newTestStore(t)
	seedMemories(t, s)

	got, err := s.Recall(context.Background(), "数据库测试可以 mock 吗", 5, 1)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(got) == 0 || got[0].Memory.Name != "数据库" {
		t.Fatalf("recall = %+v, want 数据库 first", got)
	}
	for _, hit := range got {
		if hit.Memory.Name == "Grafana 面板" {
			t.Fatalf("unrelated memory recalled: %+v", got)
		}
	}
}

func TestRecallImportanceAndRecencyAffectRank(t *testing.T) {
	s := newTestStore(t)
	important := saveEntry(t, s, "部署", "deploy pipeline", "project", "deploy notes for prod")
	plain := saveEntry(t, s, "备份", "deploy pipeline", "project", "deploy notes for backup")
	if _, _, err := s.Update(context.Background(), important.FileName, EntryInput{Importance: 10}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	// 把 plain 的访问时间推到很久以前，削弱新近性。
	plainPath := filepath.Join(s.Dir(), plain.FileName)
	old := mustTime(t, "2020-01-01T00:00:00Z")
	if err := os.Chtimes(plainPath, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	got, err := s.Recall(context.Background(), "deploy pipeline", 5, 1)
	if err != nil || len(got) != 2 {
		t.Fatalf("recall = %+v err=%v", got, err)
	}
	if got[0].Memory.Name != important.Name {
		t.Fatalf("rank = %s first, want %s", got[0].Memory.Name, important.Name)
	}
	if got[0].Score <= got[1].Score {
		t.Fatalf("scores not ordered: %v vs %v", got[0].Score, got[1].Score)
	}
}

func TestRecallMatchesEnglishAndDefaultsLimit(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 8; i++ {
		name := "deploy-" + string(rune('a'+i))
		saveEntry(t, s, name, "deploy pipeline", "project", "deploy pipeline notes "+string(rune('a'+i)))
	}
	got, err := s.Recall(context.Background(), "deploy pipeline failed", 0, 1)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("recall len = %d, want default 5", len(got))
	}
}

func TestRecallReturnsNothingWithoutOverlap(t *testing.T) {
	s := newTestStore(t)
	seedMemories(t, s)
	got, err := s.Recall(context.Background(), "量子力学", 5, 1)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("recall = %+v, want empty", got)
	}
}

func TestRecallTouchesAccessMetadata(t *testing.T) {
	s := newTestStore(t)
	mem := saveEntry(t, s, "数据库", "不要 mock", "feedback", "直连真实库。")
	// 把 frontmatter 里的访问时间改旧，模拟上次命中在一小时之前。
	stale := mustTime(t, "2020-01-01T00:00:00Z")
	mem.LastAccessedAt = stale
	if err := os.WriteFile(filepath.Join(s.Dir(), mem.FileName), []byte(serializeMemory(mem)), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if _, err := s.Recall(context.Background(), "数据库", 5, 1); err != nil {
		t.Fatalf("Recall: %v", err)
	}
	got, err := s.Get(mem.FileName)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AccessCount != 1 {
		t.Fatalf("accessCount = %d, want 1", got.AccessCount)
	}
	if got.LastAccessedAt.IsZero() || got.LastAccessedAt.Before(stale) {
		t.Fatalf("lastAccessedAt = %v", got.LastAccessedAt)
	}
}

func TestProjectDirNameDeterministic(t *testing.T) {
	a := ProjectDirName("/tmp/demo")
	b := ProjectDirName("/tmp/demo")
	other := ProjectDirName("/tmp/other")
	if a != b {
		t.Fatalf("not deterministic: %q vs %q", a, b)
	}
	if a == other {
		t.Fatal("different dirs must map to different project dirs")
	}
	if !strings.HasPrefix(a, "demo-") || len(a) != len("demo-")+16 {
		t.Fatalf("name = %q, want slug-sha16", a)
	}
}

func TestHubProjectScopeRouting(t *testing.T) {
	hub, err := NewHub(filepath.Join(t.TempDir(), "memory"))
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	workDir := "/tmp/demo-project"
	if _, _, err := hub.Save(context.Background(), ScopeProject, workDir, EntryInput{Name: "构建命令", Body: "make build"}); err != nil {
		t.Fatalf("Save project: %v", err)
	}
	if _, _, err := hub.Save(context.Background(), ScopeUser, workDir, EntryInput{Name: "语言偏好", Body: "用中文交流"}); err != nil {
		t.Fatalf("Save user: %v", err)
	}
	projects, err := hub.List(ScopeProject, workDir)
	if err != nil || len(projects) != 1 || projects[0].Name != "构建命令" {
		t.Fatalf("projects = %+v err=%v", projects, err)
	}
	if projects[0].Scope != ScopeProject {
		t.Fatalf("scope = %q", projects[0].Scope)
	}
	// 项目目录落在 projects/<slug>-<sha16> 下。
	entries, err := os.ReadDir(filepath.Join(hub.dir, ProjectsDirName))
	if err != nil {
		t.Fatalf("read projects: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != ProjectDirName(workDir) {
		t.Fatalf("project dirs = %+v, want %s", entries, ProjectDirName(workDir))
	}
	if _, err := hub.List("galaxy", workDir); err == nil {
		t.Fatal("unknown scope must fail")
	}
}

func TestHubContextTextCombinesIndexAndRecall(t *testing.T) {
	hub, err := NewHub(filepath.Join(t.TempDir(), "memory"))
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	workDir := "/tmp/demo-project"
	if _, _, err := hub.Save(context.Background(), ScopeUser, workDir, EntryInput{Name: "语言偏好", Description: "始终用中文", Body: "用中文交流。"}); err != nil {
		t.Fatalf("Save user: %v", err)
	}
	if _, _, err := hub.Save(context.Background(), ScopeProject, workDir, EntryInput{Name: "构建命令", Description: "项目用 make 构建", Body: "make build && make test。"}); err != nil {
		t.Fatalf("Save project: %v", err)
	}
	text := hub.ContextText(context.Background(), workDir, "构建命令是什么")
	for _, want := range []string{"## 长期记忆索引", "### 用户级", "### 本项目级", "## 相关记忆", "构建命令", "语言偏好"} {
		if !strings.Contains(text, want) {
			t.Fatalf("context missing %q:\n%s", want, text)
		}
	}
	// 空记忆库返回空文本。
	empty, err := NewHub(filepath.Join(t.TempDir(), "memory"))
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	if got := empty.ContextText(context.Background(), workDir, "任意"); got != "" {
		t.Fatalf("empty hub context = %q", got)
	}
}

func TestHubRecallMergesScopes(t *testing.T) {
	hub, err := NewHub(filepath.Join(t.TempDir(), "memory"))
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	workDir := "/tmp/demo-project"
	if _, _, err := hub.Save(context.Background(), ScopeUser, workDir, EntryInput{Name: "部署脚本", Body: "deploy.sh 在仓库根目录。"}); err != nil {
		t.Fatalf("Save user: %v", err)
	}
	if _, _, err := hub.Save(context.Background(), ScopeProject, workDir, EntryInput{Name: "部署目标", Body: "部署脚本部署到 staging 集群。"}); err != nil {
		t.Fatalf("Save project: %v", err)
	}
	hits := hub.Recall(context.Background(), workDir, "部署脚本", 5)
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(hits))
	}
	if hits[0].Memory.Scope != ScopeProject {
		t.Fatalf("project hit must rank first with boost: %+v", hits)
	}
}

func TestMemoryToolsViaHub(t *testing.T) {
	hub, err := NewHub(filepath.Join(t.TempDir(), "memory"))
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	tools := hub.ToolsFor("/tmp/demo")
	if len(tools) != 3 {
		t.Fatalf("tools = %d, want 3", len(tools))
	}
	byName := make(map[string]tool.Tool, len(tools))
	for _, tl := range tools {
		byName[tl.Name()] = tl
		if tl.ReadOnly(nil) || tl.ConcurrencySafe(nil) {
			t.Fatalf("%s must be a write tool and serialized", tl.Name())
		}
	}
	save := byName[ToolNameMemorySave]
	res, err := save.Run(context.Background(), json.RawMessage(
		`{"name":"外部工单","description":"缺陷在 Linear 的 INGEST 项目","type":"reference","content":"查看 INGEST。","scope":"project","keywords":["工单"],"importance":6}`), nil)
	if err != nil {
		t.Fatalf("save run: %v", err)
	}
	if res.Meta["scope"] != ScopeProject || res.Meta["fileName"] != "外部工单.md" {
		t.Fatalf("result = %+v", res)
	}
	// 幂等：同内容再存返回已存在。
	res, err = save.Run(context.Background(), json.RawMessage(`{"name":"外部工单","content":"查看 INGEST。","scope":"project"}`), nil)
	if err != nil {
		t.Fatalf("save again: %v", err)
	}
	if !strings.Contains(res.Content, "未重复保存") {
		t.Fatalf("second save content = %q", res.Content)
	}

	update := byName[ToolNameMemoryUpdate]
	res, err = update.Run(context.Background(), json.RawMessage(
		`{"fileName":"外部工单.md","scope":"project","content":"工单系统换成了 Jira。"}`), nil)
	if err != nil {
		t.Fatalf("update run: %v", err)
	}
	if !strings.Contains(res.Content, "已更新") {
		t.Fatalf("update content = %q", res.Content)
	}
	res, err = update.Run(context.Background(), json.RawMessage(
		`{"fileName":"外部工单.md","scope":"project","content":"工单系统换成了 Jira。"}`), nil)
	if err != nil {
		t.Fatalf("update again: %v", err)
	}
	if !strings.Contains(res.Content, "未发生变化") {
		t.Fatalf("no-op update content = %q", res.Content)
	}

	del := byName[ToolNameMemoryDelete]
	res, err = del.Run(context.Background(), json.RawMessage(`{"fileName":"外部工单.md","scope":"project"}`), nil)
	if err != nil {
		t.Fatalf("delete run: %v", err)
	}
	if !strings.Contains(res.Content, "归档保留") {
		t.Fatalf("delete content = %q", res.Content)
	}
	if mem, err := hub.Get(ScopeProject, "/tmp/demo", "外部工单.md"); err != nil || mem.Status != StatusArchived {
		t.Fatalf("get after delete: %+v err=%v", mem, err)
	}
	if _, err := del.Run(context.Background(), json.RawMessage(`{"fileName":"missing.md"}`), nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing delete err = %v", err)
	}
	if _, err := save.Run(context.Background(), json.RawMessage(`{"name":""}`), nil); !errors.Is(err, ErrEmptyName) {
		t.Fatalf("empty name err = %v", err)
	}
	if _, err := save.Run(context.Background(), json.RawMessage(`{`), nil); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestTokenizeChineseBigrams(t *testing.T) {
	tokens := tokenize("数据库测试")
	for _, want := range []string{"数据", "据库", "库测", "测试"} {
		if _, ok := tokens[want]; !ok {
			t.Fatalf("missing token %q in %v", want, tokens)
		}
	}
}
