package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// Git 变更视图：状态列表、单文件差异、暂存与放弃。
//
// 所有 git 命令都在工作目录内执行，但 git 输出的路径始终相对仓库根；
// 本包负责把仓库相对路径换算成工作目录相对路径，前端只认工作目录相对路径。
//
// 不是 git 仓库时不算错误：Changes 返回 IsRepo=false 的空变更集。

// ChangeStatus 是单个文件的变更类型。
type ChangeStatus string

// 变更类型常量，取值与前端展示一一对应。
const (
	// ChangeAdded 表示新增文件。
	ChangeAdded ChangeStatus = "added"
	// ChangeModified 表示内容被修改。
	ChangeModified ChangeStatus = "modified"
	// ChangeDeleted 表示文件被删除。
	ChangeDeleted ChangeStatus = "deleted"
	// ChangeRenamed 表示文件被重命名。
	ChangeRenamed ChangeStatus = "renamed"
	// ChangeCopied 表示文件被复制。
	ChangeCopied ChangeStatus = "copied"
	// ChangeUntracked 表示未被 git 跟踪的新文件。
	ChangeUntracked ChangeStatus = "untracked"
	// ChangeConflicted 表示存在合并冲突。
	ChangeConflicted ChangeStatus = "conflicted"
	// ChangeTypeChange 表示文件类型发生变化（文件与目录互换等）。
	ChangeTypeChange ChangeStatus = "typechange"
)

// FileChange 是单个文件在暂存区或工作区中的一处变更。
type FileChange struct {
	// Path 是相对工作目录的斜杠路径。
	Path string `json:"path"`
	// OldPath 是重命名或复制前的路径；没有时为空。
	OldPath string `json:"oldPath"`
	// Status 是变更类型。
	Status ChangeStatus `json:"status"`
	// Staged 为 true 表示变更位于暂存区，为 false 表示位于工作区。
	Staged bool `json:"staged"`
	// Additions 是新增行数。
	Additions int `json:"additions"`
	// Deletions 是删除行数。
	Deletions int `json:"deletions"`
	// Binary 为 true 表示文件是二进制，没有行级差异。
	Binary bool `json:"binary"`
}

// ChangeSet 是一次工作区变更快照。
type ChangeSet struct {
	// Root 是工作目录的绝对路径。
	Root string `json:"root"`
	// Branch 是当前分支名；游离 HEAD 时为空。
	Branch string `json:"branch"`
	// Changes 是变更列表，暂存区变更在前。
	Changes []FileChange `json:"changes"`
	// IsRepo 为 false 表示工作目录不在 git 仓库内。
	IsRepo bool `json:"isRepo"`
}

// FileDiff 是单个文件的 unified diff。
type FileDiff struct {
	// Path 是相对工作目录的斜杠路径。
	Path string `json:"path"`
	// OldPath 是重命名或复制前的路径；没有时为空。
	OldPath string `json:"oldPath"`
	// Status 是变更类型。
	Status ChangeStatus `json:"status"`
	// Diff 是 unified diff 文本；二进制或无差异时为空。
	Diff string `json:"diff"`
	// Additions 是新增行数。
	Additions int `json:"additions"`
	// Deletions 是删除行数。
	Deletions int `json:"deletions"`
	// Binary 为 true 表示文件是二进制。
	Binary bool `json:"binary"`
	// Staged 为 true 表示这是暂存区的差异。
	Staged bool `json:"staged"`
}

// git 变更操作的错误哨兵。
var (
	// ErrNotRepo 表示工作目录不在 git 仓库内。
	ErrNotRepo = errors.New("workspace: not a git repository")
	// ErrPathRequired 表示缺少必须提供的路径。
	ErrPathRequired = errors.New("workspace: path is required")
)

