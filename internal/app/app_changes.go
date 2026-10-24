package app

import (
	"fmt"

	"github.com/Baike12/b-desktop/internal/workspace"
)

// FileChangeDTO 是单个文件的变更。Path 相对会话工作目录。
type FileChangeDTO struct {
	// Path 是变更文件的相对路径。
	Path string `json:"path"`
	// OldPath 是重命名或复制前的路径；没有时为空。
	OldPath string `json:"oldPath"`
	// Status 是变更类型：added / modified / deleted / renamed / copied /
	// untracked / conflicted / typechange。
	Status string `json:"status"`
	// Staged 为 true 表示变更位于暂存区。
	Staged bool `json:"staged"`
	// Additions 是新增行数。
	Additions int `json:"additions"`
	// Deletions 是删除行数。
	Deletions int `json:"deletions"`
	// Binary 为 true 表示文件是二进制。
	Binary bool `json:"binary"`
}

// ChangeSetDTO 是一次工作区变更快照。
type ChangeSetDTO struct {
	// Root 是工作目录的绝对路径。
	Root string `json:"root"`
	// Branch 是当前分支名；游离 HEAD 时为空。
	Branch string `json:"branch"`
	// IsRepo 为 false 表示工作目录不在 git 仓库内。
	IsRepo bool `json:"isRepo"`
	// Changes 是变更列表，暂存区变更在前。
	Changes []FileChangeDTO `json:"changes"`
}

// FileDiffDTO 是单个文件的 unified diff。
type FileDiffDTO struct {
	// Path 是相对工作目录的文件路径。
	Path string `json:"path"`
	// OldPath 是重命名或复制前的路径；没有时为空。
	OldPath string `json:"oldPath"`
	// Status 是变更类型。
	Status string `json:"status"`
	// Diff 是 unified diff 文本；无差异或二进制时为空。
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

// ListSessionChanges 返回会话工作目录相对 HEAD 的全部变更。
// 工作目录不在 git 仓库内时返回 IsRepo=false 的空变更集，不算错误。
func (a *App) ListSessionChanges(sessionID, workDir string) (ChangeSetDTO, error) {
	root, err := a.sessionFileRoot(sessionID, workDir)
	if err != nil {
		return ChangeSetDTO{}, err
	}
	set, err := workspace.Changes(a.lifeCtx(), root)
	if err != nil {
		return ChangeSetDTO{}, fmt.Errorf("app: list session changes: %w", err)
	}
	out := ChangeSetDTO{
		Root:    set.Root,
		Branch:  set.Branch,
		IsRepo:  set.IsRepo,
		Changes: make([]FileChangeDTO, 0, len(set.Changes)),
	}
	for _, change := range set.Changes {
		out.Changes = append(out.Changes, FileChangeDTO{
			Path:      change.Path,
			OldPath:   change.OldPath,
			Status:    string(change.Status),
			Staged:    change.Staged,
			Additions: change.Additions,
			Deletions: change.Deletions,
			Binary:    change.Binary,
		})
	}
	return out, nil
}

// GetSessionFileDiff 返回会话工作目录下单个文件的 unified diff。
// staged 为 true 时取暂存区差异，否则取工作区差异。
func (a *App) GetSessionFileDiff(sessionID, workDir, path string, staged bool) (FileDiffDTO, error) {
	root, err := a.sessionFileRoot(sessionID, workDir)
	if err != nil {
		return FileDiffDTO{}, err
	}
	diff, err := workspace.FileDiffOf(a.lifeCtx(), root, path, staged)
	if err != nil {
		return FileDiffDTO{}, fmt.Errorf("app: get session file diff %q: %w", path, err)
	}
	return FileDiffDTO{
		Path:      diff.Path,
		OldPath:   diff.OldPath,
		Status:    string(diff.Status),
		Diff:      diff.Diff,
		Additions: diff.Additions,
		Deletions: diff.Deletions,
		Binary:    diff.Binary,
		Staged:    diff.Staged,
	}, nil
}

// StageSessionFiles 把工作目录相对路径对应的变更加入暂存区。
func (a *App) StageSessionFiles(sessionID, workDir string, paths []string) error {
	root, err := a.sessionFileRoot(sessionID, workDir)
	if err != nil {
		return err
	}
	if err := workspace.StagePaths(a.lifeCtx(), root, paths); err != nil {
		return fmt.Errorf("app: stage session files: %w", err)
	}
	return nil
}

// UnstageSessionFiles 把工作目录相对路径对应的变更移出暂存区，保留工作区内容。
func (a *App) UnstageSessionFiles(sessionID, workDir string, paths []string) error {
	root, err := a.sessionFileRoot(sessionID, workDir)
	if err != nil {
		return err
	}
	if err := workspace.UnstagePaths(a.lifeCtx(), root, paths); err != nil {
		return fmt.Errorf("app: unstage session files: %w", err)
	}
	return nil
}

// DiscardSessionFile 放弃工作目录相对路径对应的改动。该操作不可撤销。
// staged 为 true 时放弃暂存区与工作区的全部改动（文件回到 HEAD 内容），
// 为 false 时只放弃工作区的改动（文件回到暂存区内容）。
// oldPath 是重命名前的路径；非空表示这是一个重命名，会把源路径一并还原。
func (a *App) DiscardSessionFile(sessionID, workDir, path, oldPath string, staged bool) error {
	root, err := a.sessionFileRoot(sessionID, workDir)
	if err != nil {
		return err
	}
	if err := workspace.DiscardPath(a.lifeCtx(), root, path, oldPath, staged); err != nil {
		return fmt.Errorf("app: discard session file %q: %w", path, err)
	}
	return nil
}
