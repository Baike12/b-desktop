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

	"github.com/Baike12/b-desktop/internal/agent"
)

// Store 必须可注入 agent 的上下文提供者列表。
var _ agent.ContextProvider = (*Store)(nil)

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

func TestSaveWritesFileAndIndex(t *testing.T) {
	s := newTestStore(t)
	mem, err := s.Save(context.Background(), "回复风格", "用户偏好简洁回答", "feedback", "不要总结。\n\nWhy: 用户能自己看 diff。")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if mem.FileName != "回复风格.md" {
		t.Fatalf("fileName = %q", mem.FileName)
	}
	data, err := os.ReadFile(filepath.Join(s.Dir(), mem.FileName))
	if err != nil {
		t.Fatalf("read memory: %v", err)
	}
	for _, want := range []string{"name: 回复风格", "description: 用户偏好简洁回答", "type: feedback", "不要总结。"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("file missing %q:\n%s", want, data)
		}
	}
	index, err := s.Index(context.Background())
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if !strings.Contains(index, "- [feedback] 回复风格: 用户偏好简洁回答") {
		t.Fatalf("index = %q", index)
	}

	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Name != "回复风格" || list[0].Type != "feedback" {
		t.Fatalf("list = %+v", list)
	}
	if strings.Contains(list[0].Body, "---") || strings.Contains(list[0].Body, "type:") {
		t.Fatalf("body includes frontmatter: %q", list[0].Body)
	}
}

func TestSaveDefaultsAndValidation(t *testing.T) {
	s := newTestStore(t)
	mem, err := s.Save(context.Background(), "事实", "", "", "正文第一行\n第二行")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if mem.Type != "project" || mem.Description != "正文第一行" {
		t.Fatalf("mem = %+v", mem)
	}
	if _, err := s.Save(context.Background(), " ", "d", "project", "body"); !errors.Is(err, ErrEmptyName) {
		t.Fatalf("err = %v, want ErrEmptyName", err)
	}
	if _, err := s.Save(context.Background(), "n", "d", "project", "  "); !errors.Is(err, ErrEmptyBody) {
		t.Fatalf("err = %v, want ErrEmptyBody", err)
	}
	if _, err := s.Save(context.Background(), "n", "d", "Bad Type", "body"); !errors.Is(err, ErrInvalidType) {
		t.Fatalf("err = %v, want ErrInvalidType", err)
	}
}

func TestSaveDeduplicatesFileNames(t *testing.T) {
	s := newTestStore(t)
	first, err := s.Save(context.Background(), "same name", "d", "project", "a")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	second, err := s.Save(context.Background(), "same name", "d", "project", "b")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if first.FileName == second.FileName {
		t.Fatalf("file names collide: %q", first.FileName)
	}
	if second.FileName != "same-name-2.md" {
		t.Fatalf("second = %q", second.FileName)
	}
}

func seedMemories(t *testing.T, s *Store) {
	t.Helper()
	saves := []struct{ name, description, typ, body string }{
		{"数据库", "集成测试必须连接真实数据库", "feedback", "不要 mock 数据库。"},
		{"发布流程", "移动端发版冻结时间", "project", "2026-03-05 起冻结非关键合并。"},
		{"Grafana 面板", "oncall 延迟监控面板地址", "reference", "grafana.internal/d/api-latency。"},
	}
	for _, item := range saves {
		if _, err := s.Save(context.Background(), item.name, item.description, item.typ, item.body); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
}

func TestRecallScoresKeywordOverlap(t *testing.T) {
	s := newTestStore(t)
	seedMemories(t, s)

	got, err := s.Recall(context.Background(), "数据库测试可以 mock 吗", 5)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(got) == 0 || got[0].Name != "数据库" {
		t.Fatalf("recall = %+v, want 数据库 first", got)
	}
	for _, mem := range got {
		if mem.Name == "Grafana 面板" {
			t.Fatalf("unrelated memory recalled: %+v", got)
		}
	}
}

func TestRecallMatchesEnglishAndDefaultsLimit(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 8; i++ {
		name := "deploy-" + string(rune('a'+i))
		if _, err := s.Save(context.Background(), name, "deploy pipeline", "project", "deploy pipeline notes"); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	got, err := s.Recall(context.Background(), "deploy pipeline failed", 0)
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
	got, err := s.Recall(context.Background(), "量子力学", 5)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("recall = %+v, want empty", got)
	}
}

func TestExtraContextInjectsOnlyRelevantMemories(t *testing.T) {
	s := newTestStore(t)
	seedMemories(t, s)
	text := s.ExtraContext(context.Background(), "sess", ".", "Grafana 面板地址是什么")
	if !strings.Contains(text, "## 相关记忆") || !strings.Contains(text, "Grafana 面板") {
		t.Fatalf("context = %q", text)
	}
	if strings.Contains(text, "数据库") {
		t.Fatalf("unexpected memory in context: %q", text)
	}
	if got := s.ExtraContext(context.Background(), "sess", ".", "量子力学"); got != "" {
		t.Fatalf("context = %q, want empty", got)
	}
}

func TestListExcludesIndexAndSortsNewestFirst(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Save(context.Background(), "old", "d", "project", "1"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := s.Save(context.Background(), "new", "d", "project", "2"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// 显式设置修改时间，避免文件系统时间精度导致排序不稳定。
	oldPath := filepath.Join(s.Dir(), "old.md")
	newPath := filepath.Join(s.Dir(), "new.md")
	if err := os.Chtimes(oldPath, mustTime(t, "2020-01-01T00:00:00Z"), mustTime(t, "2020-01-01T00:00:00Z")); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if err := os.Chtimes(newPath, mustTime(t, "2021-01-01T00:00:00Z"), mustTime(t, "2021-01-01T00:00:00Z")); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || list[0].Name != "new" {
		t.Fatalf("list = %+v", list)
	}
	for _, mem := range list {
		if mem.FileName == IndexFileName {
			t.Fatal("index returned as memory")
		}
	}
}

func TestMemorySaveTool(t *testing.T) {
	s := newTestStore(t)
	tools := s.Tools()
	if len(tools) != 1 || tools[0].Name() != ToolNameMemorySave {
		t.Fatalf("tools = %+v", tools)
	}
	tl := tools[0]
	if tl.ReadOnly(nil) || tl.ConcurrencySafe(nil) {
		t.Fatal("MemorySave must be a write tool and serialized")
	}
	res, err := tl.Run(context.Background(), json.RawMessage(`{"name":"外部工单","description":"缺陷在 Linear 的 INGEST 项目","type":"reference","content":"查看 INGEST。"}`), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Content, "已保存记忆") || res.Meta["fileName"] != "外部工单.md" {
		t.Fatalf("result = %+v", res)
	}
	index, err := s.Index(context.Background())
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if !strings.Contains(index, "INGEST") {
		t.Fatalf("index = %q", index)
	}
	if _, err := tl.Run(context.Background(), json.RawMessage(`{"name":""}`), nil); !errors.Is(err, ErrEmptyName) {
		t.Fatalf("err = %v", err)
	}
	if _, err := tl.Run(context.Background(), json.RawMessage(`{`), nil); err == nil {
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
