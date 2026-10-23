package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"anc-dsk/internal/tool"
)

// newTestRoot 返回已解析符号链接的临时工作区根目录。
func newTestRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("eval symlinks %q: %v", root, err)
	}
	return real
}

// writeFixture 在 root 下写入测试文件，自动创建父目录。
func writeFixture(t *testing.T, root, rel string, data []byte) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %q: %v", rel, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %q: %v", rel, err)
	}
	return path
}

// readFixture 读取测试文件内容。
func readFixture(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	return string(data)
}

// mustJSON 把输入编码成工具参数。
func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	return raw
}

// runFileTool 执行一次工具调用，并要求 Run 不返回 error（可预期失败应转成结构化结果）。
func runFileTool(t *testing.T, tl tool.Tool, input any) tool.Result {
	t.Helper()
	res, err := tl.Run(context.Background(), mustJSON(t, input), nil)
	if err != nil {
		t.Fatalf("%s: Run returned error: %v", tl.Name(), err)
	}
	return res
}

func assertResult(t *testing.T, res tool.Result, wantErr bool, contains, notContains []string) {
	t.Helper()
	if res.IsError != wantErr {
		t.Fatalf("IsError = %v, want %v; content = %q", res.IsError, wantErr, res.Content)
	}
	for _, want := range contains {
		if !strings.Contains(res.Content, want) {
			t.Errorf("content %q does not contain %q", res.Content, want)
		}
	}
	for _, unwanted := range notContains {
		if strings.Contains(res.Content, unwanted) {
			t.Errorf("content %q unexpectedly contains %q", res.Content, unwanted)
		}
	}
}

