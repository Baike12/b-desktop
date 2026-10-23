package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Baike12/b-desktop/internal/tool"
)

// writeTestFile 在 root 下创建文件并返回绝对路径。
func writeTestFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	return path
}

// runToolJSON 用 JSON 输入执行工具，失败时终止测试。
func runToolJSON(t *testing.T, tl tool.Tool, in any) tool.Result {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	res, err := tl.Run(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("run %s: %v", tl.Name(), err)
	}
	return res
}

// resultLines 把结果拆成非空行。
func resultLines(res tool.Result) []string {
	content := strings.TrimSpace(res.Content)
	if content == "" {
		return nil
	}
	return strings.Split(content, "\n")
}

func TestGlobRecursivePattern(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "top.go", "package top")
	writeTestFile(t, root, "a/b/c.go", "package c")
	writeTestFile(t, root, "a/notes.txt", "notes")
	writeTestFile(t, root, "vendor/v.go", "package v")
	writeTestFile(t, root, "node_modules/n.go", "package n")
	writeTestFile(t, root, ".git/g.go", "package g")

	res := runToolJSON(t, NewGlob(root), map[string]any{"pattern": "**/*.go"})
	if res.IsError {
		t.Fatalf("unexpected error result: %s", res.Content)
	}
	got := resultLines(res)
	want := map[string]bool{"top.go": true, "a/b/c.go": true}
	if len(got) != len(want) {
		t.Fatalf("got %v, want exactly %v", got, want)
	}
	for _, line := range got {
		if !want[line] {
			t.Fatalf("unexpected path %q (ignored dirs must be skipped); got %v", line, got)
		}
	}
}

func TestGlobSingleStarIsNotRecursive(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "top.go", "")
	writeTestFile(t, root, "sub/nested.go", "")

	got := resultLines(runToolJSON(t, NewGlob(root), map[string]any{"pattern": "*.go"}))
	if len(got) != 1 || got[0] != "top.go" {
		t.Fatalf("got %v, want [top.go]", got)
	}
}

func TestGlobMaxResults(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.go", "b.go", "c.go", "d.go", "e.go"} {
		writeTestFile(t, root, name, "")
	}

	res := runToolJSON(t, NewGlob(root), map[string]any{"pattern": "*.go", "max_results": 2})
	if !res.Truncated {
		t.Fatalf("expected Truncated=true, result=%+v", res)
	}
	if got := resultLines(res); len(got) != 2 {
		t.Fatalf("got %d lines, want 2: %v", len(got), got)
	}
	if res.Meta["count"] != "2" {
		t.Fatalf("meta count = %q, want 2", res.Meta["count"])
	}
}

func TestGlobSortsByMtimeThenPath(t *testing.T) {
	root := t.TempDir()
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	names := []string{"a.go", "b.go", "c.go"}
	for i, name := range names {
		p := writeTestFile(t, root, name, "")
		ts := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(p, ts, ts); err != nil {
			t.Fatalf("chtimes %s: %v", name, err)
		}
	}

	got := resultLines(runToolJSON(t, NewGlob(root), map[string]any{"pattern": "*.go"}))
	want := []string{"c.go", "b.go", "a.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v (newest first)", got, want)
	}

	// 相同 mtime 时按路径升序，输出仍然稳定。
	same := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	for _, name := range names {
		if err := os.Chtimes(filepath.Join(root, name), same, same); err != nil {
			t.Fatalf("chtimes %s: %v", name, err)
		}
	}
	got = resultLines(runToolJSON(t, NewGlob(root), map[string]any{"pattern": "*.go"}))
	want = []string{"a.go", "b.go", "c.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v (path tie-break)", got, want)
	}
}

func TestGlobRejectsPathOutsideRoot(t *testing.T) {
	root := t.TempDir()
	res := runToolJSON(t, NewGlob(root), map[string]any{"pattern": "*", "path": ".."})
	if !res.IsError {
		t.Fatalf("expected IsError for path outside root, got %+v", res)
	}
}

func TestGlobRejectsMissingPattern(t *testing.T) {
	res := runToolJSON(t, NewGlob(t.TempDir()), map[string]any{})
	if !res.IsError {
		t.Fatalf("expected IsError for missing pattern, got %+v", res)
	}
}

func TestGlobRejectsInvalidJSONInput(t *testing.T) {
	tl := NewGlob(t.TempDir())
	res, err := tl.Run(context.Background(), json.RawMessage("{"), nil)
	if err != nil {
		t.Fatalf("Run returned internal error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError for invalid JSON, got %+v", res)
	}
}

func TestGlobContract(t *testing.T) {
	tl := NewGlob(t.TempDir())
	if tl.Name() != "Glob" {
		t.Fatalf("name = %q", tl.Name())
	}
	if !tl.ReadOnly(nil) || !tl.ConcurrencySafe(nil) {
		t.Fatal("Glob must be read-only and concurrency safe")
	}
	if !json.Valid(tl.InputSchema()) {
		t.Fatal("InputSchema is not valid JSON")
	}
	if tl.Description() == "" {
		t.Fatal("empty description")
	}
}

func TestGlobMatchPath(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"**/*.go", "a.go", true},
		{"**/*.go", "a/b/c.go", true},
		{"**/*.go", "a/b/c.txt", false},
		{"a/**/b.go", "a/b.go", true},
		{"a/**/b.go", "a/x/y/b.go", true},
		{"a/**/b.go", "a/x/y/c.go", false},
		{"*.go", "a.go", true},
		{"*.go", "sub/a.go", false},
		{"file?.txt", "file1.txt", true},
		{"file?.txt", "file12.txt", false},
		{"[ab].go", "a.go", true},
		{"[ab].go", "c.go", false},
		{"[!ab].go", "c.go", true},
		{"[!ab].go", "a.go", false},
		{"[a-c].go", "b.go", true},
		{"", "a.go", false},
		{"[abc", "[abc", true}, // 未闭合字符类按字面量处理
	}
	for _, tc := range cases {
		if got := globMatchPath(tc.pattern, tc.path); got != tc.want {
			t.Errorf("globMatchPath(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}
