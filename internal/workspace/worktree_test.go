package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requireGit 在缺少 git 时跳过依赖 git 的测试。
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

// runGitTest 在 dir 下执行 git 命令，屏蔽用户级配置以免签名或模板干扰测试。
func runGitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+filepath.Join(dir, ".gitconfig-none"),
		"GIT_AUTHOR_NAME=anc",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=anc",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// initTestRepo 创建带一次提交的临时 git 仓库。
func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGitTest(t, dir, "init")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runGitTest(t, dir, "add", "README.md")
	runGitTest(t, dir, "-c", "commit.gpgsign=false", "commit", "-m", "init")
	return dir
}

func TestCreateListRemoveWorktree(t *testing.T) {
	requireGit(t)
	repo := initTestRepo(t)
	ctx := context.Background()

	created, err := CreateWorktree(ctx, repo, "feature-x", "")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	wantPath := filepath.Join(ManagedWorktreesRoot(repo), "feature-x")
	if !samePath(created.Path, wantPath) {
		t.Fatalf("worktree path = %q, want %q", created.Path, wantPath)
	}
	if created.Branch != "feature-x" {
		t.Fatalf("branch = %q, want feature-x", created.Branch)
	}
	if created.Main {
		t.Fatalf("created worktree marked as main")
	}
	if !created.Managed {
		t.Fatalf("created worktree not marked as managed")
	}
	if created.Head == "" {
		t.Fatalf("created worktree has empty HEAD")
	}
	if _, err := os.Stat(filepath.Join(created.Path, "README.md")); err != nil {
		t.Fatalf("worktree checkout missing: %v", err)
	}

	list, err := ListWorktrees(ctx, repo)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len(list) = %d, want 2: %+v", len(list), list)
	}
	if !list[0].Main {
		t.Fatalf("first worktree is not main: %+v", list[0])
	}
	if !samePath(list[1].Path, wantPath) || list[1].Branch != "feature-x" {
		t.Fatalf("second worktree = %+v, want feature-x", list[1])
	}
	if list[0].Managed {
		t.Fatalf("main worktree marked as managed: %+v", list[0])
	}
	if !list[1].Managed {
		t.Fatalf("created worktree not marked as managed: %+v", list[1])
	}

	again, err := CreateWorktree(ctx, repo, "feature-x", "")
	if err != nil {
		t.Fatalf("CreateWorktree (idempotent): %v", err)
	}
	if !samePath(again.Path, wantPath) {
		t.Fatalf("idempotent path = %q, want %q", again.Path, wantPath)
	}
	list, err = ListWorktrees(ctx, repo)
	if err != nil {
		t.Fatalf("ListWorktrees after repeat: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("after repeat len(list) = %d, want 2", len(list))
	}

	if err := RemoveWorktree(ctx, repo, created.Path); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if _, err := os.Stat(created.Path); !os.IsNotExist(err) {
		t.Fatalf("worktree path still exists: %v", err)
	}
	list, err = ListWorktrees(ctx, repo)
	if err != nil {
		t.Fatalf("ListWorktrees after remove: %v", err)
	}
	if len(list) != 1 || !list[0].Main {
		t.Fatalf("after remove list = %+v, want only main", list)
	}
}

func TestCreateWorktreeMountsExistingBranch(t *testing.T) {
	requireGit(t)
	repo := initTestRepo(t)
	ctx := context.Background()
	runGitTest(t, repo, "branch", "existing-branch")
	head := runGitTest(t, repo, "rev-parse", "HEAD")

	wt, err := CreateWorktree(ctx, repo, "existing-branch", "")
	if err != nil {
		t.Fatalf("CreateWorktree(existing): %v", err)
	}
	if wt.Branch != "existing-branch" {
		t.Fatalf("branch = %q, want existing-branch", wt.Branch)
	}
	if wt.Head != head {
		t.Fatalf("HEAD = %q, want %q", wt.Head, head)
	}
	// 挂载分支不会新建分支，本地分支数量保持 1。
	branches := runGitTest(t, repo, "branch", "--list", "--format=%(refname:short)")
	if branches != "existing-branch" && !strings.Contains(branches, "existing-branch") {
		t.Fatalf("branches = %q, want existing-branch present", branches)
	}
}

func TestCreateWorktreeNestedBranch(t *testing.T) {
	requireGit(t)
	repo := initTestRepo(t)
	ctx := context.Background()

	wt, err := CreateWorktree(ctx, repo, "team/nested-feature", "HEAD")
	if err != nil {
		t.Fatalf("CreateWorktree(nested): %v", err)
	}
	wantPath := filepath.Join(ManagedWorktreesRoot(repo), "team", "nested-feature")
	if !samePath(wt.Path, wantPath) {
		t.Fatalf("nested path = %q, want %q", wt.Path, wantPath)
	}
	if wt.Branch != "team/nested-feature" {
		t.Fatalf("branch = %q", wt.Branch)
	}
}

