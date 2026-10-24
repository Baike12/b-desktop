package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// initChangesRepo 建一个带 README.md 提交的仓库，返回仓库根目录。
func initChangesRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	repo := initTestRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runGitTest(t, repo, "add", "tracked.txt")
	runGitTest(t, repo, "-c", "commit.gpgsign=false", "commit", "-m", "tracked")
	return repo
}

func TestChangesReportsModifiedUntrackedAndAdded(t *testing.T) {
	root := initChangesRepo(t)
	ctx := context.Background()

	writeTestFile(t, filepath.Join(root, "tracked.txt"), "one\ntwo\nthree\n")
	writeTestFile(t, filepath.Join(root, "fresh.txt"), "a\nb\n")

	set, err := Changes(ctx, root)
	if err != nil {
		t.Fatalf("Changes: %v", err)
	}
	if !set.IsRepo {
		t.Fatal("IsRepo = false, want true")
	}
	byPath := map[string]FileChange{}
	for _, change := range set.Changes {
		byPath[change.Path] = change
	}
	modified, ok := byPath["tracked.txt"]
	if !ok {
		t.Fatalf("tracked.txt missing from %+v", set.Changes)
	}
	if modified.Status != ChangeModified {
		t.Fatalf("tracked status = %q, want modified", modified.Status)
	}
	if modified.Additions != 1 || modified.Deletions != 0 {
		t.Fatalf("tracked stats = +%d -%d, want +1 -0", modified.Additions, modified.Deletions)
	}
	untracked, ok := byPath["fresh.txt"]
	if !ok {
		t.Fatalf("fresh.txt missing from %+v", set.Changes)
	}
	if untracked.Status != ChangeUntracked {
		t.Fatalf("fresh status = %q, want untracked", untracked.Status)
	}
	if untracked.Additions != 2 {
		t.Fatalf("fresh additions = %d, want 2", untracked.Additions)
	}
}

func TestChangesSeparatesStagedAndUnstaged(t *testing.T) {
	root := initChangesRepo(t)
	ctx := context.Background()

	writeTestFile(t, filepath.Join(root, "staged.txt"), "s\n")
	writeTestFile(t, filepath.Join(root, "tracked.txt"), "one\ntwo\nthree\n")
	runGitTest(t, root, "add", "staged.txt")

	set, err := Changes(ctx, root)
	if err != nil {
		t.Fatalf("Changes: %v", err)
	}
	var staged, unstaged *FileChange
	for i := range set.Changes {
		change := set.Changes[i]
		if change.Path != "staged.txt" {
			continue
		}
		if change.Staged {
			staged = &set.Changes[i]
		} else {
			unstaged = &set.Changes[i]
		}
	}
	if staged == nil || staged.Status != ChangeAdded {
		t.Fatalf("staged entry = %+v, want added", staged)
	}
	if unstaged != nil {
		t.Fatalf("unexpected unstaged entry %+v", unstaged)
	}
}

func TestChangesOutsideRepositoryIsNotAnError(t *testing.T) {
	root := t.TempDir()
	set, err := Changes(context.Background(), root)
	if err != nil {
		t.Fatalf("Changes: %v", err)
	}
	if set.IsRepo {
		t.Fatal("IsRepo = true, want false")
	}
	if len(set.Changes) != 0 {
		t.Fatalf("changes = %+v, want empty", set.Changes)
	}
}

func TestFileDiffOfReturnsUnifiedDiff(t *testing.T) {
	root := initChangesRepo(t)
	writeTestFile(t, filepath.Join(root, "tracked.txt"), "one\ntwo\nthree\n")

	diff, err := FileDiffOf(context.Background(), root, "tracked.txt", false)
	if err != nil {
		t.Fatalf("FileDiffOf: %v", err)
	}
	if !strings.Contains(diff.Diff, "+three") {
		t.Fatalf("diff missing added line: %q", diff.Diff)
	}
	if diff.Additions != 1 {
		t.Fatalf("additions = %d, want 1", diff.Additions)
	}
}