// Changes 返回工作目录相对 HEAD 的全部变更。
//
// 暂存区（index 与 HEAD 的差异）与工作区（worktree 与 index 的差异）分开列出，
// 与编辑器侧栏「已暂存 / 未暂存」两段式视图一致。
// 工作目录不在仓库内时返回 IsRepo=false，不视为错误。
func Changes(ctx context.Context, root string) (ChangeSet, error) {
	if err := ctx.Err(); err != nil {
		return ChangeSet{}, fmt.Errorf("workspace: changes: %w", err)
	}
	rootReal, err := resolveRoot(root)
	if err != nil {
		return ChangeSet{}, err
	}
	set := ChangeSet{Root: rootReal, Changes: []FileChange{}}
	repoRoot, err := gitTopLevel(ctx, rootReal)
	if err != nil {
		if errors.Is(err, ErrNotRepo) {
			return set, nil
		}
		return ChangeSet{}, err
	}
	set.IsRepo = true
	if branch, err := runGit(ctx, repoRoot, gitArgs("rev-parse", "--abbrev-ref", "HEAD")...); err == nil {
		set.Branch = strings.TrimSpace(branch)
	}
	prefix, err := workDirPrefix(repoRoot, rootReal)
	if err != nil {
		return ChangeSet{}, err
	}

	out, err := runGit(ctx, repoRoot, gitArgs("status", "--porcelain=v1", "-z", "--untracked-files=all")...)
	if err != nil {
		return ChangeSet{}, fmt.Errorf("workspace: git status: %w", err)
	}
	entries := parsePorcelainV1(out)

	stagedStats, err := numstatMap(ctx, repoRoot, "--cached")
	if err != nil {
		return ChangeSet{}, err
	}
	workStats, err := numstatMap(ctx, repoRoot, "")
	if err != nil {
		return ChangeSet{}, err
	}

	changes := make([]FileChange, 0, len(entries)*2)
	for _, entry := range entries {
		path, ok := prefix.strip(entry.Path)
		if !ok {
			continue
		}
		oldPath := ""
		if entry.OldPath != "" {
			if stripped, ok := prefix.strip(entry.OldPath); ok {
				oldPath = stripped
			}
		}
		// 暂存区变更。
		if staged, ok := statusFor(entry.X); ok && staged != ChangeUntracked {
			stat := stagedStats[entry.Path]
			changes = append(changes, FileChange{
				Path:      path,
				OldPath:   oldPath,
				Status:    staged,
				Staged:    true,
				Additions: stat.additions,
				Deletions: stat.deletions,
				Binary:    stat.binary,
			})
		}
		// 工作区变更。
		if unstaged, ok := statusFor(entry.Y); ok {
			stat := workStats[entry.Path]
			change := FileChange{
				Path:      path,
				OldPath:   oldPath,
				Status:    unstaged,
				Staged:    false,
				Additions: stat.additions,
				Deletions: stat.deletions,
				Binary:    stat.binary,
			}
			if unstaged == ChangeUntracked {
				// 未跟踪文件不在 numstat 里，行数由文件内容估算。
				// 传工作目录相对路径：ReadFile 按工作目录解析，不能用仓库相对路径。
				change.Additions, change.Binary = countUntracked(ctx, rootReal, path)
			}
			changes = append(changes, change)
		}
	}
	set.Changes = changes
	return set, nil
}

// FileDiffOf 返回单个文件的 unified diff。staged 为 true 时取暂存区差异，
// 否则取工作区差异。未跟踪文件没有 git 差异，这里按「全部新增」合成一份。
func FileDiffOf(ctx context.Context, root, path string, staged bool) (FileDiff, error) {
	if err := ctx.Err(); err != nil {
		return FileDiff{}, fmt.Errorf("workspace: file diff: %w", err)
	}
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return FileDiff{}, ErrPathRequired
	}
	rootReal, err := resolveRoot(root)
	if err != nil {
		return FileDiff{}, err
	}
	repoRoot, err := gitTopLevel(ctx, rootReal)
	if err != nil {
		return FileDiff{}, err
	}
	prefix, err := workDirPrefix(repoRoot, rootReal)
	if err != nil {
		return FileDiff{}, err
	}
	repoPath, ok := prefix.join(trimmed)
	if !ok {
		return FileDiff{}, fmt.Errorf("%w: %s", ErrInvalidPath, path)
	}

	out, err := runGit(ctx, repoRoot, gitArgs(diffArgs(staged, repoPath)...)...)
	if err != nil {
		return FileDiff{}, fmt.Errorf("workspace: git diff: %w", err)
	}
	if strings.TrimSpace(out) == "" {
		// 未跟踪文件没有 diff：按新增文件合成一份全量差异。
		synth, err := synthesizeNewFileDiff(ctx, rootReal, trimmed, repoPath)
		if err != nil {
			return FileDiff{}, err
		}
		if synth != nil {
			synth.Staged = staged
			return *synth, nil
		}
	}
	result := FileDiff{
		Path:   trimmed,
		Diff:   out,
		Staged: staged,
	}
	add, del, binary := diffLineStats(out)
	result.Additions = add
	result.Deletions = del
	result.Binary = binary
	return result, nil
}

