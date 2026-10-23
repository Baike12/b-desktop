// Package memory 实现基于文件的长期记忆：MEMORY.md 是索引，每条记忆一个
// markdown 文件，frontmatter 提供 name、description、type。
//
// 目录约定（参考 ../cc/src/memdir/paths.ts 与 memoryScan.ts）：
//
//	~/.b-desktop/memory/MEMORY.md          索引
//	~/.b-desktop/memory/<slug>.md          单条记忆
//
// 与参考实现使用一次模型侧查询挑选记忆不同，本包按关键词重合度打分，
// 不引入额外模型调用；召回结果最多 5 条注入系统提示。
package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// IndexFileName 是记忆索引文件名。
const IndexFileName = "MEMORY.md"

// 存储限制与默认值。
const (
	// maxMemories 是单次扫描读取的记忆文件上限。
	maxMemories = 200
	// defaultRecallLimit 是 Recall 的默认返回条数。
	defaultRecallLimit = 5
	// maxRecallBodyRunes 是单条记忆注入上下文时的正文上限。
	maxRecallBodyRunes = 2000
	// maxContextRunes 是注入上下文的整体字符上限。
	maxContextRunes = 8000
	// maxNameRunes 是记忆名的字符上限。
	maxNameRunes = 100
)

// 错误哨兵。
var (
	// ErrEmptyName 表示记忆名为空。
	ErrEmptyName = errors.New("memory: empty name")
	// ErrEmptyBody 表示记忆正文为空。
	ErrEmptyBody = errors.New("memory: empty body")
	// ErrInvalidType 表示记忆类型不合法。
	ErrInvalidType = errors.New("memory: invalid type")
)

// Memory 是一条记忆。Body 是去掉 frontmatter 后的正文。
type Memory struct {
	// Name 是记忆名，来自 frontmatter 的 name。
	Name string
	// Description 是一行描述，用于召回打分与索引展示。
	Description string
	// Type 是记忆类型，例如 user、feedback、project、reference。
	Type string
	// FileName 是记忆文件相对 Store 根目录的文件名。
	FileName string
	// Body 是记忆正文。
	Body string
	// UpdatedAt 是文件修改时间。
	UpdatedAt time.Time
}

// Store 管理一个记忆目录。所有导出方法并发安全。
type Store struct {
	mu  sync.Mutex
	dir string
}

// NewStore 打开或创建记忆目录，并确保索引文件存在。
func NewStore(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("memory: empty directory")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("memory: resolve dir %q: %w", dir, err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("memory: create dir %s: %w", abs, err)
	}
	store := &Store{dir: abs}
	if err := store.ensureIndex(); err != nil {
		return nil, err
	}
	return store, nil
}

// DefaultDir 返回用户级记忆目录 ~/.b-desktop/memory。
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("memory: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".b-desktop", "memory"), nil
}

// Dir 返回记忆目录的绝对路径。
func (s *Store) Dir() string { return s.dir }

