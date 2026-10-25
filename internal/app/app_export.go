package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Baike12/b-desktop/internal/llm"
	"github.com/Baike12/b-desktop/internal/session"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ExportResultDTO 是导出结果。
type ExportResultDTO struct {
	// Path 是写出的 Markdown 文件绝对路径。
	Path string `json:"path"`
	// MessageCount 是导出的消息数。
	MessageCount int `json:"messageCount"`
	// Bytes 是文件大小。
	Bytes int64 `json:"bytes"`
}

// ExportSession 把会话 transcript 导出为 Markdown：同时写文件并复制到剪贴板。
// path 为空时写入 <数据目录>/exports/<时间>-<标题>.md；返回文件信息。
func (a *App) ExportSession(sessionID, path string) (ExportResultDTO, error) {
	store, cfg, err := a.sessionStore()
	if err != nil {
		return ExportResultDTO{}, err
	}
	if err := session.ValidateID(sessionID); err != nil {
		return ExportResultDTO{}, fmt.Errorf("app: export session: %w", err)
	}
	recs, err := store.Load(a.lifeCtx(), sessionID)
	if err != nil && err != session.ErrCorruptLines {
		return ExportResultDTO{}, fmt.Errorf("app: export session: %w", err)
	}
	markdown, count := renderTranscriptMarkdown(recs, time.Now())
	if count == 0 {
		return ExportResultDTO{}, fmt.Errorf("app: export session: %w: nothing to export", ErrSessionNotFound)
	}

	if strings.TrimSpace(path) == "" {
		dir := filepath.Join(cfg.DataDir, "exports")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return ExportResultDTO{}, fmt.Errorf("app: export session: %w", err)
		}
		title := exportFileName(sessionTitleOf(recs))
		path = filepath.Join(dir, time.Now().Format("20060102-150405")+"-"+title+".md")
	}
	if err := os.WriteFile(path, []byte(markdown), 0o600); err != nil {
		return ExportResultDTO{}, fmt.Errorf("app: export session: write %s: %w", path, err)
	}
	// 剪贴板复制失败不阻断导出：文件已经写出。Wails 未启动（测试）时跳过。
	if a.isStarted() {
		_ = runtime.ClipboardSetText(a.lifeCtx(), markdown)
	}
	info, err := os.Stat(path)
	if err != nil {
		return ExportResultDTO{}, fmt.Errorf("app: export session: stat %s: %w", path, err)
	}
	return ExportResultDTO{Path: path, MessageCount: count, Bytes: info.Size()}, nil
}

// renderTranscriptMarkdown 把记录重放成 Markdown 文本，返回文本与消息数。
// 图片块只标注数量不内嵌 base64；tool_result 预览截断到 2KB。
func renderTranscriptMarkdown(recs []session.Record, now time.Time) (string, int) {
	var b strings.Builder
	meta := session.Meta{}
	messages := 0
	for _, rec := range recs {
		if rec.Type != session.RecordSessionMeta {
			continue
		}
		meta.ID = rec.SessionID
		meta.Title = rec.Meta["title"]
		meta.WorkDir = rec.Meta["workDir"]
		meta.CreatedAt = rec.Time
		break
	}
	b.WriteString("# " + sessionTitleOf(recs) + "\n\n")
	b.WriteString("| 字段 | 值 |\n|---|---|\n")
	fmt.Fprintf(&b, "| 会话 ID | `%s` |\n", meta.ID)
	if meta.WorkDir != "" {
		fmt.Fprintf(&b, "| 工作目录 | `%s` |\n", meta.WorkDir)
	}
	if !meta.CreatedAt.IsZero() {
		fmt.Fprintf(&b, "| 创建时间 | %s |\n", meta.CreatedAt.Format("2006-01-02 15:04:05"))
	}
	fmt.Fprintf(&b, "| 导出时间 | %s |\n\n---\n", now.Format("2006-01-02 15:04:05"))

	for _, rec := range recs {
		if rec.Type != session.RecordMessage || rec.Message == nil {
			continue
		}
		messages++
		msg := rec.Message
		clock := msg.Time.Format("15:04:05")
		if msg.Role == "user" {
			// 工具结果回灌消息按块渲染，不渲染成「用户」发言。
			if len(msg.Blocks) > 0 && msg.Blocks[0].Type == "tool_result" {
				b.WriteString(renderToolResultBlocks(msg.Blocks, clock))
				continue
			}
			fmt.Fprintf(&b, "\n## 用户 · %s\n\n", clock)
			b.WriteString(renderUserBlocks(msg.Blocks))
			continue
		}
		fmt.Fprintf(&b, "\n## 助手 · %s\n\n", clock)
		b.WriteString(renderAssistantBlocks(msg.Blocks))
	}
	return b.String(), messages
}

