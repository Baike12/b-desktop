package app

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Baike12/b-desktop/internal/session"
	"github.com/Baike12/b-desktop/internal/workspace"
)

// ErrWorkDirUnavailable 表示会话没有可用的工作目录，无法浏览文件。
var ErrWorkDirUnavailable = errors.New("app: work directory unavailable")

// FileEntryDTO 是文件树的一个节点。Path 是相对工作目录的斜杠路径。
type FileEntryDTO struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	IsDir     bool   `json:"isDir"`
	IsSymlink bool   `json:"isSymlink"`
	Size      int64  `json:"size"`
}

// FileListDTO 是一次目录读取的结果；Truncated 表示条目超过上限被截断。
type FileListDTO struct {
	Entries   []FileEntryDTO `json:"entries"`
	Truncated bool           `json:"truncated"`
}

// FileContentDTO 是只读文件预览的结果。
// Binary 为 true 时 Content 为空；Truncated 表示超过预览上限被截断。
type FileContentDTO struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Content   string `json:"content"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
}

// ListSessionFiles 列出会话工作目录下 rel 的直接子项。rel 为空表示根目录。
// workDir 由前端从会话详情带入；若会话运行时已在内存中，以运行时的值为准。
func (a *App) ListSessionFiles(sessionID, workDir, rel string) (FileListDTO, error) {
	root, err := a.sessionFileRoot(sessionID, workDir)
	if err != nil {
		return FileListDTO{}, err
	}
	list, err := workspace.ListDir(a.lifeCtx(), root, rel)
	if err != nil {
		return FileListDTO{}, fmt.Errorf("app: list session files: %w", err)
	}
	entries := make([]FileEntryDTO, 0, len(list.Entries))
	for _, entry := range list.Entries {
		entries = append(entries, FileEntryDTO{
			Name:      entry.Name,
			Path:      entry.Path,
			IsDir:     entry.IsDir,
			IsSymlink: entry.IsSymlink,
			Size:      entry.Size,
		})
	}
	return FileListDTO{Entries: entries, Truncated: list.Truncated}, nil
}

// ReadSessionFile 读取会话工作目录下 rel 的只读预览。
func (a *App) ReadSessionFile(sessionID, workDir, rel string) (FileContentDTO, error) {
	root, err := a.sessionFileRoot(sessionID, workDir)
	if err != nil {
		return FileContentDTO{}, err
	}
	content, err := workspace.ReadFile(a.lifeCtx(), root, rel)
	if err != nil {
		return FileContentDTO{}, fmt.Errorf("app: read session file: %w", err)
	}
	return FileContentDTO{
		Path:      content.Path,
		Size:      content.Size,
		Content:   content.Content,
		Binary:    content.Binary,
		Truncated: content.Truncated,
	}, nil
}

// CreateSessionFile 在会话工作目录下新建文件或目录。
// isDir 为 true 时创建目录；目标已存在时返回错误，不会覆盖既有内容。
func (a *App) CreateSessionFile(sessionID, workDir, rel string, isDir bool) error {
	root, err := a.sessionFileRoot(sessionID, workDir)
	if err != nil {
		return err
	}
	if err := workspace.CreateEntry(a.lifeCtx(), root, rel, isDir); err != nil {
		return fmt.Errorf("app: create session file %q: %w", rel, err)
	}
	return nil
}

// RenameSessionFile 把会话工作目录下的 from 改名为 to。
// to 必须是工作目录内的相对路径，且不能指向已存在的路径。
func (a *App) RenameSessionFile(sessionID, workDir, from, to string) error {
	root, err := a.sessionFileRoot(sessionID, workDir)
	if err != nil {
		return err
	}
	if err := workspace.RenameEntry(a.lifeCtx(), root, from, to); err != nil {
		return fmt.Errorf("app: rename session file %q: %w", from, err)
	}
	return nil
}

// MoveSessionFile 把会话工作目录下的 from 移动到 toDir 目录，返回移动后的相对路径。
// toDir 为空表示移动到工作目录根。
func (a *App) MoveSessionFile(sessionID, workDir, from, toDir string) (string, error) {
	root, err := a.sessionFileRoot(sessionID, workDir)
	if err != nil {
		return "", err
	}
	moved, err := workspace.MoveEntry(a.lifeCtx(), root, from, toDir)
	if err != nil {
		return "", fmt.Errorf("app: move session file %q: %w", from, err)
	}
	return moved, nil
}

// DeleteSessionFile 删除会话工作目录下的文件或目录。目录会连同内容一起删除。
func (a *App) DeleteSessionFile(sessionID, workDir, rel string) error {
	root, err := a.sessionFileRoot(sessionID, workDir)
	if err != nil {
		return err
	}
	if err := workspace.DeleteEntry(a.lifeCtx(), root, rel); err != nil {
		return fmt.Errorf("app: delete session file %q: %w", rel, err)
	}
	return nil
}

// WriteSessionFile 覆盖写入会话工作目录下的文件内容。
func (a *App) WriteSessionFile(sessionID, workDir, rel, content string) error {
	root, err := a.sessionFileRoot(sessionID, workDir)
	if err != nil {
		return err
	}
	if err := workspace.WriteFile(a.lifeCtx(), root, rel, content); err != nil {
		return fmt.Errorf("app: write session file %q: %w", rel, err)
	}
	return nil
}

// SearchSessionFiles 按文件名子串搜索会话工作目录下的文件，
// 返回相对工作目录的斜杠路径列表。query 为空时返回空列表。
func (a *App) SearchSessionFiles(sessionID, workDir, query string) ([]string, error) {
	root, err := a.sessionFileRoot(sessionID, workDir)
	if err != nil {
		return nil, err
	}
	paths, err := workspace.SearchFiles(a.lifeCtx(), root, query, 0)
	if err != nil {
		return nil, fmt.Errorf("app: search session files: %w", err)
	}
	return paths, nil
}

// sessionFileRoot 解析会话的文件浏览根目录。
func (a *App) sessionFileRoot(sessionID, workDir string) (string, error) {
	if a.isClosed() {
		return "", ErrClosed
	}
	if err := session.ValidateID(sessionID); err != nil {
		return "", fmt.Errorf("app: session files: %w", err)
	}
	root := strings.TrimSpace(workDir)
	// 会话运行时持有创建时的 workDir；它比前端缓存更可靠。
	if rt, err := a.runtimeOf(sessionID); err == nil {
		if current := rt.workDirSnapshot(); current != "" {
			root = current
		}
	}
	if root == "" {
		return "", ErrWorkDirUnavailable
	}
	return root, nil
}

// workDirSnapshot 返回会话的工作目录。workDir 创建后不再变化，读取时仍加锁。
func (rt *sessionRuntime) workDirSnapshot() string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.workDir
}
