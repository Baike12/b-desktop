package app

import (
	"fmt"
	"os"

	"github.com/Baike12/b-desktop/internal/session"
)

// ForkSession 把一个现有会话复制为新的独立线程：新会话拥有与源会话一致的
// 完整历史（含压缩边界、用量与权限模式记录），标题追加 " (fork)" 后缀，
// 此后两条线程各自独立追加。新会话 ID 自动生成；WorkDir 与 Model 沿用源值。
// 源会话正在运行时同样允许分叉：分叉到的是发起时刻的 transcript 快照，
// 运行中的源会话不受影响。
func (a *App) ForkSession(sessionID string) (SessionInfoDTO, error) {
	store, cfg, err := a.sessionStore()
	if err != nil {
		return SessionInfoDTO{}, err
	}
	if a.isClosed() {
		return SessionInfoDTO{}, ErrClosed
	}
	if err := session.ValidateID(sessionID); err != nil {
		return SessionInfoDTO{}, fmt.Errorf("app: fork session: %w", err)
	}

	info, err := store.ForkSession(a.lifeCtx(), sessionID, session.Meta{ID: newID("s")})
	if err != nil {
		return SessionInfoDTO{}, fmt.Errorf("app: fork session: %w", err)
	}

	// 与 CreateSession 一致：登记内存运行时，首次 SendMessage 按需恢复引擎。
	meta := session.Meta{
		ID:        info.ID,
		Title:     info.Title,
		WorkDir:   info.WorkDir,
		Model:     cfg.Model,
		CreatedAt: info.UpdatedAt,
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return SessionInfoDTO{}, ErrClosed
	}
	a.sessions[info.ID] = newSessionRuntime(meta, nil)
	a.mu.Unlock()

	dto := toSessionInfoDTO(info, false)
	if projects, perr := a.projectPaths(); perr == nil {
		dto.ProjectPath = projectPathForWorkDir(info.WorkDir, projects)
	} else {
		fmt.Fprintf(os.Stderr, "app: resolve project for session %s: %v\n", info.ID, perr)
	}
	return dto, nil
}