func TestFileDiffOfSynthesizesUntrackedFile(t *testing.T) {
	root := initChangesRepo(t)
	writeTestFile(t, filepath.Join(root, "fresh.txt"), "a\nb\n")

	diff, err := FileDiffOf(context.Background(), root, "fresh.txt", false)
	if err != nil {
		t.Fatalf("FileDiffOf: %v", err)
	}
	if !strings.Contains(diff.Diff, "@@ -0,0 +1,2 @@") {
		t.Fatalf("diff missing new-file hunk: %q", diff.Diff)
	}
	if !strings.Contains(diff.Diff, "+a") || !strings.Contains(diff.Diff, "+b") {
		t.Fatalf("diff missing added lines: %q", diff.Diff)
	}
	if diff.Additions != 2 {
		t.Fatalf("additions = %d, want 2", diff.Additions)
	}
}

func TestStageAndUnstagePaths(t *testing.T) {
	root := initChangesRepo(t)
	ctx := context.Background()
	writeTestFile(t, filepath.Join(root, "tracked.txt"), "one\ntwo\nthree\n")

	if err := StagePaths(ctx, root, []string{"tracked.txt"}); err != nil {
		t.Fatalf("StagePaths: %v", err)
	}
	set, err := Changes(ctx, root)
	if err != nil {
		t.Fatalf("Changes: %v", err)
	}
	found := false
	for _, change := range set.Changes {
		if change.Path == "tracked.txt" && change.Staged {
			found = true
		}
	}
	if !found {
		t.Fatalf("no staged entry for tracked.txt: %+v", set.Changes)
	}

	if err := UnstagePaths(ctx, root, []string{"tracked.txt"}); err != nil {
		t.Fatalf("UnstagePaths: %v", err)
	}
	set, err = Changes(ctx, root)
	if err != nil {
		t.Fatalf("Changes: %v", err)
	}
	for _, change := range set.Changes {
		if change.Path == "tracked.txt" && change.Staged {
			t.Fatalf("tracked.txt still staged: %+v", set.Changes)
		}
	}
}

