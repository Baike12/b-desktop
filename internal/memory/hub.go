package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// ScopeUser 与 ScopeProject 是记忆作用域标识。
const (
	ScopeUser    = "user"
	ScopeProject = "project"
)

// maxIndexEntries 是抽取 prompt 中携带的既有记忆条数上限。
const maxIndexEntries = 60

// maxSummaryRunes 是抽取 prompt 中既有记忆清单的字符上限。
const maxSummaryRunes = 6000

// Hub 聚合用户级与项目级两层记忆库，是 App 与工具的统一入口。
// 所有导出方法并发安全。
type Hub struct {
	mu       sync.Mutex
	dir      string
	user     *Store
	projects map[string]*Store
}

// NewHub 打开用户级记忆目录并惰性管理项目级目录。
// userDir 通常为 ~/.b-desktop/memory；项目级目录是其下 projects/ 子目录。
func NewHub(userDir string) (*Hub, error) {
	user, err := NewStore(userDir)
	if err != nil {
		return nil, err
	}
	return &Hub{
		dir:      user.Dir(),
		user:     user,
		projects: make(map[string]*Store),
	}, nil
}

// User 返回用户级记忆库。
func (h *Hub) User() *Store { return h.user }

// ProjectDirName 由工作目录推导项目级记忆目录名：
// 项目 slug + 绝对路径 SHA-256 前 16 位，确定性且不依赖 git。
func ProjectDirName(workDir string) string {
	abs, err := filepath.Abs(workDir)
	if err != nil {
		abs = workDir
	}
	sum := sha256.Sum256([]byte(abs))
	base := slug(filepath.Base(abs))
	if base == "" {
		base = "project"
	}
	if utf8.RuneCountInString(base) > 40 {
		base = string([]rune(base)[:40])
	}
	return base + "-" + hex.EncodeToString(sum[:8])
}

// Project 返回工作目录对应的项目级记忆库，首次访问时创建。
func (h *Hub) Project(workDir string) (*Store, error) {
	if strings.TrimSpace(workDir) == "" {
		return nil, fmt.Errorf("memory: empty work dir")
	}
	name := ProjectDirName(workDir)
	h.mu.Lock()
	defer h.mu.Unlock()
	if store, ok := h.projects[name]; ok {
		return store, nil
	}
	store, err := NewStore(filepath.Join(h.dir, ProjectsDirName, name))
	if err != nil {
		return nil, err
	}
	h.projects[name] = store
	return store, nil
}

// resolveStore 按 scope 解析记忆库。scope 为空时默认 user。
func (h *Hub) resolveStore(scope, workDir string) (*Store, error) {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "", ScopeUser:
		return h.user, nil
	case ScopeProject:
		return h.Project(workDir)
	default:
		return nil, fmt.Errorf("memory: unknown scope %q", scope)
	}
}

// Save 在指定作用域写入记忆。
func (h *Hub) Save(ctx context.Context, scope, workDir string, in EntryInput) (Memory, bool, error) {
	store, err := h.resolveStore(scope, workDir)
	if err != nil {
		return Memory{}, false, err
	}
	mem, created, err := store.Save(ctx, in)
	if err == nil {
		mem.Scope = scopeOf(scope)
	}
	return mem, created, err
}

// Update 在指定作用域更新记忆。
func (h *Hub) Update(ctx context.Context, scope, workDir, fileName string, in EntryInput) (Memory, bool, error) {
	store, err := h.resolveStore(scope, workDir)
	if err != nil {
		return Memory{}, false, err
	}
	mem, changed, err := store.Update(ctx, fileName, in)
	if err == nil {
		mem.Scope = scopeOf(scope)
	}
	return mem, changed, err
}

// Archive 在指定作用域软删记忆。
func (h *Hub) Archive(ctx context.Context, scope, workDir, fileName string) (Memory, error) {
	store, err := h.resolveStore(scope, workDir)
	if err != nil {
		return Memory{}, err
	}
	mem, err := store.Archive(ctx, fileName)
	if err == nil {
		mem.Scope = scopeOf(scope)
	}
	return mem, err
}

// Get 在指定作用域读取单条记忆。
func (h *Hub) Get(scope, workDir, fileName string) (Memory, error) {
	store, err := h.resolveStore(scope, workDir)
	if err != nil {
		return Memory{}, err
	}
	mem, err := store.Get(fileName)
	if err == nil {
		mem.Scope = scopeOf(scope)
	}
	return mem, err
}

// List 返回指定作用域的全部 active 记忆。
func (h *Hub) List(scope, workDir string) ([]Memory, error) {
	store, err := h.resolveStore(scope, workDir)
	if err != nil {
		return nil, err
	}
	entries, err := store.List()
	for i := range entries {
		entries[i].Scope = scopeOf(scope)
	}
	return entries, err
}