func TestResolve(t *testing.T) {
	root := newTestRoot(t)
	outside := newTestRoot(t)
	writeFixture(t, root, "a.txt", []byte("a"))
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	writeFixture(t, outside, "secret.txt", []byte("secret"))

	if err := os.Symlink(outside, filepath.Join(root, "outside-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "outside-file")); err != nil {
		t.Fatalf("symlink outside file: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "a.txt"), filepath.Join(root, "inside-link")); err != nil {
		t.Fatalf("symlink inside file: %v", err)
	}

	cases := []struct {
		name    string
		path    string
		want    string
		wantErr error
	}{
		{"relative inside", "a.txt", filepath.Join(root, "a.txt"), nil},
		{"nested missing tail", "sub/new/deep.txt", filepath.Join(root, "sub", "new", "deep.txt"), nil},
		{"absolute inside", filepath.Join(root, "a.txt"), filepath.Join(root, "a.txt"), nil},
		{"dotdot prefix filename", "..foo", filepath.Join(root, "..foo"), nil},
		{"symlink inside root", "inside-link", filepath.Join(root, "a.txt"), nil},
		{"empty path", "   ", "", ErrPathRequired},
		{"parent escape", "../x", "", ErrPathOutsideRoot},
		{"nested parent escape", "sub/../../x", "", ErrPathOutsideRoot},
		{"absolute outside", filepath.Join(outside, "secret.txt"), "", ErrPathOutsideRoot},
		{"symlink directory escape", "outside-link/secret.txt", "", ErrPathOutsideRoot},
		{"symlink file escape", "outside-file", "", ErrPathOutsideRoot},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(root, tc.path)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRead(t *testing.T) {
	root := newTestRoot(t)
	writeFixture(t, root, "lines.txt", []byte("line1\nline2\nline3\n"))
	writeFixture(t, root, "no-trailing-newline.txt", []byte("only"))
	writeFixture(t, root, "crlf.txt", []byte("a\r\nb\r\n"))
	writeFixture(t, root, "empty.txt", nil)
	writeFixture(t, root, "binary.bin", []byte{0x00, 0x01, 0x02, 0x03})
	writeFixture(t, root, "badutf8.txt", []byte{0xff, 0xfe, 'a', '\n'})
	writeFixture(t, root, "big.txt", bytes.Repeat([]byte("a"), 300*1024))
	writeFixture(t, root, "long.txt", []byte(strings.Repeat(strings.Repeat("x", 100)+"\n", 2000)))

	read := NewRead(root)
	cases := []struct {
		name        string
		input       map[string]any
		wantErr     bool
		contains    []string
		notContains []string
		truncated   bool
	}{
		{
			name:     "line numbers by default",
			input:    map[string]any{"file_path": "lines.txt"},
			contains: []string{"     1\tline1\n", "     2\tline2\n", "     3\tline3\n"},
		},
		{
			name:        "offset and limit",
			input:       map[string]any{"file_path": "lines.txt", "offset": 1, "limit": 1},
			contains:    []string{"     2\tline2"},
			notContains: []string{"line1", "line3"},
		},
		{
			name:     "line numbers disabled",
			input:    map[string]any{"file_path": "lines.txt", "show_line_numbers": false},
			contains: []string{"line1\nline2\nline3\n"},
		},
		{
			name:     "file without trailing newline",
			input:    map[string]any{"file_path": "no-trailing-newline.txt"},
			contains: []string{"     1\tonly"},
		},
		{
			name:     "crlf is normalized",
			input:    map[string]any{"file_path": "crlf.txt"},
			contains: []string{"     1\ta\n", "     2\tb\n"},
		},
		{
			name:     "empty file",
			input:    map[string]any{"file_path": "empty.txt"},
			contains: []string{"is empty"},
		},
		{
			name:     "missing file",
			input:    map[string]any{"file_path": "nope.txt"},
			wantErr:  true,
			contains: []string{"file not found"},
		},
		{
			name:     "binary NUL byte",
			input:    map[string]any{"file_path": "binary.bin"},
			wantErr:  true,
			contains: []string{"binary file", "NUL"},
		},
		{
			name:     "invalid utf8",
			input:    map[string]any{"file_path": "badutf8.txt"},
			wantErr:  true,
			contains: []string{"binary file", "UTF-8"},
		},
		{
			name:     "oversized file",
			input:    map[string]any{"file_path": "big.txt"},
			wantErr:  true,
			contains: []string{"exceeding", "262144"},
		},
		{
			name:     "offset past end",
			input:    map[string]any{"file_path": "lines.txt", "offset": 99},
			contains: []string{"has 3 lines", "past the end"},
		},
		{
			name:     "limit above maximum",
			input:    map[string]any{"file_path": "lines.txt", "limit": 2001},
			wantErr:  true,
			contains: []string{"limit must be between 1 and 2000"},
		},
		{
			name:     "negative offset",
			input:    map[string]any{"file_path": "lines.txt", "offset": -1},
			wantErr:  true,
			contains: []string{"offset must be >= 0"},
		},
		{
			name:     "path escape",
			input:    map[string]any{"file_path": "../outside.txt"},
			wantErr:  true,
			contains: []string{"escapes workspace root"},
		},
		{
			name:     "directory",
			input:    map[string]any{"file_path": "."},
			wantErr:  true,
			contains: []string{"is a directory"},
		},
		{
			name:      "output truncation",
			input:     map[string]any{"file_path": "long.txt"},
			contains:  []string{"[truncated"},
			truncated: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := runFileTool(t, read, tc.input)
			assertResult(t, res, tc.wantErr, tc.contains, tc.notContains)
			if res.Truncated != tc.truncated {
				t.Fatalf("Truncated = %v, want %v", res.Truncated, tc.truncated)
			}
		})
	}
}

func TestReadContract(t *testing.T) {
	root := newTestRoot(t)
	read := NewRead(root)

	if read.Name() != "Read" {
		t.Fatalf("Name = %q", read.Name())
	}
	if !read.ReadOnly(nil) {
		t.Fatal("Read must report ReadOnly")
	}
	if !read.ConcurrencySafe(nil) {
		t.Fatal("Read must report ConcurrencySafe")
	}
	var schema map[string]any
	if err := json.Unmarshal(read.InputSchema(), &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	required, _ := schema["required"].([]any)
	if len(required) != 1 || required[0] != "file_path" {
		t.Fatalf("schema required = %v, want [file_path]", schema["required"])
	}

	paths, err := read.(tool.PathAware).AffectedPaths(mustJSON(t, map[string]any{"file_path": "sub/a.txt"}))
	if err != nil {
		t.Fatalf("AffectedPaths: %v", err)
	}
	if len(paths) != 1 || paths[0] != filepath.Join(root, "sub", "a.txt") {
		t.Fatalf("AffectedPaths = %v", paths)
	}
	if _, err := read.(tool.PathAware).AffectedPaths(mustJSON(t, map[string]any{"file_path": "../x"})); !errors.Is(err, ErrPathOutsideRoot) {
		t.Fatalf("AffectedPaths escape err = %v", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := read.Run(canceled, mustJSON(t, map[string]any{"file_path": "a.txt"}), nil)
	if err != nil {
		t.Fatalf("Run returned error on cancel: %v", err)
	}
	assertResult(t, res, true, []string{"canceled"}, nil)

	invalid, err := read.Run(context.Background(), json.RawMessage("{"), nil)
	if err != nil {
		t.Fatalf("Run returned error on invalid input: %v", err)
	}
	assertResult(t, invalid, true, []string{"invalid input"}, nil)
}

func TestWrite(t *testing.T) {
	root := newTestRoot(t)
	write := NewWrite(root)

	t.Run("creates directories and writes content", func(t *testing.T) {
		res := runFileTool(t, write, map[string]any{"file_path": "a/b/c.txt", "content": "hello\n"})
		assertResult(t, res, false, []string{"wrote 6 bytes", filepath.Join("a", "b", "c.txt")}, nil)
		if got := readFixture(t, filepath.Join(root, "a", "b", "c.txt")); got != "hello\n" {
			t.Fatalf("content = %q", got)
		}
		entries, err := os.ReadDir(filepath.Join(root, "a", "b"))
		if err != nil {
			t.Fatalf("read dir: %v", err)
		}
		if len(entries) != 1 || entries[0].Name() != "c.txt" {
			t.Fatalf("leftover temp files: %v", entries)
		}
	})

	t.Run("replaces existing content", func(t *testing.T) {
		path := writeFixture(t, root, "replace.txt", []byte("old content that is longer"))
		runFileTool(t, write, map[string]any{"file_path": "replace.txt", "content": "new"})
		if got := readFixture(t, path); got != "new" {
			t.Fatalf("content = %q", got)
		}
	})

	t.Run("preserves existing permissions", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("permission bits are not meaningful on windows")
		}
		path := writeFixture(t, root, "mode.txt", []byte("a"))
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		runFileTool(t, write, map[string]any{"file_path": "mode.txt", "content": "b"})
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("mode = %v, want 0600", got)
		}
	})

	t.Run("empty content is allowed", func(t *testing.T) {
		res := runFileTool(t, write, map[string]any{"file_path": "empty.txt", "content": ""})
		assertResult(t, res, false, []string{"wrote 0 bytes"}, nil)
		if got := readFixture(t, filepath.Join(root, "empty.txt")); got != "" {
			t.Fatalf("content = %q", got)
		}
	})

	if err := os.MkdirAll(filepath.Join(root, "adir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFixture(t, root, "blocker.txt", []byte("x"))

	cases := []struct {
		name     string
		input    any
		contains []string
	}{
		{"missing content", map[string]any{"file_path": "x.txt"}, []string{"content is required"}},
		{"missing path", map[string]any{"content": "x"}, []string{"path is required"}},
		{"path escape", map[string]any{"file_path": "../x.txt", "content": "x"}, []string{"escapes workspace root"}},
		{"directory target", map[string]any{"file_path": "adir", "content": "x"}, []string{"is a directory"}},
		{"parent is a file", map[string]any{"file_path": "blocker.txt/child.txt", "content": "x"}, []string{"Write:"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := runFileTool(t, write, tc.input)
			assertResult(t, res, true, tc.contains, nil)
		})
	}

	if got := readFixture(t, filepath.Join(root, "blocker.txt")); got != "x" {
		t.Fatalf("failed write modified existing file: %q", got)
	}
}

func TestWriteContract(t *testing.T) {
	root := newTestRoot(t)
	write := NewWrite(root)

	if write.Name() != "Write" {
		t.Fatalf("Name = %q", write.Name())
	}
	if write.ReadOnly(nil) {
		t.Fatal("Write must not report ReadOnly")
	}
	if write.ConcurrencySafe(nil) {
		t.Fatal("Write must not report ConcurrencySafe")
	}
	var schema map[string]any
	if err := json.Unmarshal(write.InputSchema(), &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	required, _ := schema["required"].([]any)
	if len(required) != 2 || required[0] != "file_path" || required[1] != "content" {
		t.Fatalf("schema required = %v", schema["required"])
	}
	paths, err := write.(tool.PathAware).AffectedPaths(mustJSON(t, map[string]any{"file_path": "out.txt", "content": "x"}))
	if err != nil {
		t.Fatalf("AffectedPaths: %v", err)
	}
	if len(paths) != 1 || paths[0] != filepath.Join(root, "out.txt") {
		t.Fatalf("AffectedPaths = %v", paths)
	}
}

func TestEdit(t *testing.T) {
	root := newTestRoot(t)
	edit := NewEdit(root)

	cases := []struct {
		name      string
		content   []byte
		input     map[string]any
		wantErr   bool
		contains  []string
		wantAfter string
	}{
		{
			name:      "unique replacement",
			content:   []byte("hello world\n"),
			input:     map[string]any{"file_path": "f.txt", "old_string": "world", "new_string": "there"},
			contains:  []string{"replaced 1 occurrence(s)"},
			wantAfter: "hello there\n",
		},
		{
			name:      "not found",
			content:   []byte("hello\n"),
			input:     map[string]any{"file_path": "f.txt", "old_string": "absent", "new_string": "x"},
			wantErr:   true,
			contains:  []string{"not found"},
			wantAfter: "hello\n",
		},
		{
			name:      "not unique",
			content:   []byte("aa bb aa\n"),
			input:     map[string]any{"file_path": "f.txt", "old_string": "aa", "new_string": "cc"},
			wantErr:   true,
			contains:  []string{"matches 2 times", "replace_all=true"},
			wantAfter: "aa bb aa\n",
		},
		{
			name:      "replace all",
			content:   []byte("aa bb aa\n"),
			input:     map[string]any{"file_path": "f.txt", "old_string": "aa", "new_string": "cc", "replace_all": true},
			contains:  []string{"replaced 2 occurrence(s)"},
			wantAfter: "cc bb cc\n",
		},
		{
			name:      "delete text",
			content:   []byte("keep DELETE keep\n"),
			input:     map[string]any{"file_path": "f.txt", "old_string": " DELETE", "new_string": ""},
			contains:  []string{"replaced 1 occurrence(s)"},
			wantAfter: "keep keep\n",
		},
		{
			name:      "no-op replacement",
			content:   []byte("same\n"),
			input:     map[string]any{"file_path": "f.txt", "old_string": "same", "new_string": "same"},
			contains:  []string{"no changes"},
			wantAfter: "same\n",
		},
		{
			name:      "empty old_string",
			content:   []byte("abc\n"),
			input:     map[string]any{"file_path": "f.txt", "old_string": "", "new_string": "x"},
			wantErr:   true,
			contains:  []string{"must not be empty"},
			wantAfter: "abc\n",
		},
		{
			name:      "missing new_string",
			content:   []byte("abc\n"),
			input:     map[string]any{"file_path": "f.txt", "old_string": "a"},
			wantErr:   true,
			contains:  []string{"new_string is required"},
			wantAfter: "abc\n",
		},
		{
			name:      "binary file",
			content:   []byte{0x00, 'a', 'b'},
			input:     map[string]any{"file_path": "f.txt", "old_string": "a", "new_string": "c"},
			wantErr:   true,
			contains:  []string{"binary file"},
			wantAfter: "\x00ab",
		},
		{
			name:      "invalid utf8",
			content:   []byte{0xff, 'a'},
			input:     map[string]any{"file_path": "f.txt", "old_string": "a", "new_string": "c"},
			wantErr:   true,
			contains:  []string{"binary file", "UTF-8"},
			wantAfter: "\xffa",
		},
		{
			name:      "path escape",
			content:   []byte("abc\n"),
			input:     map[string]any{"file_path": "../f.txt", "old_string": "a", "new_string": "c"},
			wantErr:   true,
			contains:  []string{"escapes workspace root"},
			wantAfter: "abc\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeFixture(t, root, "f.txt", tc.content)
			res := runFileTool(t, edit, tc.input)
			assertResult(t, res, tc.wantErr, tc.contains, nil)
			if got := readFixture(t, path); got != tc.wantAfter {
				t.Fatalf("file after edit = %q, want %q", got, tc.wantAfter)
			}
		})
	}

	t.Run("missing file", func(t *testing.T) {
		res := runFileTool(t, edit, map[string]any{"file_path": "nope.txt", "old_string": "a", "new_string": "b"})
		assertResult(t, res, true, []string{"file not found"}, nil)
	})
}

func TestEditContract(t *testing.T) {
	root := newTestRoot(t)
	edit := NewEdit(root)

	if edit.Name() != "Edit" {
		t.Fatalf("Name = %q", edit.Name())
	}
	if edit.ReadOnly(nil) {
		t.Fatal("Edit must not report ReadOnly")
	}
	if edit.ConcurrencySafe(nil) {
		t.Fatal("Edit must not report ConcurrencySafe")
	}
	var schema map[string]any
	if err := json.Unmarshal(edit.InputSchema(), &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	required, _ := schema["required"].([]any)
	if len(required) != 3 || required[0] != "file_path" || required[1] != "old_string" || required[2] != "new_string" {
		t.Fatalf("schema required = %v", schema["required"])
	}
	paths, err := edit.(tool.PathAware).AffectedPaths(mustJSON(t, map[string]any{
		"file_path": "f.txt", "old_string": "a", "new_string": "b",
	}))
	if err != nil {
		t.Fatalf("AffectedPaths: %v", err)
	}
	if len(paths) != 1 || paths[0] != filepath.Join(root, "f.txt") {
		t.Fatalf("AffectedPaths = %v", paths)
	}
}
