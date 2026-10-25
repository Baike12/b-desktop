package app

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Baike12/b-desktop/internal/llm"
)

// SlashCommandDTO 是 / 面板的一个候选命令。
type SlashCommandDTO struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	ArgumentHint string `json:"argumentHint"`
	// Source 是命令来源：builtin 表示内置本地命令，skill 表示技能。
	Source string `json:"source"`
}

// builtinSlashCommands 是不进入模型消息、由前端本地执行的命令。
// /compact 交给 CompactSession 绑定；/clear、/model、/export 由前端 send 拦截实现。
var builtinSlashCommands = []SlashCommandDTO{
	{Name: "compact", Description: "压缩当前会话上下文，生成摘要并保留最近消息", Source: "builtin"},
	{Name: "clear", Description: "清空当前对话，在同一项目下开始新会话", Source: "builtin"},
	{Name: "model", Description: "查看或切换模型：/model <模型名>；不带参数打开设置", Source: "builtin"},
	{Name: "export", Description: "导出当前会话为 Markdown 文件并复制到剪贴板", Source: "builtin"},
}

// builtinCommandNames 是内置本地命令名集合：展开与本地拦截共用。
var builtinCommandNames = map[string]bool{
	"compact": true,
	"clear":   true,
	"model":   true,
	"export":  true,
}

// slashCommandNameRe 校验 / 后第一个 token 形如命令名（与参考实现一致的字符集）。
var slashCommandNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.\-]*$`)

// positionalArgsRe 匹配正文里的位置参数占位符 $1..$9。
var positionalArgsRe = regexp.MustCompile(`\$[1-9]`)

// ListSlashCommands 返回 / 面板候选：内置命令 + 命令目录 + 指定工作目录的技能。
// workDir 为空时使用应用默认工作目录。
func (a *App) ListSlashCommands(workDir string) ([]SlashCommandDTO, error) {
	out := append([]SlashCommandDTO(nil), builtinSlashCommands...)
	loader, err := a.skillsComponent(workDir)
	if err != nil {
		return nil, err
	}
	for _, s := range loader.List() {
		if builtinCommandNames[s.Name] {
			continue
		}
		out = append(out, SlashCommandDTO{
			Name:         s.Name,
			Description:  s.Description,
			ArgumentHint: s.ArgumentHint,
			Source:       "skill",
		})
	}
	commands, err := a.commandsComponent(workDir)
	if err != nil {
		return nil, err
	}
	for _, c := range commands.List() {
		out = append(out, SlashCommandDTO{
			Name:         c.Name,
			Description:  c.Description,
			ArgumentHint: c.ArgumentHint,
			Source:       "command",
		})
	}
	return out, nil
}

// expandSlashCommand 解析以 / 开头的 prompt。命中命令目录或技能时返回展开后的
// 附件块（<command-name> 面包屑 + 参数替换后的正文，包在 system-reminder 里），
// 与参考实现的消息组装一致：用户原文保留，展开正文追加在原文本块之后。
// 未命中（含内置本地命令）返回 nil，调用方按普通文本发送。
// 命令目录优先于技能：同名时 .md 命令覆盖 SKILL.md 技能。
func (a *App) expandSlashCommand(workDir, prompt string) []llm.ContentBlock {
	prompt = strings.TrimSpace(prompt)
	if !strings.HasPrefix(prompt, "/") {
		return nil
	}
	name, args, _ := strings.Cut(strings.TrimPrefix(prompt, "/"), " ")
	name = strings.TrimSpace(name)
	if !slashCommandNameRe.MatchString(name) || builtinCommandNames[name] {
		return nil
	}
	body, ok, err := a.slashCommandBody(workDir, name)
	if err != nil || !ok {
		return nil
	}
	body = substituteSkillArguments(body, args)
	reminder := fmt.Sprintf(
		"<system-reminder>\n<command-name>/%s</command-name>\n<command-message>%s</command-message>\n<command-args>%s</command-args>\n\n%s\n</system-reminder>",
		name, name, args, body,
	)
	return []llm.ContentBlock{{Type: llm.BlockText, Text: reminder}}
}

// slashCommandBody 查找命令正文：先命令目录，再技能目录。
// 第二个返回值报告是否命中。
func (a *App) slashCommandBody(workDir, name string) (string, bool, error) {
	if commands, err := a.commandsComponent(workDir); err == nil {
		if _, ok := commands.Lookup(name); ok {
			body, err := commands.Body(a.lifeCtx(), name)
			return body, err == nil, err
		}
	}
	loader, err := a.skillsComponent(workDir)
	if err != nil {
		return "", false, err
	}
	if _, ok := loader.Lookup(name); !ok {
		return "", false, nil
	}
	body, err := loader.Body(a.lifeCtx(), name)
	return body, err == nil, err
}

// substituteSkillArguments 把命令参数替换进技能正文，占位符语义与参考实现一致：
// $ARGUMENTS 是整串参数，$1..$9 是按空白（支持双引号）拆分的位置参数；
// 正文不含任何占位符而用户带了参数时，把参数追加到正文尾部。
func substituteSkillArguments(body, args string) string {
	if strings.TrimSpace(args) == "" {
		return body
	}
	parts := splitCommandArgs(args)
	replaced := body
	if strings.Contains(replaced, "$ARGUMENTS") {
		replaced = strings.ReplaceAll(replaced, "$ARGUMENTS", args)
	}
	for i, part := range parts {
		if i >= 9 {
			break
		}
		replaced = strings.ReplaceAll(replaced, fmt.Sprintf("$%d", i+1), part)
	}
	if !strings.Contains(body, "$ARGUMENTS") && !positionalArgsRe.MatchString(body) {
		replaced += "\n\nARGUMENTS: " + args
	}
	return replaced
}

// splitCommandArgs 按空白拆分参数串，双引号段算作一个参数。
func splitCommandArgs(args string) []string {
	var (
		out      []string
		current  strings.Builder
		inQuote  bool
		hasToken bool
	)
	for _, r := range args {
		switch {
		case r == '"':
			inQuote = !inQuote
			hasToken = true
		case r == ' ' || r == '\t':
			if inQuote {
				current.WriteRune(r)
				continue
			}
			if hasToken {
				out = append(out, current.String())
				current.Reset()
				hasToken = false
			}
		default:
			current.WriteRune(r)
			hasToken = true
		}
	}
	if hasToken {
		out = append(out, current.String())
	}
	return out
}
