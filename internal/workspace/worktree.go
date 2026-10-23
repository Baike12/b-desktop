package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// worktreeDirName 是工作树相对仓库根目录的位置。
const worktreeDirName = ".b-desktop/worktrees"

// worktree 相关错误哨兵。
var (
	// ErrGitUnavailable 表示系统 PATH 中找不到 git。
	ErrGitUnavailable = errors.New("workspace: git is not available")
	// ErrInvalidRepo 表示仓库路径不存在或不是 git 仓库。
	ErrInvalidRepo = errors.New("workspace: invalid repository")
	// ErrInvalidBranch 表示分支名不合法（含路径穿越或非法字符）。
	ErrInvalidBranch = errors.New("workspace: invalid branch name")
	// ErrInvalidBase 表示创建分支时的基线修订不合法或不存在。
	ErrInvalidBase = errors.New("workspace: invalid base revision")
	// ErrInvalidWorktreePath 表示工作树路径为空。
	ErrInvalidWorktreePath = errors.New("workspace: invalid worktree path")
	// ErrWorktreeNotFound 表示路径不是该仓库已登记的工作树。
	ErrWorktreeNotFound = errors.New("workspace: worktree not found")
	// ErrMainWorktree 表示试图删除主工作树。
	ErrMainWorktree = errors.New("workspace: cannot remove the main worktree")
)

// Worktree 描述 git worktree list 中的一个工作树。
type Worktree struct {
	Path     string // 工作树绝对路径
	Branch   string // 短分支名；游离 HEAD 时为空
	Head     string // HEAD 提交
	Detached bool   // 是否游离 HEAD
	Bare     bool   // 是否裸仓库
	Locked   bool   // 是否被 git worktree lock 锁定
	Prunable bool   // 是否可被 git worktree prune 清理
	Main     bool   // 是否为主工作树（列表第一项）
}

// ListWorktrees 返回 repo 所属仓库的全部工作树，顺序与 git worktree list 一致（主工作树在前）。
func ListWorktrees(ctx context.Context, repo string) ([]Worktree, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("workspace: list worktrees: %w", err)
	}
	dir, err := repoDir(repo)
	if err != nil {
		return nil, err
	}
	out, err := runGit(ctx, dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrInvalidRepo, repo, err)
	}
	return parseWorktreeList(out), nil
}

// CreateWorktree 在 <repo>/.b-desktop/worktrees/<branch> 创建或挂载工作树。
//
//   - 该路径已是已登记的工作树时直接返回，不重复创建；
//   - 分支已存在时执行 git worktree add <path> <branch>，直接挂载现有分支；
//   - 分支不存在时以 base 为基线创建分支；base 为空取 HEAD。
//
// repo 可以是仓库内任意子目录，工作树始终落在主仓库根目录下。
func CreateWorktree(ctx context.Context, repo, branch, base string) (Worktree, error) {
	if err := ctx.Err(); err != nil {
		return Worktree{}, fmt.Errorf("workspace: create worktree: %w", err)
	}
	trimmedBranch := strings.TrimSpace(branch)
	if err := validateBranch(trimmedBranch); err != nil {
		return Worktree{}, err
	}
	trimmedBase := strings.TrimSpace(base)
	if trimmedBase != "" && (strings.HasPrefix(trimmedBase, "-") || strings.ContainsAny(trimmedBase, " \t\r\n")) {
		return Worktree{}, fmt.Errorf("%w: %q", ErrInvalidBase, trimmedBase)
	}

	root, err := mainRepoRoot(ctx, repo)
	if err != nil {
		return Worktree{}, err
	}
	path, err := worktreePath(root, trimmedBranch)
	if err != nil {
		return Worktree{}, err
	}

	existing, err := ListWorktrees(ctx, root)
	if err != nil {
		return Worktree{}, err
	}
	for _, wt := range existing {
		if samePath(wt.Path, path) {
			return wt, nil
		}
	}

	exists, err := localBranchExists(ctx, root, trimmedBranch)
	if err != nil {
		return Worktree{}, err
	}
	if exists {
		if _, err := runGit(ctx, root, "worktree", "add", path, trimmedBranch); err != nil {
			return Worktree{}, fmt.Errorf("workspace: mount worktree %q: %w", trimmedBranch, err)
		}
	} else {
		if trimmedBase == "" {
			trimmedBase = "HEAD"
		}
		if _, err := runGit(ctx, root, "rev-parse", "--verify", "--quiet", trimmedBase+"^{commit}"); err != nil {
			return Worktree{}, fmt.Errorf("%w: %q: %v", ErrInvalidBase, trimmedBase, err)
		}
		if _, err := runGit(ctx, root, "worktree", "add", "-b", trimmedBranch, path, trimmedBase); err != nil {
			return Worktree{}, fmt.Errorf("workspace: create worktree %q: %w", trimmedBranch, err)
		}
	}

	after, err := ListWorktrees(ctx, root)
	if err != nil {
		return Worktree{}, err
	}
	for _, wt := range after {
		if samePath(wt.Path, path) {
			return wt, nil
		}
	}
	return Worktree{Path: path, Branch: trimmedBranch}, nil
}