// ensureIndex 在索引文件缺失时创建带标题的空索引。
func (s *Store) ensureIndex() error {
	path := filepath.Join(s.dir, IndexFileName)
	info, err := os.Stat(path)
	if err == nil && info.Size() > 0 {
		return nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("memory: stat index %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte("# 记忆索引\n\n"), 0o600); err != nil {
		return fmt.Errorf("memory: create index %s: %w", path, err)
	}
	return nil
}

// Index 返回 MEMORY.md 的完整内容。
func (s *Store) Index(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(filepath.Join(s.dir, IndexFileName))
	if err != nil {
		return "", fmt.Errorf("memory: read index: %w", err)
	}
	return string(data), nil
}

// List 返回全部记忆，按修改时间从新到旧排序；索引文件本身不返回。
func (s *Store) List() ([]Memory, error) {
	return s.list(context.Background())
}

// list 在持锁状态下扫描记忆目录。
func (s *Store) list(ctx context.Context) ([]Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("memory: read dir %s: %w", s.dir, err)
	}
	out := make([]Memory, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") || entry.Name() == IndexFileName {
			continue
		}
		path := filepath.Join(s.dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		name, description, typ, body, _ := parseMemory(string(data))
		if strings.TrimSpace(name) == "" {
			name = strings.TrimSuffix(entry.Name(), ".md")
		}
		out = append(out, Memory{
			Name:        strings.TrimSpace(name),
			Description: strings.TrimSpace(description),
			Type:        strings.TrimSpace(typ),
			FileName:    entry.Name(),
			Body:        strings.TrimSpace(body),
			UpdatedAt:   info.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].Name < out[j].Name
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	if len(out) > maxMemories {
		out = out[:maxMemories]
	}
	return out, nil
}

// Recall 按关键词重合度召回与 prompt 相关的记忆，最多 limit 条。
// limit <= 0 时使用默认值 5；没有重合项的条目不返回。
func (s *Store) Recall(ctx context.Context, prompt string, limit int) ([]Memory, error) {
	if limit <= 0 {
		limit = defaultRecallLimit
	}
	memories, err := s.list(ctx)
	if err != nil {
		return nil, err
	}
	tokens := tokenize(prompt)
	if len(tokens) == 0 || len(memories) == 0 {
		return nil, nil
	}
	type scored struct {
		mem   Memory
		score int
	}
	ranked := make([]scored, 0, len(memories))
	for _, mem := range memories {
		description := tokenize(mem.Name + " " + mem.Description)
		body := tokenize(mem.Body)
		score := 0
		for token := range tokens {
			if _, ok := description[token]; ok {
				score += 3
				continue
			}
			if _, ok := body[token]; ok {
				score++
			}
		}
		if score == 0 {
			continue
		}
		ranked = append(ranked, scored{mem: mem, score: score})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		if !ranked[i].mem.UpdatedAt.Equal(ranked[j].mem.UpdatedAt) {
			return ranked[i].mem.UpdatedAt.After(ranked[j].mem.UpdatedAt)
		}
		return ranked[i].mem.Name < ranked[j].mem.Name
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]Memory, 0, len(ranked))
	for _, item := range ranked {
		out = append(out, item.mem)
	}
	return out, nil
}

// ExtraContext 把与 prompt 相关的记忆注入系统提示，最多 5 条。
// 没有相关记忆时返回空字符串。
func (s *Store) ExtraContext(ctx context.Context, sessionID, workDir, prompt string) string {
	memories, err := s.Recall(ctx, prompt, defaultRecallLimit)
	if err != nil || len(memories) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## 相关记忆\n")
	b.WriteString("以下记忆来自用户记忆目录，可能已过时；与当前代码或文件冲突时，以实际状态为准。\n")
	for _, mem := range memories {
		b.WriteString("\n### ")
		if mem.Type != "" {
			b.WriteString("[" + mem.Type + "] ")
		}
		b.WriteString(mem.Name)
		b.WriteString("\n")
		if mem.Description != "" {
			b.WriteString(mem.Description)
			b.WriteString("\n")
		}
		if mem.Body != "" {
			b.WriteString(truncateRunes(mem.Body, maxRecallBodyRunes))
			b.WriteString("\n")
		}
	}
	return truncateRunes(strings.TrimRight(b.String(), "\n"), maxContextRunes)
}

// Save 写入一条新记忆并追加索引行，返回落盘后的记忆。
func (s *Store) Save(ctx context.Context, name, description, typ, body string) (Memory, error) {
	if err := ctx.Err(); err != nil {
		return Memory{}, err
	}
	name = collapseSpaces(name)
	if name == "" {
		return Memory{}, ErrEmptyName
	}
	if utf8.RuneCountInString(name) > maxNameRunes {
		return Memory{}, fmt.Errorf("%w: name longer than %d runes", ErrEmptyName, maxNameRunes)
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return Memory{}, ErrEmptyBody
	}
	if strings.TrimSpace(description) == "" {
		description = firstLine(body)
	}
	description = collapseSpaces(description)
	typ = strings.ToLower(strings.TrimSpace(typ))
	if typ == "" {
		typ = "project"
	}
	if !validType(typ) {
		return Memory{}, fmt.Errorf("%w: %q", ErrInvalidType, typ)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Memory{}, err
	}
	fileName, err := s.uniqueFileName(name)
	if err != nil {
		return Memory{}, err
	}
	path := filepath.Join(s.dir, fileName)
	content := fmt.Sprintf("---\nname: %s\ndescription: %s\ntype: %s\n---\n\n%s\n", name, description, typ, body)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return Memory{}, fmt.Errorf("memory: write %s: %w", path, err)
	}
	if err := s.appendIndex(fileName, name, description, typ); err != nil {
		return Memory{}, err
	}
	info, err := os.Stat(path)
	updated := time.Now()
	if err == nil {
		updated = info.ModTime()
	}
	return Memory{
		Name:        name,
		Description: description,
		Type:        typ,
		FileName:    fileName,
		Body:        body,
		UpdatedAt:   updated,
	}, nil
}

