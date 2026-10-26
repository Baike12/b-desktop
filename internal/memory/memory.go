// Package memory 实现基于文件的长期记忆：MEMORY.md 是索引，每条记忆一个
// markdown 文件，frontmatter 提供检索与生命周期字段。
//
// 目录约定（参考 ../cc/src/memdir/paths.ts 的双层作用域与 200 行/25KB
// 索引上限，设计依据见 docs/memory.md）：
//
//	~/.b-desktop/memory/MEMORY.md                    用户级索引
//	~/.b-desktop/memory/<slug>.md                    用户级记忆
//	~/.b-desktop/memory/projects/<slug>-<sha16>/     项目级记忆目录
//
// 与参考实现用一次模型侧查询挑选记忆不同，本包召回按确定性打分
// （相关度 + 新近性 + 重要性，score.go），不引入额外模型调用；唯一的
// 模型调用在轮末自动抽取（extract.go），可整体关闭。
package memory

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// IndexFileName 是记忆索引文件名。
const IndexFileName = "MEMORY.md"

// ProjectsDirName 是项目级记忆在用户记忆目录下的子目录名。
const ProjectsDirName = "projects"

// 记忆状态。
const (
	// StatusActive 表示记忆可用，参与召回与注入。
	StatusActive = "active"
	// StatusArchived 表示记忆已删除（软删），文件保留但不参与任何注入。
	StatusArchived = "archived"
)

// 存储限制与默认值。
const (
	// maxMemories 是单次扫描读取的记忆文件上限。
	maxMemories = 200
	// indexMaxLines 是注入系统提示的索引行数上限（对齐 cc memdir）。
	indexMaxLines = 200
	// indexMaxBytes 是注入系统提示的索引字节上限（对齐 cc memdir）。
	indexMaxBytes = 25 * 1024
	// defaultRecallLimit 是 Recall 的默认返回条数。
	defaultRecallLimit = 5
	// maxRecallBodyRunes 是单条记忆注入上下文时的正文上限。
	maxRecallBodyRunes = 2000
	// maxContextRunes 是注入上下文的整体字符上限。
	maxContextRunes = 8000
	// maxNameRunes 是记忆名的字符上限。
	maxNameRunes = 100
	// touchInterval 是召回命中后回写 lastAccessedAt 的最小间隔。
	touchInterval = time.Hour
)

// 错误哨兵。
var (
	// ErrEmptyName 表示记忆名为空。
	ErrEmptyName = errors.New("memory: empty name")
	// ErrEmptyBody 表示记忆正文为空。
	ErrEmptyBody = errors.New("memory: empty body")
	// ErrInvalidType 表示记忆类型不合法。
	ErrInvalidType = errors.New("memory: invalid type")
	// ErrNotFound 表示指定文件的记忆不存在。
	ErrNotFound = errors.New("memory: not found")
	// ErrInvalidImportance 表示重要性取值超出 1-10。
	ErrInvalidImportance = errors.New("memory: invalid importance")
)

// Memory 是一条记忆。Body 是去掉 frontmatter 后的正文。
type Memory struct {
	// Name 是记忆名，来自 frontmatter 的 name。
	Name string
	// Description 是一行描述，用于召回打分与索引展示。
	Description string
	// Type 是记忆类型：user、feedback、project 或 reference。
	Type string
	// Keywords 是召回加权关键词，由写入方（工具或抽取器）生成。
	Keywords []string
	// Importance 是 1-10 的重要性评分，0 表示未评分。
	Importance int
	// Sources 是记忆来源标识（例如会话轮次），用于溯源。
	Sources []string
	// FileName 是记忆文件相对 Store 根目录的文件名。
	FileName string
	// Scope 是记忆作用域：user 或 project。由 Hub 在读取时标注，
	// 单个 Store 内的条目不含该信息。
	Scope string
	// Body 是记忆正文。
	Body string
	// CreatedAt 是记忆创建时间；旧版文件缺失时为零值。
	CreatedAt time.Time
	// UpdatedAt 是最后一次内容修改时间。
	UpdatedAt time.Time
	// LastAccessedAt 是最后一次召回命中时间。
	LastAccessedAt time.Time
	// AccessCount 是累计召回命中次数。
	AccessCount int
	// Status 是记忆状态：active 或 archived。
	Status string
	// Hash 是正文的 md5 指纹，用于写入幂等去重。
	Hash string
}