// renderUserBlocks 渲染用户消息块：文本进正文，附件块折叠成引用说明，图片计数。
func renderUserBlocks(blocks []llm.ContentBlock) string {
	var b strings.Builder
	for _, block := range blocks {
		switch block.Type {
		case "text":
			text := block.Text
			if strings.Contains(text, "<system-reminder>") {
				b.WriteString("> （附件：@ 引用 / 命令展开内容，见 transcript）\n\n")
				continue
			}
			b.WriteString(text)
			b.WriteString("\n\n")
		case "image":
			b.WriteString("> （附带图片）\n\n")
		}
	}
	return b.String()
}

// renderAssistantBlocks 渲染助手消息块：文本、思考引用、工具调用 JSON。
func renderAssistantBlocks(blocks []llm.ContentBlock) string {
	var b strings.Builder
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if strings.TrimSpace(block.Text) == "" {
				continue
			}
			b.WriteString(block.Text)
			b.WriteString("\n\n")
		case "thinking":
			if strings.TrimSpace(block.Thinking) == "" {
				continue
			}
			b.WriteString("> **思考**\n>\n")
			for _, line := range strings.Split(strings.TrimRight(block.Thinking, "\n"), "\n") {
				b.WriteString("> " + line + "\n")
			}
			b.WriteString("\n")
		case "tool_use":
			name := block.ToolName
			if name == "" {
				name = "tool"
			}
			fmt.Fprintf(&b, "**工具调用 · %s**\n\n```json\n%s\n```\n\n", name, block.Input)
		}
	}
	return b.String()
}

// renderToolResultBlocks 渲染工具结果回灌消息。
func renderToolResultBlocks(blocks []llm.ContentBlock, clock string) string {
	var b strings.Builder
	for _, block := range blocks {
		if block.Type != "tool_result" {
			continue
		}
		preview := block.Text
		if len(preview) > toolResultPreviewLimit {
			preview = preview[:toolResultPreviewLimit] + "\n…（截断）"
		}
		state := "结果"
		if block.IsError {
			state = "错误"
		}
		fmt.Fprintf(&b, "\n**工具%s · %s**\n\n```\n%s\n```\n", state, clock, preview)
	}
	return b.String()
}

// sessionTitleOf 从记录里取最新标题；没有标题记录时回退首行 meta 或会话 ID。
func sessionTitleOf(recs []session.Record) string {
	title := ""
	for _, rec := range recs {
		if rec.Type == session.RecordSessionMeta {
			title = rec.Meta["title"]
		}
		if rec.Type == session.RecordTitle && rec.Meta["title"] != "" {
			title = rec.Meta["title"]
		}
	}
	if strings.TrimSpace(title) == "" {
		return "会话"
	}
	return title
}

// exportFileName 把标题转成安全的文件名片段。
func exportFileName(title string) string {
	var b strings.Builder
	for _, r := range title {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r > 127:
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "session"
	}
	if len([]rune(out)) > 48 {
		out = string([]rune(out)[:48])
	}
	return out
}

// toolResultPreviewLimit 是导出中工具结果的截断上限。
const toolResultPreviewLimit = 2048
