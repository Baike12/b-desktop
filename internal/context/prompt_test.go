package context

import (
	stdctx "context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Baike12/b-desktop/internal/tool"
)

func fixedNow() time.Time {
	return time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
}

// fakeGit 按参数返回脚本化的 git 输出。
func fakeGit(branch string, status string, calls *int) func(ctx stdctx.Context, dir string, args ...string) ([]byte, error) {
	return func(_ stdctx.Context, _ string, args ...string) ([]byte, error) {
		*calls++
		if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "--abbrev-ref" {
			if len(args) >= 3 && args[2] == "origin/HEAD" {
				return []byte("origin/main\n"), nil
			}
			return []byte(branch + "\n"), nil
		}
		switch args[0] {
		case "status":
			return []byte(status), nil
		case "log":
			return []byte("abc123 feat: first\ndef456 fix: second\n"), nil
		}
		return nil, fmt.Errorf("unexpected git args: %v", args)
	}
}

func TestSystemOrderAndInstructionLookup(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(filepath.Join(root, "AGENTS.md"), "ROOT AGENTS")
	mustWrite(filepath.Join(root, "CLAUDE.md"), "ROOT CLAUDE")
	mustWrite(filepath.Join(sub, "AGENTS.md"), "SUB AGENTS")
	mustWrite(filepath.Join(sub, "CLAUDE.md"), "SUB CLAUDE")

	gitCalls := 0
	b := NewBuilder(Options{
		WorkDir:     sub,
		Model:       "test-model",
		Now:         fixedNow,
		RunGit:      fakeGit("feature/x", " M main.go\n", &gitCalls),
		SystemExtra: "EXTRA-MARKER",
	})

	got, err := b.System(stdctx.Background(), []tool.Spec{
		{Name: "Read", Description: "Read a file\nwith details"},
		{Name: "Bash", Description: "Run shell commands"},
	})
	if err != nil {
		t.Fatalf("System: %v", err)
	}

	for _, want := range []string{
		"你是 b-desktop 桌面工作台里的编码 Agent",
		"<env>", "cwd: " + sub, "date: 2026-10-23", "model: test-model",
		"ROOT AGENTS", "SUB AGENTS",
		"branch: feature/x", "main branch: main", " M main.go", "abc123 feat: first",
		"- Read: Read a file with details", "- Bash: Run shell commands",
		"EXTRA-MARKER",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("System 缺少 %q\n%s", want, got)
		}
	}
	// 逻辑与 cc 相同：从根到工作目录逐级加载，越近的 AGENTS.md 优先级越高。
	if strings.Index(got, "ROOT AGENTS") > strings.Index(got, "SUB AGENTS") {
		t.Errorf("上层 AGENTS.md 应在下层之前\n%s", got)
	}
	if strings.Contains(got, "ROOT CLAUDE") || strings.Contains(got, "SUB CLAUDE") {
		t.Errorf("只加载 AGENTS.md，不应加载 CLAUDE.md\n%s", got)
	}

	assertOrder(t, got,
		"你是 b-desktop",
		"<env>",
		"<project-instructions>",
		"<git>",
		"<tools>",
		"EXTRA-MARKER",
	)
}

func TestSystemOmitsGitWhenUnavailable(t *testing.T) {
	dir := t.TempDir()
	b := NewBuilder(Options{
		WorkDir: dir,
		Now:     fixedNow,
		RunGit: func(stdctx.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("git not found")
		},
		ReadFile: func(string) ([]byte, error) { return nil, os.ErrNotExist },
	})
	got, err := b.System(stdctx.Background(), nil)
	if err != nil {
		t.Fatalf("git 不可用时不应返回错误: %v", err)
	}
	if strings.Contains(got, "<git>") {
		t.Fatalf("git 不可用时应省略整个 git 段\n%s", got)
	}
	if !strings.Contains(got, "<env>") {
		t.Fatalf("其余段落仍应存在\n%s", got)
	}
}

