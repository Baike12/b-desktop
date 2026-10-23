// Package skill 加载磁盘上的 SKILL.md 技能定义，并按渐进披露原则向 Agent 暴露：
// 上下文里只注入「名称 + 描述」清单，正文由 Skill 工具按需读取。
//
// 目录约定与参考实现（../cc/src/skills/loadSkillsDir.ts、../ZCode
// packages/services/src/skills/skillDiscoveryWalk.ts）一致：每个技能是
// 一个包含 SKILL.md 的子目录。本包只识别两层来源：
//
//	~/.b-desktop/skills/<name>/SKILL.md        用户级
//	<workDir>/.b-desktop/skills/<name>/SKILL.md 项目级
//
// 同名技能由项目级覆盖用户级（越靠近工作目录优先级越高）。
package skill

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

// SkillFileName 是技能定义文件名。
const SkillFileName = "SKILL.md"

// maxContextSkills 是注入系统提示的技能条数上限，避免清单挤占上下文。
const maxContextSkills = 50

// maxDescriptionRunes 是自动提取描述时的最大字符数。
const maxDescriptionRunes = 160

// 技能来源标识。
const (
	// SourceUser 表示用户级目录 ~/.b-desktop/skills。
	SourceUser = "user"
	// SourceProject 表示项目级目录 <workDir>/.b-desktop/skills。
	SourceProject = "project"
)

// 错误哨兵。
var (
	// ErrSkillNotFound 表示指定名称的技能不存在。
	ErrSkillNotFound = errors.New("skill: not found")
	// ErrEmptyName 表示技能名为空。
	ErrEmptyName = errors.New("skill: empty name")
	// ErrSkillExists 表示同名技能已经存在。
	ErrSkillExists = errors.New("skill: already exists")
	// ErrSkillPath 表示技能文件不在受管理的技能目录内。
	ErrSkillPath = errors.New("skill: path outside managed dirs")
)

// Skill 是一个已加载的技能定义（不含正文）。
type Skill struct {
	// Name 是技能名，来自 frontmatter 的 name，缺省时取目录名。
	Name string
	// Description 是技能用途描述，来自 frontmatter 的 description。
	Description string
	// Path 是 SKILL.md 的绝对路径。
	Path string
	// Source 是来源标识：user 或 project。
	Source string
}

// Loader 加载并持有技能清单，可重复 Reload。
// 所有导出方法并发安全；正文不缓存，每次调用 Skill 工具时读取磁盘。
type Loader struct {
	mu     sync.RWMutex
	dirs   []string
	source []string
	skills map[string]Skill
}

// NewLoader 加载用户级与项目级技能目录。
// workDir 为空时只加载用户级目录；目录不存在不视为错误。
func NewLoader(workDir string) (*Loader, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("skill: resolve home dir: %w", err)
	}
	userDir := filepath.Join(home, ".b-desktop", "skills")
	dirs := []string{userDir}
	sources := []string{SourceUser}
	if strings.TrimSpace(workDir) != "" {
		abs, err := filepath.Abs(workDir)
		if err != nil {
			return nil, fmt.Errorf("skill: resolve work dir %q: %w", workDir, err)
		}
		dirs = append(dirs, filepath.Join(abs, ".b-desktop", "skills"))
		sources = append(sources, SourceProject)
	}
	return NewLoaderAt(dirs, sources)
}

// NewLoaderAt 用显式目录创建加载器。dirs 与 sources 一一对应；
// 项目级（source=project）始终覆盖用户级，同一来源内保留先出现的技能。
func NewLoaderAt(dirs, sources []string) (*Loader, error) {
	if len(dirs) != len(sources) {
		return nil, fmt.Errorf("skill: dirs and sources length mismatch: %d != %d", len(dirs), len(sources))
	}
	l := &Loader{
		dirs:   append([]string(nil), dirs...),
		source: append([]string(nil), sources...),
		skills: make(map[string]Skill),
	}
	if err := l.Reload(); err != nil {
		return nil, err
	}
	return l, nil
}

