package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Baike12/b-desktop/internal/tool"
)

// ToolNameMemorySave 是保存记忆工具的名称。
const ToolNameMemorySave = "MemorySave"

// saveInput 是 MemorySave 的入参。
type saveInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Content     string `json:"content"`
}

// memorySaveTool 把一条新记忆写入记忆目录并追加索引行。
type memorySaveTool struct {
	store *Store
}

// Tools 返回记忆模块提供的工具集合。
func (s *Store) Tools() []tool.Tool {
	return []tool.Tool{&memorySaveTool{store: s}}
}

// Name 返回工具名。
func (t *memorySaveTool) Name() string { return ToolNameMemorySave }

// Description 返回给模型看的用途说明。
func (t *memorySaveTool) Description() string {
	return "保存一条长期记忆到用户记忆目录，并更新 MEMORY.md 索引。只保存无法从代码、git 历史或文档直接得到的信息，例如用户偏好、项目背景、外部系统位置。"
}

// InputSchema 返回 JSON Schema。
func (t *memorySaveTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"记忆名，简洁可检索"},"description":{"type":"string","description":"一行描述，用于未来召回打分"},"type":{"type":"string","description":"记忆类型：user、feedback、project 或 reference，默认 project"},"content":{"type":"string","description":"记忆正文"}},"required":["name","content"],"additionalProperties":false}`)
}

// ReadOnly 报告该工具会写入文件，属于写工具。
func (t *memorySaveTool) ReadOnly(input json.RawMessage) bool { return false }

// ConcurrencySafe 报告该工具需要串行执行，避免索引并发追加。
func (t *memorySaveTool) ConcurrencySafe(input json.RawMessage) bool { return false }

// Run 保存记忆。
func (t *memorySaveTool) Run(ctx context.Context, input json.RawMessage, progress tool.ProgressFunc) (tool.Result, error) {
	var in saveInput
	if err := json.Unmarshal(input, &in); err != nil {
		return tool.Result{}, fmt.Errorf("memory: decode input: %w", err)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return tool.Result{}, ErrEmptyName
	}
	mem, err := t.store.Save(ctx, name, in.Description, in.Type, in.Content)
	if err != nil {
		return tool.Result{}, err
	}
	if progress != nil {
		progress(tool.Progress{Text: "已保存记忆 " + mem.Name, Bytes: int64(len(mem.Body))})
	}
	return tool.Result{
		Content: fmt.Sprintf("已保存记忆 %q（类型 %s，文件 %s）。", mem.Name, mem.Type, mem.FileName),
		Meta: map[string]string{
			"name":     mem.Name,
			"type":     mem.Type,
			"fileName": mem.FileName,
		},
	}, nil
}
