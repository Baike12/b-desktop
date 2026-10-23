package app

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Baike12/b-desktop/internal/agent"
	"github.com/Baike12/b-desktop/internal/hook"
)

// Hooks 绑定层的错误哨兵。
var (
	// ErrHookWorkDir 表示无法确定 hooks 配置对应的项目目录。
	ErrHookWorkDir = errors.New("app: hook work dir required")
	// ErrHooksUnavailable 表示 hooks 组件无法初始化或类型异常。
	ErrHooksUnavailable = errors.New("app: hooks unavailable")
	// ErrHookInvalid 表示事件、作用域或命令字段非法。
	ErrHookInvalid = errors.New("app: invalid hook")
)

// HookDTO 描述一条已加载的钩子配置。Command 是用户自己写入的 shell 命令，
// 前端展示即可，不要再拼接到其它命令里执行。
type HookDTO struct {
	// Event 是触发事件：PreToolUse / PostToolUse / UserPromptSubmit / SessionStart。
	Event string `json:"event"`
	// Matcher 是工具名匹配模式，空或 * 表示全部。
	Matcher string `json:"matcher"`
	// Command 是交给 shell 执行的命令。
	Command string `json:"command"`
	// Source 是配置来源：user 或 project。
	Source string `json:"source"`
}

// hooksComponent 返回某个工作目录对应的 agent.Hooks 实现。
// workDir 为空时使用应用默认工作目录；组件按工作目录缓存。
func (a *App) hooksComponent(workDir string) (agent.Hooks, error) {
	return a.hookManager(workDir)
}

// hookManager 返回 hooks 管理器本身，供绑定层读取条目与重新加载。
func (a *App) hookManager(workDir string) (*hook.Manager, error) {
	cfg, _ := a.configSnapshot()
	dir := strings.TrimSpace(workDir)
	if dir == "" {
		dir = cfg.WorkDir
	}
	if dir == "" {
		return nil, fmt.Errorf("%w: empty work dir", ErrHookWorkDir)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve work dir: %v", ErrHookWorkDir, err)
	}
	dir = abs

	key := "hooks:" + dir
	value, err := a.extensionStore(key, func() (any, error) {
		settings, err := a.settingsDir()
		if err != nil {
			return nil, err
		}
		return hook.New(hook.Config{
			WorkDir:  dir,
			UserPath: filepath.Join(settings, "hooks.json"),
		})
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrHooksUnavailable, err)
	}
	manager, ok := value.(*hook.Manager)
	if !ok {
		return nil, fmt.Errorf("%w: unexpected component type %T", ErrHooksUnavailable, value)
	}
	return manager, nil
}

// ListHooks 返回指定工作目录当前生效的钩子（用户级在前、项目级在后）。
// 只读取已加载的配置，不重新扫盘；需要读取磁盘最新内容时调用 ReloadHooks。
func (a *App) ListHooks(workDir string) ([]HookDTO, error) {
	manager, err := a.hookManager(workDir)
	if err != nil {
		return nil, err
	}
	return toHookDTOs(manager.Entries()), nil
}

// ReloadHooks 重新读取 ~/.b-desktop/hooks.json 与 <workDir>/.b-desktop/hooks.json，
// 返回替换后的钩子列表。解析失败时保持旧配置并返回错误。
func (a *App) ReloadHooks(workDir string) ([]HookDTO, error) {
	manager, err := a.hookManager(workDir)
	if err != nil {
		return nil, err
	}
	if err := manager.Reload(a.lifeCtx()); err != nil {
		return nil, fmt.Errorf("app: reload hooks: %w", err)
	}
	return toHookDTOs(manager.Entries()), nil
}

// HookInput 是新增或修改一条钩子的入参。
// Original* 为空表示新增；非空时先按原三元组删除旧条目，再写入新条目。
// Scope 与 OriginalScope 取 user 或 project。
type HookInput struct {
	Event           string `json:"event"`
	Matcher         string `json:"matcher"`
	Command         string `json:"command"`
	Scope           string `json:"scope"`
	OriginalEvent   string `json:"originalEvent"`
	OriginalMatcher string `json:"originalMatcher"`
	OriginalCommand string `json:"originalCommand"`
	OriginalScope   string `json:"originalScope"`
}