// Reload 重新扫描全部目录。单个不可读的技能文件被跳过；
// 目录本身不存在视为空目录。
func (l *Loader) Reload() error {
	loaded := make(map[string]Skill)
	for i, dir := range l.dirs {
		source := SourceUser
		if i < len(l.source) {
			source = l.source[i]
		}
		if err := scanDir(dir, source, loaded); err != nil {
			return err
		}
	}
	l.mu.Lock()
	l.skills = loaded
	l.mu.Unlock()
	return nil
}

// scanDir 扫描 dir 下的直接子目录，把有效技能写入 into。
func scanDir(dir, source string, into map[string]Skill) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("skill: read dir %s: %w", dir, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name(), SkillFileName)
		data, err := os.ReadFile(path)
		if err != nil {
			// 子目录没有 SKILL.md 是常态；其他读取错误也不该让整个清单失效。
			continue
		}
		name, description, _ := parseFrontmatter(string(data))
		if strings.TrimSpace(name) == "" {
			name = entry.Name()
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if exists, ok := into[name]; ok {
			// 同来源重复保留先出现的；项目级始终覆盖用户级。
			if exists.Source == SourceProject || source != SourceProject {
				continue
			}
		}
		into[name] = Skill{
			Name:        name,
			Description: strings.TrimSpace(description),
			Path:        path,
			Source:      source,
		}
	}
	return nil
}

// List 返回按名称排序的技能清单（不含正文）。
func (l *Loader) List() []Skill {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Skill, 0, len(l.skills))
	for _, s := range l.skills {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Lookup 按名称查找技能。
func (l *Loader) Lookup(name string) (Skill, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	s, ok := l.skills[strings.TrimSpace(name)]
	return s, ok
}

// Body 返回技能正文（去掉 frontmatter）。技能不存在时返回 ErrSkillNotFound。
func (l *Loader) Body(ctx context.Context, name string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", ErrEmptyName
	}
	s, ok := l.Lookup(name)
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrSkillNotFound, name)
	}
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return "", fmt.Errorf("skill: read %s: %w", s.Path, err)
	}
	_, _, body := parseFrontmatter(string(data))
	return strings.TrimSpace(body), nil
}

// ExtraContext 把可用技能清单（最多 50 条）追加到系统提示；正文不注入。
// 没有技能时返回空字符串。
func (l *Loader) ExtraContext(ctx context.Context, sessionID, workDir, prompt string) string {
	if err := ctx.Err(); err != nil {
		return ""
	}
	skills := l.List()
	if len(skills) == 0 {
		return ""
	}
	if len(skills) > maxContextSkills {
		skills = skills[:maxContextSkills]
	}
	var b strings.Builder
	b.WriteString("## 可用技能\n")
	b.WriteString("需要某个技能的完整说明时，调用 Skill 工具并传入技能名。以下只有清单，正文按需加载。\n")
	for _, s := range skills {
		b.WriteString("- ")
		b.WriteString(s.Name)
		if s.Description != "" {
			b.WriteString(": ")
			b.WriteString(s.Description)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// parseFrontmatter 解析文件开头的 --- 块，只识别 name 与 description 两行。
// 返回解析到的字段、正文以及是否找到 frontmatter。
func parseFrontmatter(content string) (name, description, body string) {
	content = strings.TrimPrefix(content, "\ufeff")
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(strings.TrimSuffix(lines[0], "\r")) != "---" {
		return "", inlineDescription(content), content
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(strings.TrimSuffix(lines[i], "\r")) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		// frontmatter 未闭合：按无 frontmatter 处理，避免吞掉正文。
		return "", inlineDescription(strings.Join(lines[1:], "\n")), content
	}
	rest := lines[end+1:]
	for _, line := range lines[1:end] {
		line = strings.TrimSuffix(line, "\r")
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "name":
			name = unquote(strings.TrimSpace(value))
		case "description":
			description = unquote(strings.TrimSpace(value))
		}
	}
	body = strings.Join(rest, "\n")
	if strings.TrimSpace(description) == "" {
		description = inlineDescription(body)
	}
	return name, description, body
}

// inlineDescription 从正文第一段非标题文本提取描述，作为 frontmatter 缺失时的兜底。
func inlineDescription(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "```") {
			continue
		}
		return truncateRunes(line, maxDescriptionRunes)
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

// UserDir 返回用户级技能目录；Loader 没有目录时返回空串。
func (l *Loader) UserDir() string {
	if len(l.dirs) == 0 {
		return ""
	}
	return l.dirs[0]
}

// Create 在用户级目录新建一个技能并重新扫描。
// 同名技能已存在时返回 ErrSkillExists；目录名由技能名派生，冲突时自动加后缀。
func (l *Loader) Create(name, description, content string) (Skill, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Skill{}, ErrEmptyName
	}
	if _, exists := l.Lookup(name); exists {
		return Skill{}, fmt.Errorf("%w: %q", ErrSkillExists, name)
	}
	root := l.UserDir()
	if root == "" {
		return Skill{}, errors.New("skill: no user skill dir configured")
	}
	dirName := skillDirName(name)
	target := filepath.Join(root, dirName)
	for suffix := 2; ; suffix++ {
		if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
			break
		}
		if suffix > 99 {
			return Skill{}, fmt.Errorf("skill: cannot allocate dir for %q", name)
		}
		target = filepath.Join(root, fmt.Sprintf("%s-%d", dirName, suffix))
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return Skill{}, fmt.Errorf("skill: create dir: %w", err)
	}
	if err := writeSkillFile(filepath.Join(target, SkillFileName), name, description, content); err != nil {
		_ = os.RemoveAll(target)
		return Skill{}, err
	}
	if err := l.Reload(); err != nil {
		return Skill{}, err
	}
	s, ok := l.Lookup(name)
	if !ok {
		return Skill{}, fmt.Errorf("%w: %q after create", ErrSkillNotFound, name)
	}
	return s, nil
}