// EntryInput 是写入或更新记忆的参数；零值字段在 Update 中表示保持不变。
type EntryInput struct {
	Name        string
	Description string
	Type        string
	Keywords    []string
	// Importance 取 1-10；0 表示未评分，Update 中 0 表示保持不变。
	Importance int
	Sources    []string
	Body       string
}

// Store 管理一个记忆目录（用户级或项目级单层）。所有导出方法并发安全。
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

// IndexText 返回截断到 200 行 / 25KB 的索引文本，超限时追加说明行。
// 只有标题、没有任何条目的索引按空处理（注入时跳过该层）。
func (s *Store) IndexText(ctx context.Context) string {
	raw, err := s.Index(ctx)
	if err != nil || !strings.Contains(raw, "\n- ") && !strings.HasPrefix(raw, "- ") {
		return ""
	}
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	total := utf8.RuneCountInString(raw)
	if len(lines) <= indexMaxLines && total <= indexMaxBytes {
		return raw
	}
	kept := 0
	used := 0
	for i, line := range lines {
		if i >= indexMaxLines || used+len(line) >= indexMaxBytes {
			break
		}
		used += len(line) + 1
		kept = i + 1
	}
	out := strings.Join(lines[:kept], "\n")
	out += "\n\n(WARNING: 记忆索引超过上限已被截断，请合并或删除过期记忆。)"
	return out
}

// List 返回全部 active 记忆，按修改时间从新到旧排序；索引与归档条目不返回。
func (s *Store) List() ([]Memory, error) {
	return s.list(context.Background(), false)
}

// Get 按 fileName 返回记忆，包含归档条目。
func (s *Store) Get(fileName string) (Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readEntry(fileName)
}

// readEntry 读取并解析单个记忆文件。调用方必须持有 s.mu。
func (s *Store) readEntry(fileName string) (Memory, error) {
	if fileName != filepath.Base(fileName) || strings.Contains(fileName, "/") || strings.Contains(fileName, "\\") {
		return Memory{}, fmt.Errorf("memory: invalid file name %q", fileName)
	}
	path := filepath.Join(s.dir, fileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Memory{}, ErrNotFound
	}
	if err != nil {
		return Memory{}, fmt.Errorf("memory: read %s: %w", path, err)
	}
	mem, ok := parseMemory(string(data))
	if !ok || strings.TrimSpace(mem.Name) == "" {
		mem.Name = strings.TrimSuffix(fileName, ".md")
	}
	mem.FileName = fileName
	if mem.Status == "" {
		mem.Status = StatusActive
	}
	return mem, nil
}

