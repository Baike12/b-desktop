package app

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Baike12/b-desktop/internal/project"
	"github.com/Baike12/b-desktop/internal/workspace"
)

// 全局搜索与 Worktree 绑定层错误哨兵。
var (
	// ErrEmptyQuery 表示全局搜索的查询串为空。
	ErrEmptyQuery = errors.New("app: empty search query")
	// ErrEmptyProject 表示未提供项目路径。
	ErrEmptyProject = errors.New("app: empty project path")
	// ErrEmptyBranch 表示未提供分支名。
	ErrEmptyBranch = errors.New("app: empty branch name")
	// ErrEmptyWorktreePath 表示未提供工作树路径。
	ErrEmptyWorktreePath = errors.New("app: empty worktree path")
)

// SessionSearchHitDTO 是全局搜索的一条命中。
type SessionSearchHitDTO struct {
	Role     string    `json:"role"`
	ToolName string    `json:"toolName,omitempty"`
	Line     int       `json:"line"`
	Snippet  string    `json:"snippet"`
	Time     time.Time `json:"time"`
}

// SessionSearchResultDTO 是一个会话的全局搜索结果。
type SessionSearchResultDTO struct {
	SessionID  string                `json:"sessionId"`
	Title      string                `json:"title"`
	WorkDir    string                `json:"workDir"`
	UpdatedAt  time.Time             `json:"updatedAt"`
	MatchCount int                   `json:"matchCount"`
	Matches    []SessionSearchHitDTO `json:"matches"`
}

// WorktreeDTO 是一个 git 工作树。
type WorktreeDTO struct {
	Path     string `json:"path"`
	Branch   string `json:"branch"`
	Head     string `json:"head"`
	Detached bool   `json:"detached"`
	Bare     bool   `json:"bare"`
	Locked   bool   `json:"locked"`
	Prunable bool   `json:"prunable"`
	Main     bool   `json:"main"`
	// Managed 表示该工作树位于本应用为当前项目管理的 worktrees 目录下。
	Managed bool `json:"managed"`
}

// ProjectWorktreesDTO 是一个项目的工作树视图。
// 工作树按仓库维度返回，ProjectPath 用于把工作树会话归回所属项目。
type ProjectWorktreesDTO struct {
	ProjectPath   string        `json:"projectPath"`
	RepoRoot      string        `json:"repoRoot"`
	WorktreesRoot string        `json:"worktreesRoot"`
	Worktrees     []WorktreeDTO `json:"worktrees"`
}

// SearchSessions 在全部会话存档中搜索 query，匹配用户/assistant 文本与工具名。
// limit <= 0 时使用默认值 50，上限 200；结果按最近更新时间倒序。
func (a *App) SearchSessions(query string, limit int) ([]SessionSearchResultDTO, error) {
	if strings.TrimSpace(query) == "" {
		return nil, ErrEmptyQuery
	}
	if a.isClosed() {
		return nil, ErrClosed
	}
	cfg, _ := a.configSnapshot()
	results, err := workspace.SearchSessions(a.lifeCtx(), cfg.DataDir, query, limit)
	if err != nil {
		return nil, fmt.Errorf("app: search sessions: %w", err)
	}
	out := make([]SessionSearchResultDTO, 0, len(results))
	for _, result := range results {
		out = append(out, toSessionSearchResultDTO(result))
	}
	return out, nil
}

// ListProjectWorktrees 返回项目所属仓库的全部工作树，主工作树排在首位。
// 项目路径可以是仓库内任意目录；工作树列表与托管目录都以主仓库为基准。
func (a *App) ListProjectWorktrees(projectPath string) (ProjectWorktreesDTO, error) {
	trimmed := strings.TrimSpace(projectPath)
	if trimmed == "" {
		return ProjectWorktreesDTO{}, ErrEmptyProject
	}
	if a.isClosed() {
		return ProjectWorktreesDTO{}, ErrClosed
	}
	ctx := a.lifeCtx()
	repoRoot, err := workspace.RepoRoot(ctx, trimmed)
	if err != nil {
		return ProjectWorktreesDTO{}, fmt.Errorf("app: list project worktrees: %w", err)
	}
	items, err := workspace.ListWorktrees(ctx, repoRoot)
	if err != nil {
		return ProjectWorktreesDTO{}, fmt.Errorf("app: list project worktrees: %w", err)
	}
	out := make([]WorktreeDTO, 0, len(items))
	for _, item := range items {
		out = append(out, toWorktreeDTO(item))
	}
	return ProjectWorktreesDTO{
		ProjectPath:   trimmed,
		RepoRoot:      repoRoot,
		WorktreesRoot: workspace.ManagedWorktreesRoot(repoRoot),
		Worktrees:     out,
	}, nil
}

