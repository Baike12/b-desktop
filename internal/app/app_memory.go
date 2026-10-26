package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Baike12/b-desktop/internal/memory"
)

// ErrMemoryUnavailable 表示记忆组件无法初始化。
var ErrMemoryUnavailable = errors.New("app: memory unavailable")

// MemoryDTO 是一条记忆的元信息，不含正文。时间字段为 Unix 毫秒。
type MemoryDTO struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Type        string   `json:"type"`
	FileName    string   `json:"fileName"`
	Scope       string   `json:"scope"`
	Keywords    []string `json:"keywords"`
	Importance  int      `json:"importance"`
	Status      string   `json:"status"`
	CreatedAtMS int64    `json:"createdAtMs"`
	UpdatedAtMS int64    `json:"updatedAtMs"`
}

// MemoryDetailDTO 是一条记忆的完整视图，含正文与访问元数据。
type MemoryDetailDTO struct {
	MemoryDTO
	Body        string   `json:"body"`
	Sources     []string `json:"sources"`
	AccessCount int      `json:"accessCount"`
}

// MemorySaveInput 是 SaveMemory 绑定的入参。
type MemorySaveInput struct {
	Scope       string   `json:"scope"`
	WorkDir     string   `json:"workDir"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Type        string   `json:"type"`
	Keywords    []string `json:"keywords"`
	Importance  int      `json:"importance"`
	Content     string   `json:"content"`
}

// MemoryUpdateInput 是 UpdateMemory 绑定的入参；空字段保持不变。
type MemoryUpdateInput struct {
	Scope       string   `json:"scope"`
	WorkDir     string   `json:"workDir"`
	FileName    string   `json:"fileName"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Type        string   `json:"type"`
	Keywords    []string `json:"keywords"`
	Importance  int      `json:"importance"`
	Content     string   `json:"content"`
}

// MemoryChangedDTO 是 memory:changed 事件的载荷，汇报一次自动抽取的结果。
type MemoryChangedDTO struct {
	SessionID string `json:"sessionId,omitempty"`
	Saved     int    `json:"saved"`
	Updated   int    `json:"updated"`
	Deleted   int    `json:"deleted"`
	Error     string `json:"error,omitempty"`
}

// memoryComponent 返回记忆 Hub：用户级 ~/.b-desktop/memory 与其下的
// 项目级子目录。组件按 key 惰性创建并跨会话复用。
func (a *App) memoryComponent() (*memory.Hub, error) {
	value, err := a.extensionStore("memory", func() (any, error) {
		dir, err := a.settingsDir()
		if err != nil {
			return nil, err
		}
		return memory.NewHub(filepath.Join(dir, "memory"))
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMemoryUnavailable, err)
	}
	hub, ok := value.(*memory.Hub)
	if !ok {
		return nil, fmt.Errorf("%w: unexpected component type %T", ErrMemoryUnavailable, value)
	}
	return hub, nil
}

// ListMemories 返回指定作用域的全部 active 记忆，按修改时间从新到旧排序。
// scope 为 user 时 workDir 可为空；scope 为 project 时 workDir 必填。
func (a *App) ListMemories(scope, workDir string) ([]MemoryDTO, error) {
	hub, err := a.memoryComponent()
	if err != nil {
		return nil, err
	}
	entries, err := hub.List(scope, workDir)
	if err != nil {
		return nil, fmt.Errorf("app: list memories: %w", err)
	}
	out := make([]MemoryDTO, 0, len(entries))
	for _, mem := range entries {
		out = append(out, toMemoryDTO(mem))
	}
	return out, nil
}

// ReadMemory 返回指定作用域单条记忆的完整内容。
func (a *App) ReadMemory(scope, workDir, fileName string) (MemoryDetailDTO, error) {
	hub, err := a.memoryComponent()
	if err != nil {
		return MemoryDetailDTO{}, err
	}
	mem, err := hub.Get(scope, workDir, fileName)
	if err != nil {
		return MemoryDetailDTO{}, fmt.Errorf("app: read memory: %w", err)
	}
	detail := MemoryDetailDTO{
		MemoryDTO:   toMemoryDTO(mem),
		Body:        mem.Body,
		Sources:     mem.Sources,
		AccessCount: mem.AccessCount,
	}
	return detail, nil
}