func TestCreateWorktreeRejectsBranchInUse(t *testing.T) {
	requireGit(t)
	repo := initTestRepo(t)
	ctx := context.Background()

	external := filepath.Join(t.TempDir(), "external")
	runGitTest(t, repo, "worktree", "add", "-b", "busy", external, "HEAD")

	if _, err := CreateWorktree(ctx, repo, "busy", ""); !errors.Is(err, ErrBranchInUse) {
		t.Fatalf("branch in use error = %v, want ErrBranchInUse", err)
	}
}

func TestCreateWorktreeRejectsOccupiedPath(t *testing.T) {
	requireGit(t)
	repo := initTestRepo(t)
	ctx := context.Background()

	path := filepath.Join(ManagedWorktreesRoot(repo), "taken")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if _, err := CreateWorktree(ctx, repo, "taken", ""); !errors.Is(err, ErrWorktreePathExists) {
		t.Fatalf("occupied path error = %v, want ErrWorktreePathExists", err)
	}
}

func TestWorktreeRootsAreProjectScoped(t *testing.T) {
	requireGit(t)
	repo := initTestRepo(t)
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	ctx := context.Background()
	root, err := RepoRoot(ctx, sub)
	if err != nil {
		t.Fatalf("RepoRoot: %v", err)
	}
	if !samePath(root, repo) {
		t.Fatalf("RepoRoot(sub) = %q, want %q", root, repo)
	}

	want := filepath.Join(filepath.Dir(root), "worktrees", filepath.Base(root))
	got := ManagedWorktreesRoot(root)
	if !samePath(got, want) {
		t.Fatalf("ManagedWorktreesRoot(%q) = %q, want %q", root, got, want)
	}
	if isWithin(got, root) {
		t.Fatalf("managed worktrees root %q is inside repo %q", got, root)
	}
}

func TestCreateWorktreeRejectsInvalidInput(t *testing.T) {
	requireGit(t)
	repo := initTestRepo(t)
	ctx := context.Background()

	for _, branch := range []string{"", " ", "../escape", "-dash", "a..b", "a b", "a//b", "a.lock/b", "trailing/", "bad~ref"} {
		if _, err := CreateWorktree(ctx, repo, branch, ""); !errors.Is(err, ErrInvalidBranch) {
			t.Fatalf("branch %q error = %v, want ErrInvalidBranch", branch, err)
		}
	}
	if _, err := CreateWorktree(ctx, repo, "new-branch", "no-such-revision"); !errors.Is(err, ErrInvalidBase) {
		t.Fatalf("bad base error = %v, want ErrInvalidBase", err)
	}
	if _, err := CreateWorktree(ctx, repo, "new-branch", "--force"); !errors.Is(err, ErrInvalidBase) {
		t.Fatalf("dash base error = %v, want ErrInvalidBase", err)
	}
	if _, err := CreateWorktree(ctx, filepath.Join(t.TempDir(), "nope"), "b", ""); !errors.Is(err, ErrInvalidRepo) {
		t.Fatalf("missing repo error = %v, want ErrInvalidRepo", err)
	}
}

func TestRemoveWorktreeErrors(t *testing.T) {
	requireGit(t)
	repo := initTestRepo(t)
	ctx := context.Background()

	list, err := ListWorktrees(ctx, repo)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListWorktrees = %+v, %v", list, err)
	}
	if err := RemoveWorktree(ctx, repo, list[0].Path); !errors.Is(err, ErrMainWorktree) {
		t.Fatalf("remove main error = %v, want ErrMainWorktree", err)
	}
	if err := RemoveWorktree(ctx, repo, filepath.Join(repo, "not-a-worktree")); !errors.Is(err, ErrWorktreeNotFound) {
		t.Fatalf("remove unknown error = %v, want ErrWorktreeNotFound", err)
	}
	if err := RemoveWorktree(ctx, repo, "  "); !errors.Is(err, ErrInvalidWorktreePath) {
		t.Fatalf("remove empty error = %v, want ErrInvalidWorktreePath", err)
	}
}

func TestListWorktreesRejectsNonRepo(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	if _, err := ListWorktrees(context.Background(), dir); !errors.Is(err, ErrInvalidRepo) {
		t.Fatalf("non-repo error = %v, want ErrInvalidRepo", err)
	}
	if _, err := ListWorktrees(context.Background(), ""); !errors.Is(err, ErrInvalidRepo) {
		t.Fatalf("empty repo error = %v, want ErrInvalidRepo", err)
	}
}