// CreateProjectWorktree 为项目创建或挂载工作树，落在仓库同级的
// worktrees/<项目名>/<分支> 下。分支已存在时直接挂载；base 为空时以 HEAD 为基线。
func (a *App) CreateProjectWorktree(projectPath, branch, base string) (WorktreeDTO, error) {
	if strings.TrimSpace(projectPath) == "" {
		return WorktreeDTO{}, ErrEmptyProject
	}
	if strings.TrimSpace(branch) == "" {
		return WorktreeDTO{}, ErrEmptyBranch
	}
	if a.isClosed() {
		return WorktreeDTO{}, ErrClosed
	}
	wt, err := workspace.CreateWorktree(a.lifeCtx(), projectPath, branch, base)
	if err != nil {
		return WorktreeDTO{}, fmt.Errorf("app: create project worktree: %w", err)
	}
	return toWorktreeDTO(wt), nil
}

// RemoveProjectWorktree 删除项目仓库中已登记的工作树目录，保留分支本身；主工作树不可删除。
func (a *App) RemoveProjectWorktree(projectPath, path string) error {
	if strings.TrimSpace(projectPath) == "" {
		return ErrEmptyProject
	}
	if strings.TrimSpace(path) == "" {
		return ErrEmptyWorktreePath
	}
	if a.isClosed() {
		return ErrClosed
	}
	if err := workspace.RemoveWorktree(a.lifeCtx(), projectPath, path); err != nil {
		return fmt.Errorf("app: remove project worktree: %w", err)
	}
	return nil
}

// projectPaths 返回已打开的项目列表；调用方据此把会话归回项目。
func (a *App) projectPaths() ([]project.Project, error) {
	store, err := a.projectStore()
	if err != nil {
		return nil, err
	}
	return store.List(a.lifeCtx())
}

// projectPathForWorkDir 返回 workDir 归属的项目路径。
//
// 先按项目路径精确匹配；再按托管工作树目录
// <项目父目录>/worktrees/<项目名> 判断。项目路径约定为主仓库根目录。
func projectPathForWorkDir(workDir string, projects []project.Project) string {
	clean := filepath.Clean(workDir)
	for _, item := range projects {
		if filepath.Clean(item.Path) == clean {
			return item.Path
		}
	}
	for _, item := range projects {
		root := workspace.ManagedWorktreesRoot(item.Path)
		if pathWithin(clean, root) {
			return item.Path
		}
	}
	return ""
}

// pathWithin 报告 path 是否位于 root 之内（不含 root 本身）。
func pathWithin(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// toSessionSearchResultDTO 把搜索结果转换成前端 DTO。
func toSessionSearchResultDTO(result workspace.SessionSearchResult) SessionSearchResultDTO {
	matches := make([]SessionSearchHitDTO, 0, len(result.Matches))
	for _, hit := range result.Matches {
		matches = append(matches, SessionSearchHitDTO{
			Role:     hit.Role,
			ToolName: hit.ToolName,
			Line:     hit.Line,
			Snippet:  hit.Snippet,
			Time:     hit.Time,
		})
	}
	return SessionSearchResultDTO{
		SessionID:  result.SessionID,
		Title:      result.Title,
		WorkDir:    result.WorkDir,
		UpdatedAt:  result.UpdatedAt,
		MatchCount: result.MatchCount,
		Matches:    matches,
	}
}

// toWorktreeDTO 把工作树转换成前端 DTO。
func toWorktreeDTO(wt workspace.Worktree) WorktreeDTO {
	return WorktreeDTO{
		Path:     wt.Path,
		Branch:   wt.Branch,
		Head:     wt.Head,
		Detached: wt.Detached,
		Bare:     wt.Bare,
		Locked:   wt.Locked,
		Prunable: wt.Prunable,
		Main:     wt.Main,
		Managed:  wt.Managed,
	}
}