// SaveMemory 写入一条用户手工创建的记忆。
func (a *App) SaveMemory(in MemorySaveInput) (MemoryDetailDTO, error) {
	hub, err := a.memoryComponent()
	if err != nil {
		return MemoryDetailDTO{}, err
	}
	mem, created, err := hub.Save(context.Background(), in.Scope, in.WorkDir, memory.EntryInput{
		Name:        in.Name,
		Description: in.Description,
		Type:        in.Type,
		Keywords:    in.Keywords,
		Importance:  in.Importance,
		Body:        in.Content,
	})
	if err != nil {
		return MemoryDetailDTO{}, fmt.Errorf("app: save memory: %w", err)
	}
	if !created {
		return MemoryDetailDTO{}, fmt.Errorf("app: save memory: %w", ErrMemoryDuplicate)
	}
	a.emitMemoryChanged(MemoryChangedDTO{Saved: 1})
	return toMemoryDetail(mem), nil
}

// UpdateMemory 局部更新一条记忆；空字段保持不变。
func (a *App) UpdateMemory(in MemoryUpdateInput) (MemoryDetailDTO, error) {
	hub, err := a.memoryComponent()
	if err != nil {
		return MemoryDetailDTO{}, err
	}
	mem, _, err := hub.Update(context.Background(), in.Scope, in.WorkDir, in.FileName, memory.EntryInput{
		Name:        in.Name,
		Description: in.Description,
		Type:        in.Type,
		Keywords:    in.Keywords,
		Importance:  in.Importance,
		Body:        in.Content,
	})
	if err != nil {
		return MemoryDetailDTO{}, fmt.Errorf("app: update memory: %w", err)
	}
	a.emitMemoryChanged(MemoryChangedDTO{Updated: 1})
	return toMemoryDetail(mem), nil
}

// DeleteMemory 软删一条记忆：标记归档并从索引移除，文件保留。
func (a *App) DeleteMemory(scope, workDir, fileName string) error {
	hub, err := a.memoryComponent()
	if err != nil {
		return err
	}
	if _, err := hub.Archive(context.Background(), scope, workDir, fileName); err != nil {
		return fmt.Errorf("app: delete memory: %w", err)
	}
	a.emitMemoryChanged(MemoryChangedDTO{Deleted: 1})
	return nil
}

// ErrMemoryDuplicate 表示保存的记忆与现有条目内容相同。
var ErrMemoryDuplicate = errors.New("memory already exists")

// emitMemoryChanged 推送记忆库变更事件；绑定层改动与自动抽取共用一个事件名。
func (a *App) emitMemoryChanged(payload MemoryChangedDTO) {
	a.emitter.EmitMemoryChanged(a.lifeCtx(), payload)
}

// EmitMemoryChanged 发送 memory:changed 事件。复用标题事件的通道语义：
// 不参与 agent 流式事件的 seq 归并。
func (e *Emitter) EmitMemoryChanged(ctx context.Context, payload MemoryChangedDTO) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.snd != nil {
		e.snd(ctx, EventNameMemoryChanged, payload)
	}
}

// toMemoryDTO 转换记忆元信息。
func toMemoryDTO(mem memory.Memory) MemoryDTO {
	return MemoryDTO{
		Name:        mem.Name,
		Description: mem.Description,
		Type:        mem.Type,
		FileName:    mem.FileName,
		Scope:       scopeOrDefault(mem.Scope),
		Keywords:    mem.Keywords,
		Importance:  mem.Importance,
		Status:      mem.Status,
		CreatedAtMS: timeMS(mem.CreatedAt),
		UpdatedAtMS: timeMS(mem.UpdatedAt),
	}
}

// toMemoryDetail 转换记忆完整视图。
func toMemoryDetail(mem memory.Memory) MemoryDetailDTO {
	return MemoryDetailDTO{
		MemoryDTO:   toMemoryDTO(mem),
		Body:        mem.Body,
		Sources:     mem.Sources,
		AccessCount: mem.AccessCount,
	}
}

// scopeOrDefault 归一化空作用域为用户级。
func scopeOrDefault(scope string) string {
	if strings.TrimSpace(scope) == "" {
		return memory.ScopeUser
	}
	return scope
}

// timeMS 转换时间为 Unix 毫秒；零值返回 0。
func timeMS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}
