package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Baike12/b-desktop/internal/workspace"
)

func TestSessionFileBindings(t *testing.T) {
	cfg := testConfig(t)
	if err := os.MkdirAll(filepath.Join(cfg.WorkDir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.WorkDir, "src", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.WorkDir, "README.md"), []byte("# demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	app, _ := newTestApp(t, newFakeProvider())
	info, err := app.CreateSession(CreateSessionInput{Title: "文件", WorkDir: cfg.WorkDir})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	list, err := app.ListSessionFiles(info.ID, info.WorkDir, "")
	if err != nil {
		t.Fatalf("ListSessionFiles: %v", err)
	}
	if len(list.Entries) != 2 || list.Entries[0].Name != "src" || !list.Entries[0].IsDir {
		t.Fatalf("entries = %+v", list.Entries)
	}
	if list.Entries[1].Name != "README.md" || list.Entries[1].IsDir {
		t.Fatalf("entries = %+v", list.Entries)
	}

	sub, err := app.ListSessionFiles(info.ID, info.WorkDir, "src")
	if err != nil {
		t.Fatalf("ListSessionFiles src: %v", err)
	}
	if len(sub.Entries) != 1 || sub.Entries[0].Path != "src/main.go" {
		t.Fatalf("sub entries = %+v", sub.Entries)
	}

	content, err := app.ReadSessionFile(info.ID, info.WorkDir, "src/main.go")
	if err != nil {
		t.Fatalf("ReadSessionFile: %v", err)
	}
	if content.Content != "package main\n" || content.Binary || content.Truncated {
		t.Fatalf("content = %+v", content)
	}

	if _, err := app.ReadSessionFile(info.ID, info.WorkDir, "../secret"); !errors.Is(err, workspace.ErrInvalidPath) && !errors.Is(err, workspace.ErrOutsideRoot) {
		t.Fatalf("traversal error = %v", err)
	}
	if _, err := app.ListSessionFiles("bad/id", info.WorkDir, ""); err == nil {
		t.Fatal("invalid session id accepted")
	}
}

func TestSessionFileRequiresWorkDir(t *testing.T) {
	app, _ := newTestApp(t, newFakeProvider())
	if _, err := app.ListSessionFiles("s_missing", "", ""); !errors.Is(err, ErrWorkDirUnavailable) {
		t.Fatalf("error = %v, want ErrWorkDirUnavailable", err)
	}
}
