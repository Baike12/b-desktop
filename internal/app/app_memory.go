package app

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/Baike12/b-desktop/internal/memory"
)

// ErrMemoryUnavailable 表示记忆组件无法初始化。
var ErrMemoryUnavailable = errors.New("app: memory unavailable")

// MemoryDTO 是一条记忆的元信息，不含正文。UpdatedAtMS 是 Unix 毫秒。
type MemoryDTO struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
	FileName    string `json:"fileName"`
	UpdatedAtMS int64  `json:"updatedAtMs"`
}

// memoryComponent 返回用户级记忆库，目录为 ~/.b-desktop/memory。
func (a *App) memoryComponent() (*memory.Store, error) {
	value, err := a.extensionStore("memory", func() (any, error) {
		dir, err := a.settingsDir()
		if err != nil {
			return nil, err
		}
		return memory.NewStore(filepath.Join(dir, "memory"))
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMemoryUnavailable, err)
	}
	store, ok := value.(*memory.Store)
	if !ok {
		return nil, fmt.Errorf("%w: unexpected component type %T", ErrMemoryUnavailable, value)
	}
	return store, nil
}

// ListMemories 返回全部记忆的元信息，按修改时间从新到旧排序。
func (a *App) ListMemories() ([]MemoryDTO, error) {
	store, err := a.memoryComponent()
	if err != nil {
		return nil, err
	}
	memories, err := store.List()
	if err != nil {
		return nil, fmt.Errorf("app: list memories: %w", err)
	}
	out := make([]MemoryDTO, 0, len(memories))
	for _, mem := range memories {
		out = append(out, MemoryDTO{
			Name:        mem.Name,
			Description: mem.Description,
			Type:        mem.Type,
			FileName:    mem.FileName,
			UpdatedAtMS: mem.UpdatedAt.UnixMilli(),
		})
	}
	return out, nil
}