// StagePaths 把 paths 相对工作目录的变更加入暂存区。
func StagePaths(ctx context.Context, root string, paths []string) error {
	return gitStage(ctx, root, paths, true)
}

// UnstagePaths 把 paths 相对工作目录的变更移出暂存区，保留工作区内容。
func UnstagePaths(ctx context.Context, root string, paths []string) error {
	return gitStage(ctx, root, paths, false)
}

// gitStage 执行暂存或取消暂存。
func gitStage(ctx context.Context, root string, paths []string, stage bool) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("workspace: stage paths: %w", err)
	}
	if len(paths) == 0 {
		return ErrPathRequired
	}
	rootReal, err := resolveRoot(root)
	if err != nil {
		return err
	}
	repoRoot, err := gitTopLevel(ctx, rootReal)
	if err != nil {
		return err
	}
	prefix, err := workDirPrefix(repoRoot, rootReal)
	if err != nil {
		return err
	}
	command := "add"
	if !stage {
		command = "restore"
	}
	args := []string{command, "-A", "--"}
	if !stage {
		args = []string{command, "--staged", "--"}
	}
	added := 0
	for _, item := range paths {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		repoPath, ok := prefix.join(trimmed)
		if !ok {
			return fmt.Errorf("%w: %s", ErrInvalidPath, item)
		}
		args = append(args, repoPath)
		added++
	}
	// 一个路径都没解析出来时必须直接报错：
	// `git add -A --` 不带 pathspec 会把整个仓库都暂存，`git reset HEAD --` 同理。
	if added == 0 {
		return ErrPathRequired
	}
	if _, err := runGit(ctx, repoRoot, gitArgs(args...)...); err != nil {
		// 旧版 git 没有 restore 时退回 reset。
		if !stage {
			fallback := append([]string{"reset", "-q", "HEAD", "--"}, args[2:]...)
			if _, resetErr := runGit(ctx, repoRoot, gitArgs(fallback...)...); resetErr == nil {
				return nil
			}
		}
		return fmt.Errorf("workspace: git stage: %w", err)
	}
	return nil
}

