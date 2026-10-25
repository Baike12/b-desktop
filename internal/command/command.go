// Package command 加载独立于技能的 slash 命令定义，与参考实现（../cc/src
// 的自定义命令、../ZCode 的 commands）对齐：命令是散置的 .md 文件，
// 不要求「一个目录一个 SKILL.md」的技能布局。
//
//	~/.b-desktop/commands/*.md           用户级
//	<workDir>/.b-desktop/commands/*.md   项目级
//
// 文件名（去 .md）即命令名；一层子目录构成命名空间（sub/cmd.md → "sub:cmd"）。
// frontmatter 提供 description 与 argument-hint（allowed-tools 解析后保留备用）。
// 同名命令项目级覆盖用户级；正文经 / 调用展开进消息，与技能共用一条管线。
package command

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/Baike12/b-desktop/internal/skill"
)

// 命令来源标识。
const (
	// SourceUser 表示用户级目录 ~/.b-desktop/commands。
	SourceUser = "user"
	// SourceProject 表示项目级目录 <workDir>/.b-desktop/commands。
	SourceProject = "project"
)

// 错误哨兵。
var (
	// ErrCommandNotFound 表示指定名称的命令不存在。
	ErrCommandNotFound = errors.New("command: not found")
	// ErrEmptyName 表示命令名为空。
	ErrEmptyName = errors.New("command: empty name")
)

// Command 是一个已加载的命令定义（不含正文）。
type Command struct {
	// Name 是命令名：文件名去 .md，子目录命令形如 "ns:cmd"。
	Name string
	// Description 是用途描述，来自 frontmatter 的 description。
	Description string
	// ArgumentHint 是 / 面板展示的参数提示，来自 frontmatter 的 argument-hint。
	ArgumentHint string
	// Path 是 .md 文件的绝对路径。
	Path string
	// Source 是来源标识：user 或 project。
	Source string
}

// Loader 加载并持有命令清单，可重复 Reload。所有导出方法并发安全；
// 正文不缓存，每次展开时读取磁盘。
type Loader struct {
	mu       sync.RWMutex
	dirs     []string
	sources  []string
	commands map[string]Command
}

// NewLoader 加载用户级与项目级命令目录。
// workDir 为空时只加载用户级目录；目录不存在不视为错误。
func NewLoader(workDir string) (*Loader, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("command: resolve home dir: %w", err)
	}
	dirs := []string{filepath.Join(home, ".b-desktop", "commands")}
	sources := []string{SourceUser}
	if strings.TrimSpace(workDir) != "" {
		abs, err := filepath.Abs(workDir)
		if err != nil {
			return nil, fmt.Errorf("command: resolve work dir %q: %w", workDir, err)
		}
		dirs = append(dirs, filepath.Join(abs, ".b-desktop", "commands"))
		sources = append(sources, SourceProject)
	}
	return NewLoaderAt(dirs, sources)
}

// NewLoaderAt 用显式目录创建加载器。dirs 与 sources 一一对应；
// 项目级（source=project）始终覆盖用户级，同一来源内保留先出现的命令。
func NewLoaderAt(dirs, sources []string) (*Loader, error) {
	if len(dirs) != len(sources) {
		return nil, fmt.Errorf("command: dirs and sources length mismatch: %d != %d", len(dirs), len(sources))
	}
	l := &Loader{
		dirs:     append([]string(nil), dirs...),
		sources:  append([]string(nil), sources...),
		commands: make(map[string]Command),
	}
	if err := l.Reload(); err != nil {
		return nil, err
	}
	return l, nil
}

// Reload 重新扫描全部目录。不可读的文件被跳过；目录不存在视为空。
func (l *Loader) Reload() error {
	loaded := make(map[string]Command)
	for i, dir := range l.dirs {
		source := SourceUser
		if i < len(l.sources) {
			source = l.sources[i]
		}
		if err := scanDir(dir, source, loaded); err != nil {
			return err
		}
	}
	l.mu.Lock()
	l.commands = loaded
	l.mu.Unlock()
	return nil
}

// scanDir 扫描 dir 下的 .md 文件与一层子目录（命名空间）。
func scanDir(dir, source string, into map[string]Command) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("command: read dir %s: %w", dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if err := scanNamespace(filepath.Join(dir, entry.Name()), entry.Name(), source, into); err != nil {
				return err
			}
			continue
		}
		if strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			loadCommandFile(filepath.Join(dir, entry.Name()), commandFileName(entry.Name()), source, into)
		}
	}
	return nil
}

// scanNamespace 扫描命名空间子目录下的一层 .md 文件，命令名形如 "ns:cmd"。
func scanNamespace(dir, namespace, source string, into map[string]Command) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("command: read dir %s: %w", dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			continue
		}
		name := namespace + ":" + commandFileName(entry.Name())
		loadCommandFile(filepath.Join(dir, entry.Name()), name, source, into)
	}
	return nil
}

// commandFileName 把文件名转成命令名：去扩展名，保留其余字符。
func commandFileName(filename string) string {
	return strings.TrimSuffix(filename, filepath.Ext(filename))
}

// loadCommandFile 读取一个命令文件并写入 into；无效或同名冲突按优先级规则处理。
func loadCommandFile(path, name, source string, into map[string]Command) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	cmdName, description, argumentHint, _ := skill.ParseFrontmatter(string(data))
	if strings.TrimSpace(name) == "" && strings.TrimSpace(cmdName) == "" {
		return
	}
	// 命令名以文件名为准；frontmatter 的 name 只在文件名不可用时兜底。
	if strings.TrimSpace(name) == "" {
		name = strings.TrimSpace(cmdName)
	}
	if exists, ok := into[name]; ok {
		// 同来源重复保留先出现的；项目级始终覆盖用户级。
		if exists.Source == SourceProject || source != SourceProject {
			return
		}
	}
	into[name] = Command{
		Name:         name,
		Description:  strings.TrimSpace(description),
		ArgumentHint: strings.TrimSpace(argumentHint),
		Path:         path,
		Source:       source,
	}
}

// List 返回按名称排序的命令清单（不含正文）。
func (l *Loader) List() []Command {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Command, 0, len(l.commands))
	for _, c := range l.commands {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Lookup 按名称查找命令。
func (l *Loader) Lookup(name string) (Command, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	c, ok := l.commands[strings.TrimSpace(name)]
	return c, ok
}

// Body 返回命令正文（去掉 frontmatter）。命令不存在时返回 ErrCommandNotFound。
func (l *Loader) Body(ctx context.Context, name string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", ErrEmptyName
	}
	c, ok := l.Lookup(name)
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrCommandNotFound, name)
	}
	data, err := os.ReadFile(c.Path)
	if err != nil {
		return "", fmt.Errorf("command: read %s: %w", c.Path, err)
	}
	_, _, _, body := skill.ParseFrontmatter(string(data))
	return strings.TrimSpace(body), nil
}
