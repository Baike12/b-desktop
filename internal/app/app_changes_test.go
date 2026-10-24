package app

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Baike12/b-desktop/internal/workspace"
)

// initGitRepo 建一个带一次提交的 git 仓库，返回仓库根目录。
func initGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_TERMINAL_PROMPT=0",
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("-c", "commit.gpgsign=false", "commit", "-qm", "init")
	return root
}

func TestSessionFileWriteBindings(t *testing.T) {
	cfg := testConfig(t)
	app, _ := newTestApp(t, newFakeProvider())
	info, err := app.CreateSession(CreateSessionInput{Title: "写入", WorkDir: cfg.WorkDir})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := app.CreateSessionFile(info.ID, info.WorkDir, "docs", true); err != nil {
		t.Fatalf("CreateSessionFile dir: %v", err)
	}
	if err := app.CreateSessionFile(info.ID, info.WorkDir, "docs/notes.md", false); err != nil {
		t.Fatalf("CreateSessionFile file: %v", err)
	}
	if err := app.CreateSessionFile(info.ID, info.WorkDir, "docs/notes.md", false); !errors.Is(err, workspace.ErrExists) {
		t.Fatalf("duplicate create error = %v, want ErrExists", err)
	}
	if err := app.WriteSessionFile(info.ID, info.WorkDir, "docs/notes.md", "hello\n"); err != nil {
		t.Fatalf("WriteSessionFile: %v", err)
	}
	content, err := app.ReadSessionFile(info.ID, info.WorkDir, "docs/notes.md")
	if err != nil {
		t.Fatalf("ReadSessionFile: %v", err)
	}
	if content.Content != "hello\n" {
		t.Fatalf("content = %q", content.Content)
	}

	if err := app.RenameSessionFile(info.ID, info.WorkDir, "docs/notes.md", "docs/readme.md"); err != nil {
		t.Fatalf("RenameSessionFile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.WorkDir, "docs", "readme.md")); err != nil {
		t.Fatalf("renamed file missing: %v", err)
	}

	moved, err := app.MoveSessionFile(info.ID, info.WorkDir, "docs/readme.md", "")
	if err != nil {
		t.Fatalf("MoveSessionFile: %v", err)
	}
	if moved != "readme.md" {
		t.Fatalf("moved = %q, want readme.md", moved)
	}
	if _, err := os.Stat(filepath.Join(cfg.WorkDir, "readme.md")); err != nil {
		t.Fatalf("moved file missing: %v", err)
	}

	if err := app.DeleteSessionFile(info.ID, info.WorkDir, "readme.md"); err != nil {
		t.Fatalf("DeleteSessionFile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.WorkDir, "readme.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file still exists: %v", err)
	}
}

func TestSessionFileWriteRejectsTraversal(t *testing.T) {
	cfg := testConfig(t)
	app, _ := newTestApp(t, newFakeProvider())
	info, err := app.CreateSession(CreateSessionInput{Title: "越界", WorkDir: cfg.WorkDir})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := app.CreateSessionFile(info.ID, info.WorkDir, "../escape.txt", false); !errors.Is(err, workspace.ErrInvalidPath) {
		t.Fatalf("error = %v, want ErrInvalidPath", err)
	}
	if err := app.WriteSessionFile(info.ID, info.WorkDir, "../escape.txt", "x"); !errors.Is(err, workspace.ErrInvalidPath) {
		t.Fatalf("error = %v, want ErrInvalidPath", err)
	}
	if err := app.DeleteSessionFile(info.ID, info.WorkDir, "../escape.txt"); !errors.Is(err, workspace.ErrInvalidPath) {
		t.Fatalf("error = %v, want ErrInvalidPath", err)
	}
}

func TestSessionChangeBindings(t *testing.T) {
	root := initGitRepo(t)
	cfg := testConfig(t)
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "fresh.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	app, _ := newTestApp(t, newFakeProvider())
	info, err := app.CreateSession(CreateSessionInput{Title: "变更", WorkDir: root})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	_ = cfg

	set, err := app.ListSessionChanges(info.ID, info.WorkDir)
	if err != nil {
		t.Fatalf("ListSessionChanges: %v", err)
	}
	if !set.IsRepo {
		t.Fatal("IsRepo = false, want true")
	}
	paths := make([]string, 0, len(set.Changes))
	for _, change := range set.Changes {
		paths = append(paths, change.Path)
	}
	if !containsString(paths, "tracked.txt") || !containsString(paths, "fresh.txt") {
		t.Fatalf("changes = %+v", set.Changes)
	}

	diff, err := app.GetSessionFileDiff(info.ID, info.WorkDir, "tracked.txt", false)
	if err != nil {
		t.Fatalf("GetSessionFileDiff: %v", err)
	}
	if !strings.Contains(diff.Diff, "+three") {
		t.Fatalf("diff = %q", diff.Diff)
	}
	if diff.Additions != 1 {
		t.Fatalf("additions = %d, want 1", diff.Additions)
	}

	if err := app.StageSessionFiles(info.ID, info.WorkDir, []string{"tracked.txt"}); err != nil {
		t.Fatalf("StageSessionFiles: %v", err)
	}
	set, err = app.ListSessionChanges(info.ID, info.WorkDir)
	if err != nil {
		t.Fatalf("ListSessionChanges: %v", err)
	}
	staged := false
	for _, change := range set.Changes {
		if change.Path == "tracked.txt" && change.Staged {
			staged = true
		}
	}
	if !staged {
		t.Fatalf("tracked.txt not staged: %+v", set.Changes)
	}

	if err := app.UnstageSessionFiles(info.ID, info.WorkDir, []string{"tracked.txt"}); err != nil {
		t.Fatalf("UnstageSessionFiles: %v", err)
	}
	if err := app.DiscardSessionFile(info.ID, info.WorkDir, "tracked.txt", "", true); err != nil {
		t.Fatalf("DiscardSessionFile: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "tracked.txt"))
	if err != nil {
		t.Fatalf("read discarded file: %v", err)
	}
	if string(content) != "one\ntwo\n" {
		t.Fatalf("content = %q, want original", content)
	}
}

func TestSessionChangesOutsideRepository(t *testing.T) {
	cfg := testConfig(t)
	app, _ := newTestApp(t, newFakeProvider())
	info, err := app.CreateSession(CreateSessionInput{Title: "非仓库", WorkDir: cfg.WorkDir})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	set, err := app.ListSessionChanges(info.ID, info.WorkDir)
	if err != nil {
		t.Fatalf("ListSessionChanges: %v", err)
	}
	if set.IsRepo {
		t.Fatal("IsRepo = true, want false")
	}
	if len(set.Changes) != 0 {
		t.Fatalf("changes = %+v, want empty", set.Changes)
	}
}

func TestSessionFileSearch(t *testing.T) {
	cfg := testConfig(t)
	if err := os.MkdirAll(filepath.Join(cfg.WorkDir, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.WorkDir, "pkg", "widget.go"), []byte("package pkg\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	app, _ := newTestApp(t, newFakeProvider())
	info, err := app.CreateSession(CreateSessionInput{Title: "搜索", WorkDir: cfg.WorkDir})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	paths, err := app.SearchSessionFiles(info.ID, info.WorkDir, "widget")
	if err != nil {
		t.Fatalf("SearchSessionFiles: %v", err)
	}
	if len(paths) != 1 || paths[0] != "pkg/widget.go" {
		t.Fatalf("paths = %+v", paths)
	}
	if empty, err := app.SearchSessionFiles(info.ID, info.WorkDir, "  "); err != nil || len(empty) != 0 {
		t.Fatalf("empty query = %+v, err = %v", empty, err)
	}
}

// containsString 报告列表里是否存在目标字符串。
func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