// RemoveWorktree 删除 repo 中已登记的工作树目录及其 git 登记信息，保留分支本身。
// 主工作树不可删除，返回 ErrMainWorktree。
func RemoveWorktree(ctx context.Context, repo, path string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("workspace: remove worktree: %w", err)
	}
	if strings.TrimSpace(path) == "" {
		return ErrInvalidWorktreePath
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrInvalidWorktreePath, path, err)
	}

	root, err := mainRepoRoot(ctx, repo)
	if err != nil {
		return err
	}
	existing, err := ListWorktrees(ctx, root)
	if err != nil {
		return err
	}
	found := false
	for _, wt := range existing {
		if !samePath(wt.Path, target) {
			continue
		}
		found = true
		if wt.Main {
			return fmt.Errorf("%w: %s", ErrMainWorktree, wt.Path)
		}
	}
	if !found {
		return fmt.Errorf("%w: %s", ErrWorktreeNotFound, target)
	}
	if _, err := runGit(ctx, root, "worktree", "remove", "--force", target); err != nil {
		return fmt.Errorf("workspace: remove worktree %s: %w", target, err)
	}
	return nil
}

// repoDir 校验 repo 是一个已存在的目录。
func repoDir(repo string) (string, error) {
	trimmed := strings.TrimSpace(repo)
	if trimmed == "" {
		return "", fmt.Errorf("%w: empty path", ErrInvalidRepo)
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %v", ErrInvalidRepo, trimmed, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %v", ErrInvalidRepo, trimmed, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: %s is not a directory", ErrInvalidRepo, trimmed)
	}
	return abs, nil
}

// mainRepoRoot 返回主仓库根目录；从链接工作树内调用也回到主工作树。
func mainRepoRoot(ctx context.Context, repo string) (string, error) {
	dir, err := repoDir(repo)
	if err != nil {
		return "", err
	}
	top, err := runGit(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("%w: %s: %v", ErrInvalidRepo, repo, err)
	}
	root := strings.TrimSpace(top)
	if root == "" {
		return "", fmt.Errorf("%w: %s", ErrInvalidRepo, repo)
	}
	common, err := runGit(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err == nil {
		if c := strings.TrimSpace(common); filepath.Base(c) == ".git" {
			return filepath.Dir(c), nil
		}
	}
	if real, err := filepath.EvalSymlinks(root); err == nil {
		return real, nil
	}
	return root, nil
}

// worktreePath 返回分支对应的工作树路径，并确认结果未越出工作树根目录。
func worktreePath(root, branch string) (string, error) {
	base := filepath.Join(root, worktreeDirName)
	path := filepath.Join(base, filepath.FromSlash(branch))
	rel, err := filepath.Rel(base, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: %q", ErrInvalidBranch, branch)
	}
	return path, nil
}

// validateBranch 校验分支名：允许字母、数字、点、下划线、连字符与作为分隔的斜杠。
func validateBranch(branch string) error {
	if branch == "" || len(branch) > 200 {
		return fmt.Errorf("%w: %q", ErrInvalidBranch, branch)
	}
	if strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") ||
		strings.HasSuffix(branch, ".") || strings.Contains(branch, "..") ||
		strings.Contains(branch, "//") || strings.Contains(branch, "@{") {
		return fmt.Errorf("%w: %q", ErrInvalidBranch, branch)
	}
	for _, segment := range strings.Split(branch, "/") {
		if segment == "" || segment == "." || segment == ".." || strings.HasSuffix(segment, ".lock") {
			return fmt.Errorf("%w: %q", ErrInvalidBranch, branch)
		}
		for _, r := range segment {
			valid := r == '_' || r == '-' || r == '.' ||
				(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
			if !valid {
				return fmt.Errorf("%w: %q", ErrInvalidBranch, branch)
			}
		}
	}
	return nil
}

// localBranchExists 报告本地是否存在 refs/heads/<branch>。
func localBranchExists(ctx context.Context, dir, branch string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, fmt.Errorf("workspace: check branch %q: %w", branch, ctxErr)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("workspace: check branch %q: %w: %s", branch, err, strings.TrimSpace(stderr.String()))
}

// runGit 在 dir 下执行 git 命令并返回 stdout；失败时把 stderr 带进错误链。
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", ErrGitUnavailable
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), ctxErr)
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, message)
	}
	return stdout.String(), nil
}

// gitEnv 返回 git 子进程环境：禁止 credential 交互，避免命令在后台挂起。
func gitEnv() []string {
	return append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=")
}

// parseWorktreeList 解析 git worktree list --porcelain 输出。
func parseWorktreeList(out string) []Worktree {
	list := make([]Worktree, 0, 4)
	var current *Worktree
	flush := func() {
		if current != nil {
			list = append(list, *current)
			current = nil
		}
	}
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSuffix(raw, "\r")
		if line == "" {
			flush()
			continue
		}
		key, value, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			flush()
			current = &Worktree{Path: value, Main: len(list) == 0}
			continue
		}
		if current == nil {
			continue
		}
		switch key {
		case "HEAD":
			current.Head = value
		case "branch":
			current.Branch = strings.TrimPrefix(value, "refs/heads/")
		case "detached":
			current.Detached = true
		case "bare":
			current.Bare = true
		case "locked":
			current.Locked = true
		case "prunable":
			current.Prunable = true
		}
	}
	flush()
	return list
}

// samePath 比较两个路径是否指向同一位置，必要时解析符号链接。
func samePath(left, right string) bool {
	if filepath.Clean(left) == filepath.Clean(right) {
		return true
	}
	leftReal, leftErr := filepath.EvalSymlinks(left)
	rightReal, rightErr := filepath.EvalSymlinks(right)
	return leftErr == nil && rightErr == nil && leftReal == rightReal
}
