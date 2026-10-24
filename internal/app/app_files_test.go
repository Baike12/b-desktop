package app

import (
	"encoding/base64"
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

func TestReadSessionImage(t *testing.T) {
	cfg := testConfig(t)
	data := []byte("\x89PNG\r\n\x1a\nfake")
	if err := os.MkdirAll(filepath.Join(cfg.WorkDir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.WorkDir, "assets", "logo.png"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	app, _ := newTestApp(t, newFakeProvider())
	info, err := app.CreateSession(CreateSessionInput{Title: "图片", WorkDir: cfg.WorkDir})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	image, err := app.ReadSessionImage(info.ID, info.WorkDir, "assets/logo.png")
	if err != nil {
		t.Fatalf("ReadSessionImage: %v", err)
	}
	if image.MediaType != "image/png" || image.Path != "assets/logo.png" || image.Size != int64(len(data)) {
		t.Fatalf("image = %+v", image)
	}
	if image.DataBase64 != base64.StdEncoding.EncodeToString(data) {
		t.Fatalf("base64 = %q", image.DataBase64)
	}
	if _, err := app.ReadSessionImage(info.ID, info.WorkDir, "README.md"); !errors.Is(err, workspace.ErrNotImage) {
		t.Fatalf("non-image error = %v, want ErrNotImage", err)
	}
}