// appendIndex 在索引末尾追加一行。调用方必须持有 s.mu。
func (s *Store) appendIndex(fileName, name, description, typ string) error {
	path := filepath.Join(s.dir, IndexFileName)
	if err := s.ensureIndex(); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("memory: open index %s: %w", path, err)
	}
	line := fmt.Sprintf("- [%s] %s: %s\n", typ, name, description)
	if existing, readErr := os.ReadFile(path); readErr == nil && len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		line = "\n" + line
	}
	if _, err := f.WriteString(line); err != nil {
		return fmt.Errorf("memory: append index %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("memory: close index %s: %w", path, err)
	}
	return nil
}

// uniqueFileName 由记忆名生成不冲突的文件名。调用方必须持有 s.mu。
func (s *Store) uniqueFileName(name string) (string, error) {
	base := slug(name)
	if base == "" {
		base = "memory-" + time.Now().UTC().Format("20060102-150405")
	}
	candidate := base + ".md"
	for i := 2; ; i++ {
		path := filepath.Join(s.dir, candidate)
		_, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", fmt.Errorf("memory: stat %s: %w", path, err)
		}
		if i > 1000 {
			return "", fmt.Errorf("memory: too many files for name %q", name)
		}
		candidate = fmt.Sprintf("%s-%d.md", base, i)
	}
}

// parseMemory 解析记忆文件的 frontmatter（name、description、type）与正文。
func parseMemory(content string) (name, description, typ, body string, ok bool) {
	content = strings.TrimPrefix(content, "\ufeff")
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(strings.TrimSuffix(lines[0], "\r")) != "---" {
		return "", "", "", content, false
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(strings.TrimSuffix(lines[i], "\r")) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return "", "", "", content, false
	}
	for _, line := range lines[1:end] {
		line = strings.TrimSuffix(line, "\r")
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "name":
			name = unquote(strings.TrimSpace(value))
		case "description":
			description = unquote(strings.TrimSpace(value))
		case "type":
			typ = unquote(strings.TrimSpace(value))
		}
	}
	return name, description, typ, strings.Join(lines[end+1:], "\n"), true
}

// validType 校验记忆类型：小写字母开头，只含字母、数字、下划线与连字符。
func validType(typ string) bool {
	if typ == "" {
		return false
	}
	for i, r := range typ {
		if i == 0 {
			if !unicode.IsLower(r) {
				return false
			}
			continue
		}
		if !unicode.IsLower(r) && !unicode.IsDigit(r) && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

// slug 把记忆名转成文件名主体，保留中日韩字符、字母、数字、下划线与连字符。
func slug(name string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
			b.WriteRune(r)
			lastDash = false
		case r == '-' || unicode.IsSpace(r):
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
		if b.Len() >= 120 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

// collapseSpaces 把换行与连续空白压成单个空格。
func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// firstLine 返回第一个非空行。
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// unquote 去掉成对的单引号或双引号。
func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// truncateRunes 按字符数截断字符串。
func truncateRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "…"
}