// DiscardPath 放弃 path 相对工作目录的改动。该操作不可撤销，调用方需要用户确认。
//
// staged 决定放弃的粒度：
//   - true 放弃暂存区与工作区的全部改动，文件回到 HEAD 的内容；
//   - false 只放弃工作区的改动，文件回到暂存区（index）里的内容。
//
// oldPath 是重命名前的路径（非空表示这是一个重命名）：
// 放弃重命名要把源路径还原出来，同时把新路径从索引与磁盘上清掉。
func DiscardPath(ctx context.Context, root, path, oldPath string, staged bool) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("workspace: discard path: %w", err)
	}
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ErrPathRequired
	}
	rootReal, err := resolveRoot(root)
	if err != nil {
		return err
	}
	repoRoot, err := gitTopLevel(ctx, rootReal)
	if err != nil {
		return err
	}
	prefix, err := workDirPrefix(repoRoot, rootReal)
	if err != nil {
		return err
	}
	repoPath, ok := prefix.join(trimmed)
	if !ok {
		return fmt.Errorf("%w: %s", ErrInvalidPath, path)
	}

	// 重命名：先把源路径还原，再把新路径清掉。
	if strings.TrimSpace(oldPath) != "" {
		repoOld, ok := prefix.join(strings.TrimSpace(oldPath))
		if !ok {
			return fmt.Errorf("%w: %s", ErrInvalidPath, oldPath)
		}
		if err := restorePath(ctx, repoRoot, repoOld, staged); err != nil {
			return err
		}
		if inIndex(ctx, repoRoot, repoPath) {
			if _, rmErr := runGit(ctx, repoRoot, gitArgs("rm", "--cached", "--force", "-q", "--", repoPath)...); rmErr != nil {
				return fmt.Errorf("workspace: discard rename %s: %w", path, rmErr)
			}
		}
		if err := DeleteEntry(ctx, rootReal, trimmed); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("workspace: remove discarded file %s: %w", path, err)
		}
		return nil
	}

	inHead := inHEAD(ctx, repoRoot, repoPath)
	switch {
	case inHead:
		// HEAD 里有这个文件：按粒度恢复到 HEAD 或暂存区。
		return restorePath(ctx, repoRoot, repoPath, staged)
	case inIndex(ctx, repoRoot, repoPath):
		// 暂存区里的新增文件：HEAD 里没有。
		if staged {
			// 连暂存区一起放弃：移出索引并从磁盘删除。
			if _, rmErr := runGit(ctx, repoRoot, gitArgs("rm", "--cached", "--force", "-q", "--", repoPath)...); rmErr != nil {
				return fmt.Errorf("workspace: discard new file %s: %w", path, rmErr)
			}
			if err := DeleteEntry(ctx, rootReal, trimmed); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("workspace: remove discarded file %s: %w", path, err)
			}
			return nil
		}
		// 只放弃工作区的改动：磁盘内容回到暂存区里的版本，不能删文件。
		return restorePath(ctx, repoRoot, repoPath, staged)
	default:
		// 完全未跟踪的文件：没有别的版本可回，只能从磁盘删除。
		if err := DeleteEntry(ctx, rootReal, trimmed); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("workspace: remove discarded file %s: %w", path, err)
		}
		return nil
	}
}

// restorePath 把 repoPath 恢复到 HEAD（staged 为 true）或暂存区（staged 为 false）。
func restorePath(ctx context.Context, repoRoot, repoPath string, staged bool) error {
	if staged {
		if _, err := runGit(ctx, repoRoot, gitArgs("restore", "--source=HEAD", "--staged", "--worktree", "--", repoPath)...); err != nil {
			if _, checkoutErr := runGit(ctx, repoRoot, gitArgs("checkout", "HEAD", "--", repoPath)...); checkoutErr != nil {
				return fmt.Errorf("workspace: restore %s: %w", repoPath, err)
			}
		}
		return nil
	}
	if _, err := runGit(ctx, repoRoot, gitArgs("restore", "--worktree", "--", repoPath)...); err != nil {
		if _, checkoutErr := runGit(ctx, repoRoot, gitArgs("checkout", "--", repoPath)...); checkoutErr != nil {
			return fmt.Errorf("workspace: restore %s: %w", repoPath, err)
		}
	}
	return nil
}

// inHEAD 报告仓库里是否存在该路径（HEAD 版本）。
func inHEAD(ctx context.Context, repoRoot, repoPath string) bool {
	_, err := runGit(ctx, repoRoot, gitArgs("cat-file", "-e", "HEAD:"+repoPath)...)
	return err == nil
}

// inIndex 报告路径是否在暂存区里。
func inIndex(ctx context.Context, repoRoot, repoPath string) bool {
	_, err := runGit(ctx, repoRoot, gitArgs("ls-files", "--error-unmatch", "--", repoPath)...)
	return err == nil
}

// porcelainEntry 是 git status --porcelain=v1 的一行。
type porcelainEntry struct {
	// X 是暂存区状态字符。
	X byte
	// Y 是工作区状态字符。
	Y byte
	// Path 是仓库相对路径。
	Path string
	// OldPath 是重命名或复制前的仓库相对路径。
	OldPath string
}

// numstatEntry 是单个文件的行数统计。
type numstatEntry struct {
	additions int
	deletions int
	binary    bool
}

// gitArgs 返回统一的 git 前缀参数：关闭颜色与外部 diff、路径不转义，
// 并且强制把 pathspec 当字面量处理。
//
// --literal-pathspecs 很关键：没有它，路径里的 "*"、":(exclude)"、":!x" 会被 git
// 当成通配与 magic，前端传进来的路径就能指向工作目录之外的文件。
func gitArgs(rest ...string) []string {
	return append([]string{
		"-c", "core.quotepath=false",
		"-c", "color.ui=false",
		"--literal-pathspecs",
	}, rest...)
}