// SaveHook 新增或修改一条钩子配置，写盘后重新加载并返回最新列表。
func (a *App) SaveHook(workDir string, in HookInput) ([]HookDTO, error) {
	manager, err := a.hookManager(workDir)
	if err != nil {
		return nil, err
	}
	event := strings.TrimSpace(in.Event)
	if !hook.ValidEvent(event) {
		return nil, fmt.Errorf("%w: unsupported event %q", ErrHookInvalid, event)
	}
	scope := normalizeHookScope(in.Scope)
	if scope == "" {
		return nil, fmt.Errorf("%w: unknown scope %q", ErrHookInvalid, in.Scope)
	}
	command := strings.TrimSpace(in.Command)
	if command == "" {
		return nil, fmt.Errorf("%w: empty command", ErrHookInvalid)
	}
	matcher := strings.TrimSpace(in.Matcher)

	userPath, projectPath := manager.Paths()
	targetPath, err := hookScopePath(scope, userPath, projectPath)
	if err != nil {
		return nil, err
	}

	// 编辑时先从原作用域文件删除旧条目，再写入新条目。
	if originalEvent := strings.TrimSpace(in.OriginalEvent); originalEvent != "" {
		originalScope := normalizeHookScope(in.OriginalScope)
		if originalScope == "" {
			originalScope = scope
		}
		originalPath, perr := hookScopePath(originalScope, userPath, projectPath)
		if perr != nil {
			return nil, perr
		}
		if derr := removeHookEntry(originalPath, originalEvent, strings.TrimSpace(in.OriginalMatcher), strings.TrimSpace(in.OriginalCommand)); derr != nil {
			return nil, derr
		}
	}

	entries, err := hook.ReadEntries(targetPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrHookInvalid, err)
	}
	byEvent := entries[hook.Event(event)]
	replaced := false
	for i := range byEvent {
		if byEvent[i].Matcher == matcher && byEvent[i].Command == command {
			replaced = true
			break
		}
	}
	if !replaced {
		entries[hook.Event(event)] = append(byEvent, hook.FileEntry{Matcher: matcher, Command: command})
	}
	if err := hook.WriteEntries(targetPath, entries); err != nil {
		return nil, fmt.Errorf("app: save hook: %w", err)
	}
	if err := manager.Reload(a.lifeCtx()); err != nil {
		return nil, fmt.Errorf("app: reload hooks: %w", err)
	}
	return toHookDTOs(manager.Entries()), nil
}

// DeleteHook 删除一条钩子配置，写盘后重新加载并返回最新列表。
func (a *App) DeleteHook(workDir, scope, event, matcher, command string) ([]HookDTO, error) {
	manager, err := a.hookManager(workDir)
	if err != nil {
		return nil, err
	}
	scope = normalizeHookScope(scope)
	if scope == "" {
		return nil, fmt.Errorf("%w: unknown scope %q", ErrHookInvalid, scope)
	}
	if !hook.ValidEvent(event) {
		return nil, fmt.Errorf("%w: unsupported event %q", ErrHookInvalid, event)
	}
	userPath, projectPath := manager.Paths()
	path, err := hookScopePath(scope, userPath, projectPath)
	if err != nil {
		return nil, err
	}
	if err := removeHookEntry(path, event, strings.TrimSpace(matcher), strings.TrimSpace(command)); err != nil {
		return nil, err
	}
	if err := manager.Reload(a.lifeCtx()); err != nil {
		return nil, fmt.Errorf("app: reload hooks: %w", err)
	}
	return toHookDTOs(manager.Entries()), nil
}

// normalizeHookScope 把作用域字符串归一化为 user/project，未知值返回空串。
func normalizeHookScope(scope string) string {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case hook.SourceUser:
		return hook.SourceUser
	case hook.SourceProject:
		return hook.SourceProject
	}
	return ""
}

// hookScopePath 返回作用域对应的配置文件路径。
func hookScopePath(scope, userPath, projectPath string) (string, error) {
	switch scope {
	case hook.SourceUser:
		return userPath, nil
	case hook.SourceProject:
		return projectPath, nil
	}
	return "", fmt.Errorf("%w: unknown scope %q", ErrHookInvalid, scope)
}

// removeHookEntry 从文件中删除与三元组完全匹配的条目；条目不存在时视为成功。
func removeHookEntry(path, event, matcher, command string) error {
	entries, err := hook.ReadEntries(path)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrHookInvalid, err)
	}
	ev := hook.Event(event)
	items := entries[ev]
	kept := make([]hook.FileEntry, 0, len(items))
	for _, item := range items {
		if item.Matcher == matcher && item.Command == command {
			continue
		}
		kept = append(kept, item)
	}
	if len(kept) == len(items) {
		return nil
	}
	if len(kept) == 0 {
		delete(entries, ev)
	} else {
		entries[ev] = kept
	}
	if err := hook.WriteEntries(path, entries); err != nil {
		return fmt.Errorf("app: save hook: %w", err)
	}
	return nil
}

// toHookDTOs 把领域条目转换为绑定层 DTO。
func toHookDTOs(entries []hook.Entry) []HookDTO {
	out := make([]HookDTO, 0, len(entries))
	for _, e := range entries {
		out = append(out, HookDTO{
			Event:   string(e.Event),
			Matcher: e.Matcher,
			Command: e.Command,
			Source:  e.Source,
		})
	}
	return out
}