func TestBeginTurnKeepsStaticCache(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	files := map[string]string{
		filepath.Join(sub, "AGENTS.md"): "SUB AGENTS",
	}
	reads := 0
	readFile := func(path string) ([]byte, error) {
		reads++
		if content, ok := files[path]; ok {
			return []byte(content), nil
		}
		return nil, os.ErrNotExist
	}

	gitCalls := 0 // 只统计 branch 查询：每个 turn 恰好一次
	git := func(_ stdctx.Context, _ string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[1] == "--abbrev-ref" {
			if len(args) >= 3 && args[2] == "origin/HEAD" {
				return []byte("origin/main\n"), nil
			}
			gitCalls++
			return []byte(fmt.Sprintf("branch-%d\n", gitCalls)), nil
		}
		if args[0] == "status" {
			return []byte(""), nil
		}
		return []byte("c1 one\n"), nil
	}

	b := NewBuilder(Options{WorkDir: sub, Model: "m", Now: fixedNow, ReadFile: readFile, RunGit: git})

	if _, err := b.System(stdctx.Background(), nil); err != nil {
		t.Fatal(err)
	}
	readsAfterBuild := reads
	if readsAfterBuild == 0 {
		t.Fatal("首次构建应读取指令文件")
	}
	if gitCalls == 0 {
		t.Fatal("首次构建应执行 git")
	}

	// 同一 turn 内：静态层与 git 层都命中缓存。
	if _, err := b.System(stdctx.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if reads != readsAfterBuild {
		t.Fatalf("同一 turn 内指令文件读取次数从 %d 变为 %d", readsAfterBuild, reads)
	}
	if gitCalls != 1 {
		t.Fatalf("同一 turn 内 git 调用次数 = %d, want 1", gitCalls)
	}

	// BeginTurn 只失效 git 动态层，不重读指令文件。
	b.BeginTurn()
	got, err := b.System(stdctx.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if reads != readsAfterBuild {
		t.Fatalf("BeginTurn 不应清空静态缓存：读取次数 %d → %d", readsAfterBuild, reads)
	}
	if gitCalls != 2 {
		t.Fatalf("BeginTurn 后应重新获取 git，调用次数 = %d", gitCalls)
	}
	if !strings.Contains(got, "branch-2") {
		t.Fatalf("应使用新 turn 的 git 输出\n%s", got)
	}

	// Invalidate 清空全部缓存，重新读取指令文件。
	b.Invalidate()
	if _, err := b.System(stdctx.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if reads <= readsAfterBuild {
		t.Fatalf("Invalidate 后应重读指令文件，读取次数仍为 %d", reads)
	}
}

func TestInstructionHierarchyRootToLeaf(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(filepath.Join(sub, ".b-desktop"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".b-desktop"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(filepath.Join(root, "AGENTS.md"), "ROOT")
	mustWrite(filepath.Join(root, ".b-desktop", "AGENTS.md"), "ROOT-DOT")
	mustWrite(filepath.Join(sub, "AGENTS.md"), "SUB")
	mustWrite(filepath.Join(sub, ".b-desktop", "AGENTS.md"), "SUB-DOT")
	mustWrite(filepath.Join(sub, "CLAUDE.md"), "IGNORED")

	b := NewBuilder(Options{
		WorkDir: sub,
		Now:     fixedNow,
		RunGit:  func(stdctx.Context, string, ...string) ([]byte, error) { return nil, errors.New("no git") },
	})
	got, err := b.System(stdctx.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	positions := []int{
		strings.Index(got, "ROOT"),
		strings.Index(got, "ROOT-DOT"),
		strings.Index(got, "SUB"),
		strings.Index(got, "SUB-DOT"),
	}
	for i := 1; i < len(positions); i++ {
		if positions[i-1] < 0 || positions[i] <= positions[i-1] {
			t.Fatalf("指令加载顺序错误: %v\n%s", positions, got)
		}
	}
	if strings.Contains(got, "IGNORED") {
		t.Fatalf("CLAUDE.md 不应被加载\n%s", got)
	}
}

func TestInstructionTruncatedToMaxBytes(t *testing.T) {
	dir := t.TempDir()
	readFile := func(path string) ([]byte, error) {
		if path == filepath.Join(dir, "AGENTS.md") {
			return []byte("ABCDEFGHIJ"), nil
		}
		return nil, os.ErrNotExist
	}
	b := NewBuilder(Options{
		WorkDir:             dir,
		Now:                 fixedNow,
		MaxInstructionBytes: 8,
		ReadFile:            readFile,
		RunGit:              func(stdctx.Context, string, ...string) ([]byte, error) { return nil, errors.New("no git") },
	})
	got, err := b.System(stdctx.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "ABCDEFGH") {
		t.Fatalf("应保留前 8 字节\n%s", got)
	}
	if strings.Contains(got, "ABCDEFGHIJ") {
		t.Fatalf("应截断到 8 字节\n%s", got)
	}
}

func TestGitStatusTruncated(t *testing.T) {
	dir := t.TempDir()
	longStatus := strings.Repeat("M file.go\n", 400)
	gitCalls := 0
	b := NewBuilder(Options{
		WorkDir: dir,
		Now:     fixedNow,
		ReadFile: func(string) ([]byte, error) {
			return nil, os.ErrNotExist
		},
		RunGit: fakeGit("main", longStatus, &gitCalls),
	})
	got, err := b.System(stdctx.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "...(truncated)") {
		t.Fatalf("超长 git status 应被截断\n%s", got)
	}
}

// TestSystemToolUseGuidance 断言「专用工具优先」段按实际工具集生成。
// 依据 cc 的 getUsingYourToolsSection：只提示确实可用的工具，且没有 Bash 时不出现该段。
func TestSystemToolUseGuidance(t *testing.T) {
	newBuilder := func() *Builder {
		return NewBuilder(Options{
			WorkDir:  t.TempDir(),
			Now:      fixedNow,
			ReadFile: func(string) ([]byte, error) { return nil, os.ErrNotExist },
			RunGit:   func(stdctx.Context, string, ...string) ([]byte, error) { return nil, errors.New("no git") },
		})
	}

	full := []tool.Spec{
		{Name: "Read"}, {Name: "Write"}, {Name: "Edit"},
		{Name: "Glob"}, {Name: "Grep"}, {Name: "Bash"},
	}
	got, err := newBuilder().System(stdctx.Background(), full)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<tool-use>",
		"有专用工具时不要用 Bash 完成同样的操作",
		"读取文件用 Read，不要用 cat、head、tail、sed",
		"修改文件用 Edit，不要用 sed、awk",
		"新建或整体覆盖文件用 Write，不要用 heredoc 或 echo 重定向",
		"按文件名查找用 Glob，不要用 find 或 ls",
		"搜索文件内容用 Grep，不要用 grep 或 rg",
		"Bash 只用于系统命令",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("系统提示缺少 %q\n%s", want, got)
		}
	}
	assertOrder(t, got, "<tools>", "<tool-use>")

	// 工具池没有 Bash：不输出该段。
	noBash, err := newBuilder().System(stdctx.Background(), []tool.Spec{{Name: "Read"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(noBash, "<tool-use>") {
		t.Fatalf("没有 Bash 时不应输出 tool-use 段\n%s", noBash)
	}

	// 只有 Bash 与 Write：只提示 Write，不提不存在的 Read。
	partial, err := newBuilder().System(stdctx.Background(), []tool.Spec{{Name: "Bash"}, {Name: "Write"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(partial, "新建或整体覆盖文件用 Write") {
		t.Fatalf("应包含 Write 提示\n%s", partial)
	}
	if strings.Contains(partial, "读取文件用 Read") {
		t.Fatalf("不应提示不存在的 Read\n%s", partial)
	}
}

func assertOrder(t *testing.T, s string, markers ...string) {
	t.Helper()
	last := -1
	for _, m := range markers {
		idx := strings.Index(s, m)
		if idx < 0 {
			t.Fatalf("缺少标记 %q", m)
		}
		if idx <= last {
			t.Fatalf("标记 %q 顺序错误（位置 %d，前一个 %d）", m, idx, last)
		}
		last = idx
	}
}
