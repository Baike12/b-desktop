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
