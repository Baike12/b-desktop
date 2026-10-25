package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/Baike12/b-desktop/internal/llm"
	"github.com/Baike12/b-desktop/internal/tool/builtin"
	"github.com/Baike12/b-desktop/internal/workspace"
)

// atMentionRe 匹配用户消息里的 @ 引用：行首或空白后的 @token；
// token 支持无空格路径或双引号包裹的含空格路径。
var atMentionRe = regexp.MustCompile(`(?:^|\s)@("(?:[^"\\]|\\.)*"|[^\s]+)`)

// maxAtMentions 是单条消息允许的 @ 引用上限，防止一次性灌入过多内容。
const maxAtMentions = 10

// expandAtMentions 解析 prompt 中的 @path 引用，把文件内容读入为附件块。
// 与参考实现的消息组装一致：用户原文不动，内容以 system-reminder 包裹的
// 伪造工具对（Read / 目录列举）追加在原文本块之后；目录渲染为条目清单，
// 图片文件走 Read 工具的图片路径转为图片块；引用不存在时注入提示块，
// 由模型决定下一步。
func (a *App) expandAtMentions(workDir, prompt string) []llm.ContentBlock {
	matches := atMentionRe.FindAllStringSubmatch(prompt, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(matches))
	blocks := make([]llm.ContentBlock, 0, len(matches))
	for _, match := range matches {
		if len(blocks) >= maxAtMentions {
			break
		}
		token := strings.TrimSpace(match[1])
		rel := unquoteAtMention(token)
		if rel == "" || seen[rel] {
			continue
		}
		seen[rel] = true
		blocks = append(blocks, a.atMentionBlock(workDir, rel)...)
	}
	return blocks
}

// unquoteAtMention 去掉 @token 的成对双引号并处理转义。
func unquoteAtMention(token string) string {
	if len(token) >= 2 && token[0] == '"' && token[len(token)-1] == '"' {
		token = token[1 : len(token)-1]
		return strings.ReplaceAll(token, `\"`, `"`)
	}
	return token
}

// atMentionBlock 解析单个引用为附件块。目录列出条目清单；文件交给内置
// Read 工具（复用它的路径安全解析、行号渲染与图片缩放）；两者都不成立时
// 注入「引用不存在」提示。
func (a *App) atMentionBlock(workDir, rel string) []llm.ContentBlock {
	root, err := a.resolveWorkRoot(workDir)
	if err != nil {
		return nil
	}
	ctx := a.lifeCtx()
	list, listErr := workspace.ListDir(ctx, root, rel)
	switch {
	case listErr == nil:
		return []llm.ContentBlock{{Type: llm.BlockText, Text: directoryReminder(rel, list)}}
	case errors.Is(listErr, workspace.ErrNotDirectory):
		return a.readFileMention(root, rel)
	default:
		// 不存在或路径非法：交给 Read 工具产出统一的错误文案。
		return a.readFileMention(root, rel)
	}
}

// resolveWorkRoot 解析 @ 引用的工作区根目录，与文件面板共用校验规则。
func (a *App) resolveWorkRoot(workDir string) (string, error) {
	root := strings.TrimSpace(workDir)
	if root == "" {
		root = a.cfg.WorkDir
	}
	if root == "" {
		return "", ErrWorkDirUnavailable
	}
	return root, nil
}

// readFileMention 用内置 Read 工具读取一个 @ 引用的文件，渲染成伪造工具对。
func (a *App) readFileMention(root, rel string) []llm.ContentBlock {
	input, err := json.Marshal(map[string]string{"file_path": rel})
	if err != nil {
		return nil
	}
	result, runErr := builtin.NewRead(root).Run(a.lifeCtx(), input, nil)
	if runErr != nil || result.IsError {
		message := result.Content
		if runErr != nil {
			message = runErr.Error()
		}
		return []llm.ContentBlock{{Type: llm.BlockText, Text: fmt.Sprintf(
			"<system-reminder>\nReferenced path not readable: %s\n%s\n</system-reminder>", rel, message,
		)}}
	}
	out := []llm.ContentBlock{{Type: llm.BlockText, Text: fmt.Sprintf(
		"<system-reminder>\nCalled the \"Read\" tool with the following input: {\"file_path\": %q}\n\nResult of calling the \"Read\" tool:\n%s\n</system-reminder>",
		rel, result.Content,
	)}}
	for _, img := range result.Images {
		if mediaType, data, err := llm.NormalizeImageInput(img.MediaType, img.Data); err == nil {
			out = append(out, llm.ImageBlock(mediaType, data))
		}
	}
	return out
}

// directoryReminder 把目录引用渲染成伪造的 Bash ls 工具对（与参考实现一致），
// 条目超过 workspace.MaxDirEntries 时附截断说明。
func directoryReminder(rel string, list workspace.FileList) string {
	var b strings.Builder
	b.WriteString("<system-reminder>\n")
	b.WriteString(fmt.Sprintf("Called the \"Bash\" tool with the following input: {\"command\": %q, \"description\": %q}\n\n",
		"ls "+rel, "Lists files in "+rel))
	b.WriteString("Result of calling the \"Bash\" tool:\n")
	for _, entry := range list.Entries {
		name := entry.Name
		if entry.IsDir {
			name += "/"
		}
		b.WriteString(name)
		b.WriteString("\n")
	}
	if list.Truncated {
		b.WriteString(fmt.Sprintf("… and more entries (limit %d reached)\n", workspace.MaxDirEntries))
	}
	b.WriteString("</system-reminder>")
	return b.String()
}
