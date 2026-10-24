package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateEntryCreatesFileAndDirectory(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	if err := CreateEntry(ctx, root, "docs", true); err != nil {
		t.Fatalf("CreateEntry dir: %v", err)
	}
	if info, err := os.Stat(filepath.Join(root, "docs")); err != nil || !info.IsDir() {
		t.Fatalf("directory not created: %v", err)
	}
	if err := CreateEntry(ctx, root, "docs/readme.md", false); err != nil {
		t.Fatalf("CreateEntry file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "docs", "readme.md")); err != nil {
		t.Fatalf("file not created: %v", err)
	}
}

func TestCreateEntryRejectsExistingPath(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "a.txt"), "x")

	err := CreateEntry(context.Background(), root, "a.txt", false)
	if !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v, want ErrExists", err)
	}
}

func TestCreateEntryRejectsPathEscape(t *testing.T) {
	root := t.TempDir()
	err := CreateEntry(context.Background(), root, "../escape.txt", false)
	if !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("err = %v, want ErrInvalidPath", err)
	}
}

func TestRenameEntryMovesFile(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "old.txt"), "data")

	if err := RenameEntry(context.Background(), root, "old.txt", "new.txt"); err != nil {
		t.Fatalf("RenameEntry: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "old.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old file still exists: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "new.txt"))
	if err != nil || string(content) != "data" {
		t.Fatalf("content = %q, err = %v", content, err)
	}
}

func TestRenameEntryRejectsExistingTarget(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "a.txt"), "a")
	writeTestFile(t, filepath.Join(root, "b.txt"), "b")

	err := RenameEntry(context.Background(), root, "a.txt", "b.txt")
	if !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v, want ErrExists", err)
	}
}

func TestMoveEntryMovesIntoDirectory(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "src.txt"), "x")
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}

	moved, err := MoveEntry(context.Background(), root, "src.txt", "pkg")
	if err != nil {
		t.Fatalf("MoveEntry: %v", err)
	}
	if moved != "pkg/src.txt" {
		t.Fatalf("moved = %q, want %q", moved, "pkg/src.txt")
	}
	if _, err := os.Stat(filepath.Join(root, "pkg", "src.txt")); err != nil {
		t.Fatalf("file not moved: %v", err)
	}
}

func TestMoveEntryRejectsMoveIntoSelf(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "dir", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := MoveEntry(context.Background(), root, "dir", "dir/sub")
	if !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("err = %v, want ErrInvalidPath", err)
	}
}

func TestDeleteEntryRemovesFileAndDirectory(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "a.txt"), "x")
	writeTestFile(t, filepath.Join(root, "dir", "b.txt"), "y")
	ctx := context.Background()

	if err := DeleteEntry(ctx, root, "a.txt"); err != nil {
		t.Fatalf("DeleteEntry file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file still exists: %v", err)
	}
	if err := DeleteEntry(ctx, root, "dir"); err != nil {
		t.Fatalf("DeleteEntry dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "dir")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dir still exists: %v", err)
	}
}

func TestDeleteEntryRemovesSymlinkNotTarget(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeTestFile(t, filepath.Join(outside, "keep.txt"), "keep")
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(filepath.Join(outside, "keep.txt"), link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	if err := DeleteEntry(context.Background(), root, "link.txt"); err != nil {
		t.Fatalf("DeleteEntry: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "keep.txt")); err != nil {
		t.Fatalf("symlink target removed: %v", err)
	}
}

func TestWriteFileReplacesContent(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "a.txt"), "old")

	if err := WriteFile(context.Background(), root, "a.txt", "new"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "a.txt"))
	if err != nil || string(content) != "new" {
		t.Fatalf("content = %q, err = %v", content, err)
	}
}

func TestWriteFileRejectsOversizedContent(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "a.txt"), "old")

	err := WriteFile(context.Background(), root, "a.txt", strings.Repeat("x", MaxWriteBytes+1))
	if !errors.Is(err, ErrWriteTooLarge) {
		t.Fatalf("err = %v, want ErrWriteTooLarge", err)
	}
}

func TestWriteFileRejectsPathEscape(t *testing.T) {
	root := t.TempDir()
	err := WriteFile(context.Background(), root, "../outside.txt", "x")
	if !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("err = %v, want ErrInvalidPath", err)
	}
}

func TestRenameEntryMovesSymlinkItself(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "target.txt"), "target")
	if err := os.Symlink(filepath.Join(root, "target.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	if err := RenameEntry(context.Background(), root, "link.txt", "renamed.txt"); err != nil {
		t.Fatalf("RenameEntry: %v", err)
	}
	// 被改名的是链接本身：目标文件不能被动。
	if _, err := os.Stat(filepath.Join(root, "target.txt")); err != nil {
		t.Fatalf("symlink target was renamed: %v", err)
	}
	info, err := os.Lstat(filepath.Join(root, "renamed.txt"))
	if err != nil {
		t.Fatalf("renamed symlink missing: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("renamed entry is not a symlink")
	}
}

func TestMoveEntryMovesSymlinkItself(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "target.txt"), "target")
	if err := os.MkdirAll(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "target.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	moved, err := MoveEntry(context.Background(), root, "link.txt", "dir")
	if err != nil {
		t.Fatalf("MoveEntry: %v", err)
	}
	if moved != "dir/link.txt" {
		t.Fatalf("moved = %q, want dir/link.txt", moved)
	}
	if _, err := os.Stat(filepath.Join(root, "target.txt")); err != nil {
		t.Fatalf("symlink target was moved: %v", err)
	}
	info, err := os.Lstat(filepath.Join(root, "dir", "link.txt"))
	if err != nil {
		t.Fatalf("moved symlink missing: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("moved entry is not a symlink")
	}
}

func TestDeleteEntryRejectsSymlinkedParentDirectory(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeTestFile(t, filepath.Join(outside, "secret.txt"), "secret")
	if err := os.MkdirAll(filepath.Join(outside, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(outside, "sub", "inner.txt"), "inner")
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	// 中间目录段是符号链接时，删除必须被拒绝：
	// 只做词法检查会真的删掉工作目录外的文件。
	if err := DeleteEntry(context.Background(), root, "link/secret.txt"); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("err = %v, want ErrOutsideRoot", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "secret.txt")); err != nil {
		t.Fatalf("file outside the work dir was deleted: %v", err)
	}
	if err := DeleteEntry(context.Background(), root, "link/sub"); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("err = %v, want ErrOutsideRoot", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "sub", "inner.txt")); err != nil {
		t.Fatalf("directory outside the work dir was deleted: %v", err)
	}
}