// diffArgs 组装单文件 diff 的 git 参数。
func diffArgs(staged bool, repoPath string) []string {
	args := []string{"diff", "--no-color", "--no-ext-diff", "--find-renames", "-U3"}
	if staged {
		args = append(args, "--cached")
	}
	return append(args, "--", repoPath)
}

// gitTopLevel 返回 root 所属仓库的根目录；不在仓库内时返回 ErrNotRepo。
func gitTopLevel(ctx context.Context, root string) (string, error) {
	out, err := runGit(ctx, root, gitArgs("rev-parse", "--show-toplevel")...)
	if err != nil {
		if errors.Is(err, ErrGitUnavailable) {
			return "", err
		}
		return "", fmt.Errorf("%w: %s", ErrNotRepo, root)
	}
	top := strings.TrimSpace(out)
	if top == "" {
		return "", fmt.Errorf("%w: %s", ErrNotRepo, root)
	}
	return top, nil
}

// pathPrefix 表示工作目录在仓库根下的相对位置。
type pathPrefix struct {
	// prefix 是工作目录相对仓库根的斜杠路径；工作目录就是仓库根时为空。
	prefix string
}

// workDirPrefix 计算 root 相对 repoRoot 的位置。
func workDirPrefix(repoRoot, root string) (pathPrefix, error) {
	rel, err := filepath.Rel(repoRoot, root)
	if err != nil {
		return pathPrefix{}, fmt.Errorf("workspace: resolve work dir prefix: %w", err)
	}
	clean := filepath.ToSlash(rel)
	if clean == "." {
		return pathPrefix{}, nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return pathPrefix{}, fmt.Errorf("%w: work directory is outside the repository", ErrInvalidPath)
	}
	return pathPrefix{prefix: clean}, nil
}

// strip 把仓库相对路径换算成工作目录相对路径；路径在工作目录之外时返回 false。
func (p pathPrefix) strip(repoPath string) (string, bool) {
	clean := filepath.ToSlash(repoPath)
	if p.prefix == "" {
		return clean, true
	}
	if clean == p.prefix {
		return "", false
	}
	if !strings.HasPrefix(clean, p.prefix+"/") {
		return "", false
	}
	return strings.TrimPrefix(clean, p.prefix+"/"), true
}

// join 把工作目录相对路径换算成仓库相对路径；路径越界时返回 false。
//
// 除了词法检查，还要挡掉 git 的 pathspec magic：以 ":" 开头的路径会被 git 解释成
// 排除、通配等规则，能指向工作目录之外的文件。
func (p pathPrefix) join(rel string) (string, bool) {
	clean := filepath.ToSlash(strings.TrimSpace(rel))
	if clean == "" || clean == "." {
		return "", false
	}
	if strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, ":") {
		return "", false
	}
	clean = path.Clean(clean)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	if p.prefix == "" {
		return clean, true
	}
	return p.prefix + "/" + clean, true
}

// statusFor 把 porcelain 状态字符翻译成变更类型。空格表示无变更。
func statusFor(code byte) (ChangeStatus, bool) {
	switch code {
	case ' ':
		return "", false
	case '?':
		return ChangeUntracked, true
	case 'M':
		return ChangeModified, true
	case 'A':
		return ChangeAdded, true
	case 'D':
		return ChangeDeleted, true
	case 'R':
		return ChangeRenamed, true
	case 'C':
		return ChangeCopied, true
	case 'T':
		return ChangeTypeChange, true
	case 'U':
		return ChangeConflicted, true
	default:
		return ChangeModified, true
	}
}

// parsePorcelainV1 解析 git status --porcelain=v1 -z 输出。
// 重命名与复制的记录是「新路径\0旧路径\0」，旧路径紧跟在新路径之后。
func parsePorcelainV1(out string) []porcelainEntry {
	fields := strings.Split(out, "\x00")
	entries := make([]porcelainEntry, 0, len(fields))
	for i := 0; i < len(fields); i++ {
		raw := fields[i]
		if len(raw) < 4 {
			continue
		}
		entry := porcelainEntry{X: raw[0], Y: raw[1], Path: raw[3:]}
		if entry.X == 'R' || entry.X == 'C' || entry.Y == 'R' || entry.Y == 'C' {
			if i+1 < len(fields) {
				i++
				entry.OldPath = fields[i]
			}
		}
		entries = append(entries, entry)
	}
	return entries
}