// list 扫描记忆目录。includeArchived 为 true 时同时返回归档条目。
// 调用方必须持有 s.mu。
func (s *Store) list(ctx context.Context, includeArchived bool) ([]Memory, error) {
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
		mem, err := s.readEntry(entry.Name())
		if err != nil {
			continue
		}
		if mem.Status == StatusArchived && !includeArchived {
			continue
		}
		info, err := entry.Info()
		if err == nil && mem.UpdatedAt.IsZero() {
			mem.UpdatedAt = info.ModTime()
		}
		if mem.CreatedAt.IsZero() {
			mem.CreatedAt = mem.UpdatedAt
		}
		if mem.LastAccessedAt.IsZero() {
			mem.LastAccessedAt = mem.UpdatedAt
		}
		out = append(out, mem)
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

// Save 写入一条新记忆并重建索引。正文指纹与现有 active 记忆相同时不
// 重复落盘，返回已有条目且 created 为 false（幂等写入，mem0 语义）。
func (s *Store) Save(ctx context.Context, in EntryInput) (Memory, bool, error) {
	if err := ctx.Err(); err != nil {
		return Memory{}, false, err
	}
	mem, err := newMemory(in)
	if err != nil {
		return Memory{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Memory{}, false, err
	}
	// 幂等：同指纹的 active 记忆已存在则不重复保存。
	existing, err := s.list(ctx, false)
	if err != nil {
		return Memory{}, false, err
	}
	for _, item := range existing {
		if item.Hash == mem.Hash {
			return item, false, nil
		}
	}
	fileName, err := s.uniqueFileName(mem.Name)
	if err != nil {
		return Memory{}, false, err
	}
	mem.FileName = fileName
	if err := s.writeEntry(mem); err != nil {
		return Memory{}, false, err
	}
	if err := s.rebuildIndexLocked(); err != nil {
		return Memory{}, false, err
	}
	return mem, true, nil
}

// Update 局部更新一条记忆：EntryInput 的非零字段覆盖既有值。
// 内容与元数据都没变时返回 changed=false 且不落盘。
func (s *Store) Update(ctx context.Context, fileName string, in EntryInput) (Memory, bool, error) {
	if err := ctx.Err(); err != nil {
		return Memory{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.readEntry(fileName)
	if err != nil {
		return Memory{}, false, err
	}
	updated := current
	changed := false
	if name := collapseSpaces(in.Name); name != "" && name != current.Name {
		if utf8.RuneCountInString(name) > maxNameRunes {
			return Memory{}, false, fmt.Errorf("%w: name longer than %d runes", ErrEmptyName, maxNameRunes)
		}
		updated.Name = name
		changed = true
	}
	if desc := collapseSpaces(in.Description); desc != "" && desc != current.Description {
		updated.Description = desc
		changed = true
	}
	if typ := strings.ToLower(strings.TrimSpace(in.Type)); typ != "" {
		if !validType(typ) {
			return Memory{}, false, fmt.Errorf("%w: %q", ErrInvalidType, typ)
		}
		if typ != current.Type {
			updated.Type = typ
			changed = true
		}
	}
	if len(in.Keywords) > 0 && !sameStrings(in.Keywords, current.Keywords) {
		updated.Keywords = normalizeKeywords(in.Keywords)
		changed = true
	}
	if in.Importance > 0 {
		if in.Importance > 10 {
			return Memory{}, false, fmt.Errorf("%w: %d", ErrInvalidImportance, in.Importance)
		}
		if in.Importance != current.Importance {
			updated.Importance = in.Importance
			changed = true
		}
	}
	body := strings.TrimSpace(in.Body)
	if body != "" {
		updated.Body = body
		updated.Hash = bodyHash(body)
		if updated.Hash != current.Hash {
			changed = true
		}
	}
	if !changed {
		return current, false, nil
	}
	updated.UpdatedAt = time.Now()
	if err := s.writeEntry(updated); err != nil {
		return Memory{}, false, err
	}
	if err := s.rebuildIndexLocked(); err != nil {
		return Memory{}, false, err
	}
	return updated, true, nil
}

// Archive 软删一条记忆：status 置为 archived，从索引移除，文件保留。
func (s *Store) Archive(ctx context.Context, fileName string) (Memory, error) {
	if err := ctx.Err(); err != nil {
		return Memory{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	mem, err := s.readEntry(fileName)
	if err != nil {
		return Memory{}, err
	}
	if mem.Status == StatusArchived {
		return mem, nil
	}
	mem.Status = StatusArchived
	mem.UpdatedAt = time.Now()
	if err := s.writeEntry(mem); err != nil {
		return Memory{}, err
	}
	if err := s.rebuildIndexLocked(); err != nil {
		return Memory{}, err
	}
	return mem, nil
}

// newMemory 校验并构造待落盘的记忆。
func newMemory(in EntryInput) (Memory, error) {
	name := collapseSpaces(in.Name)
	if name == "" {
		return Memory{}, ErrEmptyName
	}
	if utf8.RuneCountInString(name) > maxNameRunes {
		return Memory{}, fmt.Errorf("%w: name longer than %d runes", ErrEmptyName, maxNameRunes)
	}
	body := strings.TrimSpace(in.Body)
	if body == "" {
		return Memory{}, ErrEmptyBody
	}
	if in.Importance < 0 || in.Importance > 10 {
		return Memory{}, fmt.Errorf("%w: %d", ErrInvalidImportance, in.Importance)
	}
	description := collapseSpaces(in.Description)
	if description == "" {
		description = firstLine(body)
	}
	typ := strings.ToLower(strings.TrimSpace(in.Type))
	if typ == "" {
		typ = "project"
	}
	if !validType(typ) {
		return Memory{}, fmt.Errorf("%w: %q", ErrInvalidType, typ)
	}
	now := time.Now()
	return Memory{
		Name:           name,
		Description:    description,
		Type:           typ,
		Keywords:       normalizeKeywords(in.Keywords),
		Importance:     in.Importance,
		Sources:        normalizeKeywords(in.Sources),
		Body:           body,
		CreatedAt:      now,
		UpdatedAt:      now,
		LastAccessedAt: now,
		Status:         StatusActive,
		Hash:           bodyHash(body),
	}, nil
}

// writeEntry 把记忆序列化写入文件。调用方必须持有 s.mu。
func (s *Store) writeEntry(mem Memory) error {
	path := filepath.Join(s.dir, mem.FileName)
	if err := os.WriteFile(path, []byte(serializeMemory(mem)), 0o600); err != nil {
		return fmt.Errorf("memory: write %s: %w", path, err)
	}
	return nil
}

// rebuildIndexLocked 从 active 记忆全量重建索引。调用方必须持有 s.mu。
func (s *Store) rebuildIndexLocked() error {
	entries, err := s.list(context.Background(), false)
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# 记忆索引\n\n")
	for _, mem := range entries {
		b.WriteString("- [" + mem.Type + "] " + mem.Name)
		if mem.Description != "" {
			b.WriteString(" — " + mem.Description)
		}
		b.WriteString("\n")
	}
	path := filepath.Join(s.dir, IndexFileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("memory: write index %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("memory: replace index %s: %w", path, err)
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

// bodyHash 计算正文 md5 指纹。
func bodyHash(body string) string {
	sum := md5.Sum([]byte(body))
	return hex.EncodeToString(sum[:])
}

// serializeMemory 把记忆序列化成 frontmatter + 正文。字段顺序固定，
// 保证重复落盘时文件内容稳定（便于 diff 与同步）。
func serializeMemory(mem Memory) string {
	var b strings.Builder
	b.WriteString("---\n")
	writeField(&b, "name", mem.Name)
	writeField(&b, "description", mem.Description)
	writeField(&b, "type", mem.Type)
	if len(mem.Keywords) > 0 {
		writeList(&b, "keywords", mem.Keywords)
	}
	if mem.Importance > 0 {
		b.WriteString("importance: " + strconv.Itoa(mem.Importance) + "\n")
	}
	if len(mem.Sources) > 0 {
		writeList(&b, "sources", mem.Sources)
	}
	if !mem.CreatedAt.IsZero() {
		b.WriteString("createdAt: " + mem.CreatedAt.Format(time.RFC3339) + "\n")
	}
	if !mem.UpdatedAt.IsZero() {
		b.WriteString("updatedAt: " + mem.UpdatedAt.Format(time.RFC3339) + "\n")
	}
	if !mem.LastAccessedAt.IsZero() {
		b.WriteString("lastAccessedAt: " + mem.LastAccessedAt.Format(time.RFC3339) + "\n")
	}
	if mem.AccessCount > 0 {
		b.WriteString("accessCount: " + strconv.Itoa(mem.AccessCount) + "\n")
	}
	if mem.Status != "" && mem.Status != StatusActive {
		b.WriteString("status: " + mem.Status + "\n")
	}
	if mem.Hash != "" {
		b.WriteString("hash: " + mem.Hash + "\n")
	}
	b.WriteString("---\n\n")
	b.WriteString(mem.Body)
	b.WriteString("\n")
	return b.String()
}

// writeField 写一行 frontmatter 字段；含空白或特殊字符的值加双引号。
func writeField(b *strings.Builder, key, value string) {
	if value == "" {
		return
	}
	b.WriteString(key + ": " + yamlValue(value) + "\n")
}

// writeList 写一行 frontmatter 列表字段：key: ["a", "b"]。
func writeList(b *strings.Builder, key string, values []string) {
	if len(values) == 0 {
		return
	}
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = yamlValue(v)
	}
	b.WriteString(key + ": [" + strings.Join(quoted, ", ") + "]\n")
}

// yamlValue 把字符串格式化成安全的 YAML 标量：可打印且无空白的值原样
// 输出，其余值用双引号（Go 转义与 YAML 双引号转义兼容）。
func yamlValue(value string) string {
	if value == "" {
		return `""`
	}
	needsQuote := strings.ContainsAny(value, " \t\"'#:[]{}\n,") || !isPlainPrintable(value)
	if needsQuote {
		return strconv.Quote(value)
	}
	return value
}

// isPlainPrintable 报告字符串是否全部由可打印字符组成（不含控制字符
// 与零宽字符，零宽字符混进正文会让索引行不可见）。
func isPlainPrintable(value string) bool {
	for _, r := range value {
		if r == 0x200B || r == 0xFEFF {
			return false
		}
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// parseMemory 解析记忆文件的 frontmatter 与正文。ok 为 false 表示没有
// 合法 frontmatter（旧版或手写文件），此时 body 为原文。
func parseMemory(content string) (mem Memory, ok bool) {
	content = strings.TrimPrefix(content, "\ufeff")
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(strings.TrimSuffix(lines[0], "\r")) != "---" {
		return Memory{Body: content}, false
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(strings.TrimSuffix(lines[i], "\r")) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return Memory{Body: content}, false
	}
	for _, line := range lines[1:end] {
		line = strings.TrimSuffix(line, "\r")
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch key {
		case "name":
			mem.Name = parseScalar(value)
		case "description":
			mem.Description = parseScalar(value)
		case "type":
			mem.Type = strings.ToLower(parseScalar(value))
		case "keywords":
			mem.Keywords = parseList(value)
		case "sources":
			mem.Sources = parseList(value)
		case "importance":
			if n, err := strconv.Atoi(value); err == nil {
				mem.Importance = n
			}
		case "accesscount":
			if n, err := strconv.Atoi(value); err == nil {
				mem.AccessCount = n
			}
		case "status":
			mem.Status = parseScalar(value)
		case "hash":
			mem.Hash = parseScalar(value)
		case "createdat":
			if t, err := time.Parse(time.RFC3339, value); err == nil {
				mem.CreatedAt = t
			}
		case "updatedat":
			if t, err := time.Parse(time.RFC3339, value); err == nil {
				mem.UpdatedAt = t
			}
		case "lastaccessedat":
			if t, err := time.Parse(time.RFC3339, value); err == nil {
				mem.LastAccessedAt = t
			}
		}
	}
	// 正文从 frontmatter 结束符的下一行开始；跳过分隔空行，去掉尾部换行。
	bodyLines := lines[end+1:]
	if len(bodyLines) > 0 && strings.TrimSpace(bodyLines[0]) == "" {
		bodyLines = bodyLines[1:]
	}
	mem.Body = strings.TrimRight(strings.Join(bodyLines, "\n"), "\n")
	if mem.Status == "" {
		mem.Status = StatusActive
	}
	return mem, true
}

// parseScalar 解析 frontmatter 标量：带引号时去引号，否则原样返回。
func parseScalar(value string) string {
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		if unquoted, err := strconv.Unquote(value); err == nil {
			return unquoted
		}
	}
	return strings.Trim(value, `'"`)
}

// parseList 解析 frontmatter 列表：["a", "b"] 或 [a, b]。
func parseList(value string) []string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "[")
	value = strings.TrimSuffix(value, "]")
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if item := parseScalar(part); item != "" {
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
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

// normalizeKeywords 去空白、去重并限制数量。
func normalizeKeywords(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, dup := seen[value]; dup {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
		if len(out) >= 8 {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sameStrings 比较两个字符串切片作为集合是否相等。
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]struct{}, len(a))
	for _, item := range a {
		set[item] = struct{}{}
	}
	for _, item := range b {
		if _, ok := set[item]; !ok {
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

// truncateRunes 按字符数截断字符串。
func truncateRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "…"
}
