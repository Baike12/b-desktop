package app

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Baike12/b-desktop/internal/workspace"
)

// 全局搜索与 Worktree 绑定层错误哨兵。
var (
	// ErrEmptyQuery 表示全局搜索的查询串为空。
	ErrEmptyQuery = errors.New("app: empty search query")
	// ErrEmptyRepo 表示未提供仓库路径。
	ErrEmptyRepo = errors.New("app: empty repository path")
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

// ListWorktrees 返回 repo 所属仓库的全部工作树，主工作树排在首位。
func (a *App) ListWorktrees(repo string) ([]WorktreeDTO, error) {
	if strings.TrimSpace(repo) == "" {
		return nil, ErrEmptyRepo
	}
	if a.isClosed() {
		return nil, ErrClosed
	}
	items, err := workspace.ListWorktrees(a.lifeCtx(), repo)
	if err != nil {
		return nil, fmt.Errorf("app: list worktrees: %w", err)
	}
	out := make([]WorktreeDTO, 0, len(items))
	for _, item := range items {
		out = append(out, toWorktreeDTO(item))
	}
	return out, nil
}

// CreateWorktree 在 <repo>/.b-desktop/worktrees/<branch> 创建或挂载工作树。
// 分支已存在时直接挂载；base 为空时以 HEAD 为基线创建新分支。
func (a *App) CreateWorktree(repo, branch, base string) (WorktreeDTO, error) {
	if strings.TrimSpace(repo) == "" {
		return WorktreeDTO{}, ErrEmptyRepo
	}
	if strings.TrimSpace(branch) == "" {
		return WorktreeDTO{}, ErrEmptyBranch
	}
	if a.isClosed() {
		return WorktreeDTO{}, ErrClosed
	}
	wt, err := workspace.CreateWorktree(a.lifeCtx(), repo, branch, base)
	if err != nil {
		return WorktreeDTO{}, fmt.Errorf("app: create worktree: %w", err)
	}
	return toWorktreeDTO(wt), nil
}

// RemoveWorktree 删除 repo 中已登记的工作树目录，保留分支本身；主工作树不可删除。
func (a *App) RemoveWorktree(repo, path string) error {
	if strings.TrimSpace(repo) == "" {
		return ErrEmptyRepo
	}
	if strings.TrimSpace(path) == "" {
		return ErrEmptyWorktreePath
	}
	if a.isClosed() {
		return ErrClosed
	}
	if err := workspace.RemoveWorktree(a.lifeCtx(), repo, path); err != nil {
		return fmt.Errorf("app: remove worktree: %w", err)
	}
	return nil
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
	}
}