// numstatMap 读取 git diff --numstat 的行数统计。cached 为 true 时取暂存区差异。
func numstatMap(ctx context.Context, repoRoot string, cached string) (map[string]numstatEntry, error) {
	args := []string{"diff", "--numstat", "-z"}
	if cached != "" {
		args = append(args, cached)
	}
	out, err := runGit(ctx, repoRoot, gitArgs(args...)...)
	if err != nil {
		return nil, fmt.Errorf("workspace: git diff --numstat: %w", err)
	}
	return parseNumstat(out), nil
}

// parseNumstat 解析 git diff --numstat -z 输出。
// 重命名记录的路径字段为空，紧随其后的是源路径与目标路径。
func parseNumstat(out string) map[string]numstatEntry {
	fields := strings.Split(out, "\x00")
	result := make(map[string]numstatEntry, len(fields))
	for i := 0; i < len(fields); i++ {
		raw := fields[i]
		if raw == "" {
			continue
		}
		parts := strings.SplitN(raw, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		entry := numstatEntry{}
		if parts[0] == "-" || parts[1] == "-" {
			entry.binary = true
		} else {
			entry.additions, _ = strconv.Atoi(parts[0])
			entry.deletions, _ = strconv.Atoi(parts[1])
		}
		path := parts[2]
		if path == "" {
			// 重命名：下一个字段是源路径，再下一个是目标路径。
			if i+2 >= len(fields) {
				continue
			}
			i += 2
			path = fields[i]
		}
		result[path] = entry
	}
	return result
}

// diffLineStats 统计 unified diff 的新增与删除行数，并识别二进制差异。
func diffLineStats(diff string) (int, int, bool) {
	if diff == "" {
		return 0, 0, false
	}
	if strings.Contains(diff, "Binary files ") || strings.Contains(diff, "GIT binary patch") {
		return 0, 0, true
	}
	additions, deletions := 0, 0
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			additions++
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			deletions++
		}
	}
	return additions, deletions, false
}

// countUntracked 统计未跟踪文件的行数；读取失败时返回 0。
func countUntracked(ctx context.Context, rootReal, repoPath string) (int, bool) {
	content, err := ReadFile(ctx, rootReal, repoPath)
	if err != nil {
		return 0, false
	}
	if content.Binary {
		return 0, true
	}
	lines := strings.Split(content.Content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return len(lines), false
}

// synthesizeNewFileDiff 为未跟踪文件合成一份「全部新增」的 unified diff。
// 文件不存在或不是普通文件时返回 nil。
func synthesizeNewFileDiff(ctx context.Context, rootReal, rel, repoPath string) (*FileDiff, error) {
	content, err := ReadFile(ctx, rootReal, rel)
	if err != nil {
		return nil, nil
	}
	if content.Binary {
		return &FileDiff{Path: rel, Binary: true}, nil
	}
	lines := strings.Split(content.Content, "\n")
	trailingNewline := len(lines) > 0 && lines[len(lines)-1] == ""
	if trailingNewline {
		lines = lines[:len(lines)-1]
	}
	var b strings.Builder
	b.WriteString("diff --git a/" + repoPath + " b/" + repoPath + "\n")
	b.WriteString("new file mode 100644\n")
	b.WriteString("--- /dev/null\n")
	b.WriteString("+++ b/" + repoPath + "\n")
	fmt.Fprintf(&b, "@@ -0,0 +1,%d @@\n", len(lines))
	for _, line := range lines {
		b.WriteString("+" + line + "\n")
	}
	if !trailingNewline && len(lines) > 0 {
		b.WriteString("\\ No newline at end of file\n")
	}
	return &FileDiff{
		Path:      rel,
		Status:    ChangeUntracked,
		Diff:      b.String(),
		Additions: len(lines),
	}, nil
}
