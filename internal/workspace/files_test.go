package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestListDirSortsAndHidesVCS(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"zeta", "Alpha", ".git", "src"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{"b.txt", "a.txt", ".env", ".DS_Store"} {
		writeTestFile(t, filepath.Join(root, file), "x")
	}

	list, err := ListDir(context.Background(), root, "")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	var got []string
	for _, entry := range list.Entries {
		got = append(got, entry.Name)
	}
	want := []string{"Alpha", "src", "zeta", ".env", "a.txt", "b.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	if list.Truncated {
		t.Fatal("unexpected truncated list")
	}
	for _, entry := range list.Entries {
		if entry.IsDir != (entry.Name == "Alpha" || entry.Name == "src" || entry.Name == "zeta") {
			t.Fatalf("entry %+v has wrong IsDir", entry)
		}
		if entry.Path != entry.Name {
			t.Fatalf("entry path = %q, want %q", entry.Path, entry.Name)
		}
	}
}

func TestListDirSubdirectoryPaths(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "src", "main.go"), "package main")

	list, err := ListDir(context.Background(), root, "src")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	if len(list.Entries) != 1 {
		t.Fatalf("entries = %+v", list.Entries)
	}
	if got := list.Entries[0].Path; got != "src/main.go" {
		t.Fatalf("path = %q, want src/main.go", got)
	}
}

func TestListDirRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	cases := []string{"../elsewhere", "/etc", "src/../../etc"}
	for _, rel := range cases {
		if _, err := ListDir(context.Background(), root, rel); !errors.Is(err, ErrInvalidPath) && !errors.Is(err, ErrOutsideRoot) {
			t.Fatalf("ListDir(%q) error = %v, want invalid/outside", rel, err)
		}
	}
}

func TestListDirRejectsSymlinkOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := ListDir(context.Background(), root, "link"); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("error = %v, want ErrOutsideRoot", err)
	}
}

func TestListDirShowsSymlinkAsFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "target"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "target"), filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	list, err := ListDir(context.Background(), root, "")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	var link *FileEntry
	for i := range list.Entries {
		if list.Entries[i].Name == "link" {
			link = &list.Entries[i]
		}
	}
	if link == nil {
		t.Fatal("symlink entry missing")
	}
	if link.IsDir || !link.IsSymlink {
		t.Fatalf("symlink entry = %+v, want file + symlink", *link)
	}
}

func TestReadFileTruncates(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "big.txt"), strings.Repeat("a", MaxPreviewBytes+10))

	content, err := ReadFile(context.Background(), root, "big.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !content.Truncated {
		t.Fatal("expected truncated content")
	}
	if len(content.Content) != MaxPreviewBytes {
		t.Fatalf("content length = %d, want %d", len(content.Content), MaxPreviewBytes)
	}
	if content.Size != int64(MaxPreviewBytes+10) {
		t.Fatalf("size = %d", content.Size)
	}
}

func TestReadFileBinary(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "bin.dat"), "abc\x00def")

	content, err := ReadFile(context.Background(), root, "bin.dat")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !content.Binary {
		t.Fatal("expected binary content")
	}
	if content.Content != "" {
		t.Fatalf("binary content = %q", content.Content)
	}
}

func TestReadFileRejectsDirectoriesAndEscapes(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(context.Background(), root, "dir"); !errors.Is(err, ErrNotFile) {
		t.Fatalf("dir error = %v, want ErrNotFile", err)
	}
	if _, err := ReadFile(context.Background(), root, ""); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("empty path error = %v", err)
	}
	if _, err := ReadFile(context.Background(), root, "../x"); !errors.Is(err, ErrInvalidPath) && !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("traversal error = %v", err)
	}
}

func TestReadFileRejectsSymlinkOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	writeTestFile(t, outside, "secret")
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := ReadFile(context.Background(), root, "link.txt"); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("error = %v, want ErrOutsideRoot", err)
	}
}

func TestReadFileSanitizesInvalidUTF8(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "bad.txt"), "ok\xffend")

	content, err := ReadFile(context.Background(), root, "bad.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if content.Binary {
		t.Fatal("invalid UTF-8 without NUL should not be treated as binary")
	}
	if !strings.Contains(content.Content, "ok") || !strings.Contains(content.Content, "end") {
		t.Fatalf("content = %q", content.Content)
	}
}