func TestDiscardPathRestoresTrackedFile(t *testing.T) {
	root := initChangesRepo(t)
	writeTestFile(t, filepath.Join(root, "tracked.txt"), "one\ntwo\nthree\n")

	if err := DiscardPath(context.Background(), root, "tracked.txt", "", true); err != nil {
		t.Fatalf("DiscardPath: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "tracked.txt"))
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if string(content) != "one\ntwo\n" {
		t.Fatalf("content = %q, want original", content)
	}
}

func TestDiscardPathRemovesUntrackedFile(t *testing.T) {
	root := initChangesRepo(t)
	writeTestFile(t, filepath.Join(root, "fresh.txt"), "a\n")

	if err := DiscardPath(context.Background(), root, "fresh.txt", "", false); err != nil {
		t.Fatalf("DiscardPath: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "fresh.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("untracked file still exists: %v", err)
	}
}

func TestPathPrefixStripAndJoin(t *testing.T) {
	prefix := pathPrefix{prefix: "pkg"}
	got, ok := prefix.strip("pkg/src/a.go")
	if !ok || got != "src/a.go" {
		t.Fatalf("strip = %q, %v; want src/a.go, true", got, ok)
	}
	if _, ok := prefix.strip("other/a.go"); ok {
		t.Fatal("strip accepted a path outside the work dir")
	}
	joined, ok := prefix.join("src/a.go")
	if !ok || joined != "pkg/src/a.go" {
		t.Fatalf("join = %q, %v; want pkg/src/a.go, true", joined, ok)
	}
	if _, ok := prefix.join("../escape.go"); ok {
		t.Fatal("join accepted an escaping path")
	}
}

func TestDiscardPathOnlyWorktreeKeepsStagedContent(t *testing.T) {
	root := initChangesRepo(t)
	ctx := context.Background()

	// 先暂存一版，再在工作区改一次：两个状态并存。
	writeTestFile(t, filepath.Join(root, "tracked.txt"), "one\ntwo\nstaged\n")
	runGitTest(t, root, "add", "tracked.txt")
	writeTestFile(t, filepath.Join(root, "tracked.txt"), "one\ntwo\nstaged\nworktree\n")

	if err := DiscardPath(ctx, root, "tracked.txt", "", false); err != nil {
		t.Fatalf("DiscardPath: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "tracked.txt"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(content) != "one\ntwo\nstaged\n" {
		t.Fatalf("content = %q, want the staged version", content)
	}
	set, err := Changes(ctx, root)
	if err != nil {
		t.Fatalf("Changes: %v", err)
	}
	for _, change := range set.Changes {
		if change.Path == "tracked.txt" && !change.Staged {
			t.Fatalf("worktree change should be gone: %+v", set.Changes)
		}
	}
}

func TestStagePathsRejectsEmptyPathList(t *testing.T) {
	root := initChangesRepo(t)
	writeTestFile(t, filepath.Join(root, "other.txt"), "other\n")
	ctx := context.Background()

	// 全是空白的路径不能退化成「暂存整个仓库」。
	if err := StagePaths(ctx, root, []string{"  ", ""}); !errors.Is(err, ErrPathRequired) {
		t.Fatalf("err = %v, want ErrPathRequired", err)
	}
	if err := UnstagePaths(ctx, root, []string{"  ", ""}); !errors.Is(err, ErrPathRequired) {
		t.Fatalf("err = %v, want ErrPathRequired", err)
	}
	set, err := Changes(ctx, root)
	if err != nil {
		t.Fatalf("Changes: %v", err)
	}
	for _, change := range set.Changes {
		if change.Path == "other.txt" && change.Staged {
			t.Fatalf("unrelated file was staged: %+v", set.Changes)
		}
	}
}

func TestStagePathsRejectsPathspecMagic(t *testing.T) {
	root := initChangesRepo(t)
	writeTestFile(t, filepath.Join(root, "other.txt"), "other\n")
	ctx := context.Background()

	// ":!other.txt" 这类 pathspec magic 不能被当成路径。
	if err := StagePaths(ctx, root, []string{":!other.txt"}); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("err = %v, want ErrInvalidPath", err)
	}
	set, err := Changes(ctx, root)
	if err != nil {
		t.Fatalf("Changes: %v", err)
	}
	for _, change := range set.Changes {
		if change.Path == "other.txt" && change.Staged {
			t.Fatalf("magic pathspec staged other files: %+v", set.Changes)
		}
	}
}

func TestDiscardPathKeepsStagedNewFileOnWorktreeDiscard(t *testing.T) {
	root := initChangesRepo(t)
	ctx := context.Background()

	// 新文件已暂存（v1），工作区又改成了 v2。
	writeTestFile(t, filepath.Join(root, "new.txt"), "v1\n")
	runGitTest(t, root, "add", "new.txt")
	writeTestFile(t, filepath.Join(root, "new.txt"), "v2\n")

	// 只放弃工作区改动：磁盘内容要回到暂存区的 v1，文件不能被删掉。
	if err := DiscardPath(ctx, root, "new.txt", "", false); err != nil {
		t.Fatalf("DiscardPath: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "new.txt"))
	if err != nil {
		t.Fatalf("staged new file was removed: %v", err)
	}
	if string(content) != "v1\n" {
		t.Fatalf("content = %q, want the staged version", content)
	}
}

func TestDiscardPathRestoresRenamedFile(t *testing.T) {
	root := initChangesRepo(t)
	ctx := context.Background()

	// 暂存一次重命名：top.txt -> renamed.txt。
	runGitTest(t, root, "mv", "tracked.txt", "renamed.txt")

	if err := DiscardPath(ctx, root, "renamed.txt", "tracked.txt", true); err != nil {
		t.Fatalf("DiscardPath: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "tracked.txt")); err != nil {
		t.Fatalf("source path not restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "renamed.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("renamed path still exists: %v", err)
	}
}

func TestChangesCountsUntrackedLinesInSubdirectory(t *testing.T) {
	root := initChangesRepo(t)
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "sub", "new.txt"), "a\nb\nc\n")

	// 工作目录是仓库的子目录时，未跟踪文件的行数也要正确。
	set, err := Changes(ctx, filepath.Join(root, "sub"))
	if err != nil {
		t.Fatalf("Changes: %v", err)
	}
	found := false
	for _, change := range set.Changes {
		if change.Path == "new.txt" {
			found = true
			if change.Additions != 3 {
				t.Fatalf("additions = %d, want 3", change.Additions)
			}
		}
	}
	if !found {
		t.Fatalf("new.txt missing from %+v", set.Changes)
	}
}
