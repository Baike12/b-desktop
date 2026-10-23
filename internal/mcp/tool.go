package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Baike12/b-desktop/internal/tool"
)

const (
	// defaultInputSchema 是服务器未提供 inputSchema 时使用的 JSON Schema。
	defaultInputSchema = `{"type":"object"}`
	// maxDescriptionRunes 是工具描述转给模型时的长度上限，与参考实现一致。
	maxDescriptionRunes = 2048
	// descriptionTruncatedSuffix 追加在被截断的描述末尾。
	descriptionTruncatedSuffix = "… [truncated]"
)

// ClientTool 是一个远端 MCP 工具在本地的包装，实现 tool.Tool。
//
// 工具名固定为 mcp__<server>__<tool>（非法字符替换为下划线），
// 输入 schema 原样透传；只读与并发安全都取自服务器的 readOnlyHint。
// 每次 Run 都通过 Manager 解析当前连接，因此 Reload 之后旧包装仍然可用。
type ClientTool struct {
	manager     *Manager
	server      string
	remote      string
	name        string
	description string
	schema      json.RawMessage
	readOnly    bool
}

// Name 返回模型看到的全名，形如 mcp__server__tool。
func (t *ClientTool) Name() string { return t.name }

// Description 返回服务器给出的用途说明，超长部分被截断。
func (t *ClientTool) Description() string { return t.description }

// InputSchema 返回服务器给出的 JSON Schema 副本；缺失时返回 {"type":"object"}。
func (t *ClientTool) InputSchema() json.RawMessage {
	if len(t.schema) == 0 {
		return json.RawMessage(defaultInputSchema)
	}
	out := make(json.RawMessage, len(t.schema))
	copy(out, t.schema)
	return out
}

// ReadOnly 报告服务器是否把该工具标注为只读。
func (t *ClientTool) ReadOnly(json.RawMessage) bool { return t.readOnly }

// ConcurrencySafe 与 ReadOnly 相同：只读调用可以并行执行。
func (t *ClientTool) ConcurrencySafe(json.RawMessage) bool { return t.readOnly }

// Run 调用远端工具。JSON-RPC 层失败返回 error，工具自身失败放在结果的 IsError 中。
func (t *ClientTool) Run(ctx context.Context, input json.RawMessage, progress tool.ProgressFunc) (tool.Result, error) {
	if progress != nil {
		progress(tool.Progress{Text: "调用 MCP 工具 " + t.name})
	}
	return t.manager.callTool(ctx, t.server, t.remote, input)
}

// Server 返回配置中的服务器名（未规范化）。
func (t *ClientTool) Server() string { return t.server }

// RemoteName 返回服务器侧的原始工具名（未规范化）。
func (t *ClientTool) RemoteName() string { return t.remote }

// buildToolName 生成模型可见的全名：mcp__<server>__<tool>。
func buildToolName(server, remote string) string {
	return "mcp__" + normalizeName(server) + "__" + normalizeName(remote)
}

// normalizeName 把名字规范化为 ^[a-zA-Z0-9_-]{1,64}$ 可接受的形式，
// 与参考实现一致：非法字符（含点与空格）替换为下划线。
func normalizeName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// truncateDescription 按字符（非字节）截断过长描述。
func truncateDescription(desc string) string {
	if utf8.RuneCountInString(desc) <= maxDescriptionRunes {
		return desc
	}
	runes := []rune(desc)
	return string(runes[:maxDescriptionRunes]) + descriptionTruncatedSuffix
}

// newClientTool 用工具列表条目和连接它的 Manager 构造包装。
func newClientTool(m *Manager, server string, info ToolInfo) *ClientTool {
	readOnly := info.Annotations != nil && info.Annotations.ReadOnlyHint != nil && *info.Annotations.ReadOnlyHint
	return &ClientTool{
		manager:     m,
		server:      server,
		remote:      info.Name,
		name:        buildToolName(server, info.Name),
		description: truncateDescription(info.Description),
		schema:      append(json.RawMessage(nil), info.InputSchema...),
		readOnly:    readOnly,
	}
}

// renderCallResult 把 tools/call 的内容块渲染成回灌模型的文本。
// 文本块直接拼接；图片、音频、资源等保持原始 JSON，避免丢失类型信息；
// structuredContent 存在时追加到末尾。
func renderCallResult(res CallToolResult) string {
	parts := make([]string, 0, len(res.Content)+1)
	for _, raw := range res.Content {
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 {
			continue
		}
		var block struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(trimmed, &block); err == nil && block.Type == "text" {
			parts = append(parts, block.Text)
			continue
		}
		parts = append(parts, string(trimmed))
	}
	if structured := bytes.TrimSpace(res.StructuredContent); len(structured) > 0 && string(structured) != "null" {
		parts = append(parts, "structuredContent: "+string(structured))
	}
	return strings.Join(parts, "\n")
}

// resultMeta 描述一次远端调用的来源，写入 tool.Result.Meta 供 UI 展示。
func resultMeta(server, remote string) map[string]string {
	return map[string]string{"mcpServer": server, "mcpTool": remote}
}

// wrapRunError 给远端调用错误补充服务器与工具上下文。
func wrapRunError(err error, server, remote string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("mcp: server %q tool %q: %w", server, remote, err)
}