// Update 覆盖指定技能的描述与正文，技能名保持不变。
func (l *Loader) Update(name, description, content string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return ErrEmptyName
	}
	s, ok := l.Lookup(name)
	if !ok {
		return fmt.Errorf("%w: %q", ErrSkillNotFound, name)
	}
	if err := writeSkillFile(s.Path, s.Name, description, content); err != nil {
		return err
	}
	return l.Reload()
}

// Delete 删除指定技能所在的目录并重新扫描。
// 只允许删除受管理目录下的子目录，拒绝删除用户级或项目级根目录本身。
func (l *Loader) Delete(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return ErrEmptyName
	}
	s, ok := l.Lookup(name)
	if !ok {
		return fmt.Errorf("%w: %q", ErrSkillNotFound, name)
	}
	dir := filepath.Dir(s.Path)
	if !l.managedDir(dir) {
		return fmt.Errorf("%w: %q", ErrSkillPath, dir)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("skill: remove %q: %w", dir, err)
	}
	return l.Reload()
}

// managedDir 报告 dir 是否是某个扫描目录的真子目录。
func (l *Loader) managedDir(dir string) bool {
	for _, root := range l.dirs {
		rel, err := filepath.Rel(root, dir)
		if err != nil || rel == "." {
			continue
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		return true
	}
	return false
}

// skillDirName 把技能名转换成安全的目录名，保留中英文与数字。
func skillDirName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		case r > 127:
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-._")
	if out == "" || out == "." || out == ".." {
		out = "skill"
	}
	if utf8.RuneCountInString(out) > 64 {
		out = string([]rune(out)[:64])
	}
	return out
}

// writeSkillFile 原子写入一份 SKILL.md（0644）。
func writeSkillFile(path, name, description, content string) error {
	desc := strings.ReplaceAll(strings.TrimSpace(description), "\n", " ")
	body := strings.TrimSpace(content)
	text := "---\nname: " + strings.TrimSpace(name) + "\ndescription: " + desc + "\n---\n\n" + body + "\n"
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".skill-*.tmp")
	if err != nil {
		return fmt.Errorf("skill: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("skill: chmod temp file: %w", err)
	}
	if _, err := tmp.WriteString(text); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("skill: write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("skill: sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("skill: close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("skill: replace file: %w", err)
	}
	return nil
}