// Recall 合并两层记忆库的召回结果：项目级带 projectBoost 加成，
// 返回得分最高的 limit 条，条目带 Scope 标识。
func (h *Hub) Recall(ctx context.Context, workDir, prompt string, limit int) []Scored {
	if limit <= 0 {
		limit = defaultRecallLimit
	}
	userHits, err := h.user.Recall(ctx, prompt, limit, 1)
	if err != nil {
		userHits = nil
	}
	var projectHits []Scored
	if project, err := h.Project(workDir); err == nil {
		if hits, err := project.Recall(ctx, prompt, limit, projectBoost); err == nil {
			projectHits = hits
		}
	}
	merged := make([]Scored, 0, len(userHits)+len(projectHits))
	for _, hit := range userHits {
		hit.Memory.Scope = ScopeUser
		merged = append(merged, hit)
	}
	for _, hit := range projectHits {
		hit.Memory.Scope = ScopeProject
		merged = append(merged, hit)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Score != merged[j].Score {
			return merged[i].Score > merged[j].Score
		}
		return merged[i].Memory.Name < merged[j].Memory.Name
	})
	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged
}

// ContextText 组装注入系统提示的记忆文本：两层索引 + 合并排序的
// 相关记忆正文。两层都没有内容时返回空字符串。
func (h *Hub) ContextText(ctx context.Context, workDir, prompt string) string {
	indexUser := strings.TrimSpace(h.user.IndexText(ctx))
	indexProject := ""
	if project, err := h.Project(workDir); err == nil {
		indexProject = strings.TrimSpace(project.IndexText(ctx))
	}
	if indexUser == "" && indexProject == "" {
		return ""
	}

	var b strings.Builder
	b.WriteString("## 长期记忆索引\n")
	b.WriteString("以下是已保存的长期记忆（用户级 + 本项目级）。保存、更新或删除记忆使用 Memory 工具；索引中的记忆可能已过时，与实际状态冲突时以实际状态为准。\n\n")
	if indexUser != "" {
		b.WriteString("### 用户级\n\n" + indexUser + "\n\n")
	}
	if indexProject != "" {
		b.WriteString("### 本项目级\n\n" + indexProject + "\n\n")
	}

	merged := h.Recall(ctx, workDir, prompt, defaultRecallLimit)
	if len(merged) == 0 {
		return strings.TrimRight(b.String(), "\n")
	}
	b.WriteString("## 相关记忆\n")
	b.WriteString("以下记忆与当前输入相关，按匹配度排序，可能已过时；与当前代码或文件冲突时，以实际状态为准。\n")
	for _, item := range merged {
		b.WriteString("\n### ")
		if item.Memory.Type != "" {
			b.WriteString("[" + item.Memory.Type + "] ")
		}
		if item.Memory.Scope == ScopeProject {
			b.WriteString("[项目] ")
		}
		b.WriteString(item.Memory.Name)
		b.WriteString("\n")
		if days := staleDays(item.Memory, time.Now()); days >= 1 {
			b.WriteString(fmt.Sprintf("(更新于 %d 天前)\n", days))
		}
		if item.Memory.Description != "" {
			b.WriteString(item.Memory.Description)
			b.WriteString("\n")
		}
		if item.Memory.Body != "" {
			b.WriteString(truncateRunes(item.Memory.Body, maxRecallBodyRunes))
			b.WriteString("\n")
		}
	}
	return truncateRunes(strings.TrimRight(b.String(), "\n"), maxContextRunes)
}

// indexSummary 返回既有记忆的「fileName | name — description」清单，供
// 抽取 prompt 让模型引用既有条目做合并。用户级在前，项目级在后。
func (h *Hub) indexSummary(ctx context.Context, workDir string) string {
	var b strings.Builder
	remaining := maxIndexEntries
	appendEntries := func(label string, entries []Memory) {
		if len(entries) == 0 || remaining <= 0 {
			return
		}
		b.WriteString("[" + label + "]\n")
		for _, mem := range entries {
			if remaining <= 0 || b.Len() >= maxSummaryRunes {
				break
			}
			b.WriteString("- " + mem.FileName + " | " + mem.Name)
			if mem.Description != "" {
				b.WriteString(" — " + mem.Description)
			}
			b.WriteString("\n")
			remaining--
		}
		b.WriteString("\n")
	}
	userEntries, err := h.user.List()
	if err == nil {
		appendEntries("用户级 scope=user", userEntries)
	}
	if project, err := h.Project(workDir); err == nil {
		projectEntries, err := project.List()
		if err == nil {
			appendEntries("项目级 scope=project", projectEntries)
		}
	}
	return strings.TrimSpace(b.String())
}

// staleDays 返回记忆距今更新的天数（向下取整）。
func staleDays(mem Memory, now time.Time) int {
	base := mem.UpdatedAt
	if base.IsZero() || now.Before(base) {
		return 0
	}
	return int(now.Sub(base).Hours() / 24)
}

// scopeOf 归一化作用域：空值默认用户级。
func scopeOf(scope string) string {
	if strings.EqualFold(strings.TrimSpace(scope), ScopeProject) {
		return ScopeProject
	}
	return ScopeUser
}
