package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Baike12/b-desktop/internal/tool"
)

// runBash 以 root 为工作目录执行一次 Bash 调用。
func runBash(t *testing.T, root string, input any) (tool.Result, error) {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	return NewBash(root).Run(context.Background(), raw, nil)
}

func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("posix shell command")
	}
}

func TestBashEcho(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	res, err := runBash(t, root, map[string]any{"command": "echo hello"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %s", res.Content)
	}
	if res.Meta["exit_code"] != "0" {
		t.Fatalf("exit_code = %q, want 0", res.Meta["exit_code"])
	}
	if !strings.Contains(res.Content, "hello") {
		t.Fatalf("content missing stdout: %q", res.Content)
	}
	if !strings.Contains(res.Content, "exit code: 0") {
		t.Fatalf("content missing exit code line: %q", res.Content)
	}
}

func TestBashUsesWorkingDirectory(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "marker.txt"), []byte("in-root"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := runBash(t, root, map[string]any{"argv": []string{"cat", "marker.txt"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError || !strings.Contains(res.Content, "in-root") {
		t.Fatalf("command did not run in root: %+v", res)
	}
}

func TestBashNonZeroExitIsNotAnException(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	res, err := runBash(t, root, map[string]any{"command": "echo out; echo err >&2; exit 3"})
	if err != nil {
		t.Fatalf("Run returned error for non-zero exit: %v", err)
	}
	if res.IsError {
		t.Fatalf("non-zero exit must not be an error result: %s", res.Content)
	}
	if res.Meta["exit_code"] != "3" {
		t.Fatalf("exit_code = %q, want 3", res.Meta["exit_code"])
	}
	if !strings.Contains(res.Content, "exit code: 3") {
		t.Fatalf("content missing exit code: %q", res.Content)
	}
	if !strings.Contains(res.Content, "out") || !strings.Contains(res.Content, "err") {
		t.Fatalf("content missing one of the streams: %q", res.Content)
	}
	if !strings.Contains(res.Content, "--- stdout ---") || !strings.Contains(res.Content, "--- stderr ---") {
		t.Fatalf("streams not separated: %q", res.Content)
	}
}

func TestBashTimeout(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	start := time.Now()
	res, err := runBash(t, root, map[string]any{"command": "sleep 30", "timeout_ms": 200})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("timeout must be a result, got error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("timeout result should be marked as error: %+v", res)
	}
	if res.Meta["timed_out"] != "true" {
		t.Fatalf("timed_out = %q, want true", res.Meta["timed_out"])
	}
	if !strings.Contains(res.Content, "timed out after 200ms") {
		t.Fatalf("content missing timeout notice: %q", res.Content)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("timeout took too long: %s", elapsed)
	}
}

func TestBashTimeoutKillsDescendants(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	marker := filepath.Join(root, "leaked.txt")

	start := time.Now()
	// 后台子 shell 自己会在 0.6s 后写文件：只杀直接子进程时它仍会泄漏。
	_, err := runBash(t, root, map[string]any{
		"command":    "(sleep 0.6; echo leaked > leaked.txt) & wait",
		"timeout_ms": 150,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout took too long: %s", elapsed)
	}

	time.Sleep(1200 * time.Millisecond)
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatalf("descendant survived the timeout and wrote %s", marker)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("stat marker: %v", statErr)
	}
}

func TestBashCancel(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	raw, err := json.Marshal(map[string]any{"command": "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, runErr := NewBash(root).Run(ctx, raw, nil)
	if runErr == nil {
		t.Fatal("canceled run returned nil error")
	}
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", runErr)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("cancel took too long: %s", elapsed)
	}
}

func TestBashTruncatesOutput(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	res, err := runBash(t, root, map[string]any{
		"command": "head -c 40000 /dev/zero | tr '\\0' 'a'",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Truncated {
		t.Fatal("Truncated = false, want true")
	}
	if res.Meta["truncated"] != "true" {
		t.Fatalf("meta truncated = %q", res.Meta["truncated"])
	}
	if res.Meta["stdout_bytes"] != "40000" {
		t.Fatalf("stdout_bytes = %q, want 40000", res.Meta["stdout_bytes"])
	}
	if !strings.Contains(res.Content, "stdout truncated: kept 30000 of 40000 bytes") {
		t.Fatalf("content missing truncation notice: %q", tail(res.Content, 200))
	}
	if len(res.Content) >= 40000 {
		t.Fatalf("content was not bounded: %d bytes", len(res.Content))
	}
}

func TestBashRejectsAmbiguousInput(t *testing.T) {
	root := t.TempDir()
	for name, in := range map[string]map[string]any{
		"neither": {},
		"both":    {"command": "echo hi", "argv": []string{"echo", "hi"}},
	} {
		res, err := runBash(t, root, in)
		if err != nil {
			t.Fatalf("%s: Run error: %v", name, err)
		}
		if !res.IsError {
			t.Fatalf("%s: expected error result, got %+v", name, res)
		}
	}
}

func TestBashReportsProgress(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	raw, err := json.Marshal(map[string]any{"command": "echo progress"})
	if err != nil {
		t.Fatal(err)
	}
	var last tool.Progress
	_, runErr := NewBash(root).Run(context.Background(), raw, func(p tool.Progress) { last = p })
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if last.Text != "done" || last.Bytes == 0 {
		t.Fatalf("unexpected last progress: %+v", last)
	}
}

func TestBashFlags(t *testing.T) {
	b := NewBash(t.TempDir())
	if b.Name() != "Bash" {
		t.Fatalf("Name = %q", b.Name())
	}
	if b.ReadOnly(nil) {
		t.Fatal("Bash must not be read-only")
	}
	if b.ConcurrencySafe(nil) {
		t.Fatal("Bash must not be concurrency safe")
	}
	if !json.Valid(b.InputSchema()) {
		t.Fatal("InputSchema is not valid JSON")
	}
	desc := b.Description()
	if desc == "" {
		t.Fatal("Description is empty")
	}
	// 与 cc 的 Bash 工具描述一致：明确把读、写、搜索引导到专用工具。
	for _, want := range []string{"IMPORTANT", "Use Read, Edit, Write, Glob and Grep instead", "cat", "sed"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("Description missing %q:\n%s", want, desc)
		}
	}
}

// tail 返回 s 的最后 n 个字节，便于失败信息可读。
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
