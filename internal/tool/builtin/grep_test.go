package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"anc-dsk/internal/tool"
)

func TestGrepBasicOutput(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "a.txt", "alpha\nneedle one\nomega\n")
	writeTestFile(t, root, "sub/b.txt", "needle two\n")

	res := runToolJSON(t, NewGrep(root), map[string]any{"pattern": "needle"})
	if res.IsError {
		t.Fatalf("unexpected error result: %s", res.Content)
	}
	got := resultLines(res)
	want := []string{"a.txt:2:needle one", "sub/b.txt:1:needle two"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if res.Meta["matches"] != "2" {
		t.Fatalf("meta matches = %q, want 2", res.Meta["matches"])
	}
}

func TestGrepInvalidRegex(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "a.txt", "anything")

	res := runToolJSON(t, NewGrep(root), map[string]any{"pattern": "["})
	if !res.IsError {
		t.Fatalf("expected IsError for invalid regex, got %+v", res)
	}
	if !strings.Contains(res.Content, "invalid regular expression") {
		t.Fatalf("content = %q, want invalid-regular-expression message", res.Content)
	}
}

func TestGrepSkipsBinaryFiles(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "binary.bin", "needle\x00needle")
	writeTestFile(t, root, "text.txt", "needle")

	got := resultLines(runToolJSON(t, NewGrep(root), map[string]any{"pattern": "needle"}))
	if len(got) != 1 || got[0] != "text.txt:1:needle" {
		t.Fatalf("got %v, want only the text file match", got)
	}
}

func TestGrepMaxResults(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "many.txt", "hit\nhit\nhit\nhit\nhit\n")

	res := runToolJSON(t, NewGrep(root), map[string]any{"pattern": "hit", "max_results": 2})
	if !res.Truncated {
		t.Fatalf("expected Truncated=true, result=%+v", res)
	}
	got := resultLines(res)
	if len(got) != 2 {
		t.Fatalf("got %d lines, want 2: %v", len(got), got)
	}
	for _, line := range got {
		if !strings.HasPrefix(line, "many.txt:") {
			t.Fatalf("unexpected line %q", line)
		}
	}
}

func TestGrepIncludeFilter(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "top.go", "needle")
	writeTestFile(t, root, "notes.txt", "needle")
	writeTestFile(t, root, "sub/lib.go", "needle")

	got := resultLines(runToolJSON(t, NewGrep(root), map[string]any{"pattern": "needle", "include": "*.go"}))
	want := []string{"sub/lib.go:1:needle", "top.go:1:needle"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("include *.go: got %v, want %v", got, want)
	}

	got = resultLines(runToolJSON(t, NewGrep(root), map[string]any{"pattern": "needle", "include": "sub/*.go"}))
	want = []string{"sub/lib.go:1:needle"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("include sub/*.go: got %v, want %v", got, want)
	}
}

func TestGrepSkipsIgnoredDirectories(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "keep.txt", "needle")
	writeTestFile(t, root, "vendor/v.txt", "needle")
	writeTestFile(t, root, "node_modules/n.txt", "needle")
	writeTestFile(t, root, ".git/g.txt", "needle")

	got := resultLines(runToolJSON(t, NewGrep(root), map[string]any{"pattern": "needle"}))
	if len(got) != 1 || got[0] != "keep.txt:1:needle" {
		t.Fatalf("got %v, want only keep.txt", got)
	}
}

func TestGrepContextLines(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "c.txt", "alpha\nneedle\nbeta\ngamma\nneedle\ndelta\n")

	got := resultLines(runToolJSON(t, NewGrep(root), map[string]any{"pattern": "needle", "context_lines": 1}))
	want := []string{
		"c.txt-1-alpha",
		"c.txt:2:needle",
		"c.txt-3-beta",
		"c.txt-4-gamma",
		"c.txt:5:needle",
		"c.txt-6-delta",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestGrepContextBlockSeparator(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "c.txt", "needle\nx\ny\nz\nneedle\n")

	got := resultLines(runToolJSON(t, NewGrep(root), map[string]any{"pattern": "needle", "context_lines": 1}))
	want := []string{"c.txt:1:needle", "c.txt-2-x", "--", "c.txt-4-z", "c.txt:5:needle"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestGrepSingleFile(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "sub/one.txt", "needle")

	got := resultLines(runToolJSON(t, NewGrep(root), map[string]any{"pattern": "needle", "path": "sub/one.txt"}))
	if len(got) != 1 || got[0] != "sub/one.txt:1:needle" {
		t.Fatalf("got %v", got)
	}
}

func TestGrepRejectsPathOutsideRoot(t *testing.T) {
	res := runToolJSON(t, NewGrep(t.TempDir()), map[string]any{"pattern": "x", "path": "../"})
	if !res.IsError {
		t.Fatalf("expected IsError for path outside root, got %+v", res)
	}
}

func TestGrepRejectsMissingPattern(t *testing.T) {
	res := runToolJSON(t, NewGrep(t.TempDir()), map[string]any{})
	if !res.IsError {
		t.Fatalf("expected IsError for missing pattern, got %+v", res)
	}
}

func TestGrepNoMatches(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "a.txt", "nothing here")

	res := runToolJSON(t, NewGrep(root), map[string]any{"pattern": "needle"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if res.Content != "no matches found" || res.Truncated {
		t.Fatalf("got %+v", res)
	}
}

func TestGrepContract(t *testing.T) {
	tl := NewGrep(t.TempDir())
	if tl.Name() != "Grep" {
		t.Fatalf("name = %q", tl.Name())
	}
	if !tl.ReadOnly(nil) || !tl.ConcurrencySafe(nil) {
		t.Fatal("Grep must be read-only and concurrency safe")
	}
	if !json.Valid(tl.InputSchema()) {
		t.Fatal("InputSchema is not valid JSON")
	}
	if tl.Description() == "" {
		t.Fatal("empty description")
	}
}

func TestGrepConcurrentUse(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		writeTestFile(t, root, name, "needle\n")
	}
	tl := NewGrep(root)
	const workers = 8
	done := make(chan struct{})
	for i := 0; i < workers; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			res, err := tl.Run(context.Background(), json.RawMessage(`{"pattern":"needle"}`), nil)
			if err != nil || res.IsError || len(resultLines(res)) != 3 {
				t.Errorf("concurrent run failed: err=%v res=%+v", err, res)
			}
		}()
	}
	for i := 0; i < workers; i++ {
		<-done
	}
}

var _ tool.Tool = NewGlob(".")
var _ tool.Tool = NewGrep(".")
