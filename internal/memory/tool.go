package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Baike12/b-desktop/internal/tool"
)

// 记忆工具名。模型通过这三个工具读写记忆目录；记忆目录在用户主目录下，
// 超出会话 Write/Edit 的 workDir 沙箱，专用工具是唯一写路径。
const (
	ToolNameMemorySave   = "MemorySave"
	ToolNameMemoryUpdate = "MemoryUpdate"
	ToolNameMemoryDelete = "MemoryDelete"
)

// usageGuide 是三个工具描述共享的使用指引：告诉模型什么值得记。
// 清单对齐 cc memdir 的四类型 + 不存清单（deps/notes/local-cc-zcode-codex.md）。
const usageGuide = "只保存无法从代码、git 历史或文档直接得到的稳定信息：" +
	"user（用户偏好与背景）、feedback（用户对工作方式的纠正与偏好）、" +
	"project（项目目标、约定、外部系统位置）、reference（相关人物与资源链接）。" +
	"不要保存一次性任务细节、可从当前仓库推导的内容、客套话。"

// memoryTools 把 Hub 与会话工作目录绑定成一组记忆工具。
type memoryTools struct {
	hub     *Hub
	workDir string
}

// ToolsFor 返回绑定该工作目录的记忆工具集合。
func (h *Hub) ToolsFor(workDir string) []tool.Tool {
	impl := &memoryTools{hub: h, workDir: workDir}
	return []tool.Tool{
		&memorySaveTool{impl},
		&memoryUpdateTool{impl},
		&memoryDeleteTool{impl},
	}
}

// saveInput 是 MemorySave 的入参。
type saveInput struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Type        string   `json:"type"`
	Scope       string   `json:"scope"`
	Keywords    []string `json:"keywords"`
	Importance  int      `json:"importance"`
	Content     string   `json:"content"`
}

// memorySaveTool 保存一条新记忆。
type memorySaveTool struct {
	impl *memoryTools
}

// Name 返回工具名。
func (t *memorySaveTool) Name() string { return ToolNameMemorySave }

// Description 返回给模型看的用途说明。
func (t *memorySaveTool) Description() string {
	return "保存一条长期记忆到用户或项目记忆库，并更新 MEMORY.md 索引。" +
		"正文与现有记忆完全相同时幂等返回，不会重复保存。" + usageGuide
}

// InputSchema 返回 JSON Schema。
func (t *memorySaveTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` +
		`"name":{"type":"string","description":"记忆名，简洁可检索"},` +
		`"content":{"type":"string","description":"记忆正文，15-80 词，自包含，不依赖上下文也能读懂"},` +
		`"description":{"type":"string","description":"一行描述，用于索引与召回"},` +
		`"type":{"type":"string","description":"user、feedback、project 或 reference，默认 project"},` +
		`"scope":{"type":"string","description":"user（跨项目）或 project（仅当前项目），默认 user"},` +
		`"keywords":{"type":"array","items":{"type":"string"},"description":"2-5 个召回关键词"},` +
		`"importance":{"type":"integer","description":"重要性 1-10，默认 5"}},` +
		`"required":["name","content"],"additionalProperties":false}`)
}

// ReadOnly 报告该工具会写文件，属于写工具。
func (t *memorySaveTool) ReadOnly(input json.RawMessage) bool { return false }

// ConcurrencySafe 报告该工具需要串行执行，避免索引并发重建。
func (t *memorySaveTool) ConcurrencySafe(input json.RawMessage) bool { return false }

// Run 保存记忆。
func (t *memorySaveTool) Run(ctx context.Context, input json.RawMessage, progress tool.ProgressFunc) (tool.Result, error) {
	var in saveInput
	if err := json.Unmarshal(input, &in); err != nil {
		return tool.Result{}, fmt.Errorf("memory: decode input: %w", err)
	}
	hub, workDir := t.impl.hub, t.impl.workDir
	mem, created, err := hub.Save(ctx, in.Scope, workDir, EntryInput{
		Name:        in.Name,
		Description: in.Description,
		Type:        in.Type,
		Keywords:    in.Keywords,
		Importance:  in.Importance,
		Body:        in.Content,
	})
	if err != nil {
		return tool.Result{}, err
	}
	if !created {
		return tool.Result{
			Content: fmt.Sprintf("内容相同的记忆 %q 已存在（%s），未重复保存。", mem.Name, mem.FileName),
			Meta:    map[string]string{"name": mem.Name, "fileName": mem.FileName, "scope": mem.Scope},
		}, nil
	}
	if progress != nil {
		progress(tool.Progress{Text: "已保存记忆 " + mem.Name, Bytes: int64(len(mem.Body))})
	}
	return tool.Result{
		Content: fmt.Sprintf("已保存 %s 记忆 %q（文件 %s）。", mem.Scope, mem.Name, mem.FileName),
		Meta: map[string]string{
			"name":     mem.Name,
			"fileName": mem.FileName,
			"scope":    mem.Scope,
		},
	}, nil
}

// updateInput 是 MemoryUpdate 的入参；空字段表示保持不变。
type updateInput struct {
	Scope       string   `json:"scope"`
	FileName    string   `json:"fileName"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Type        string   `json:"type"`
	Keywords    []string `json:"keywords"`
	Importance  int      `json:"importance"`
	Content     string   `json:"content"`
}

// memoryUpdateTool 更新一条既有记忆。
type memoryUpdateTool struct {
	impl *memoryTools
}

// Name 返回工具名。
func (t *memoryUpdateTool) Name() string { return ToolNameMemoryUpdate }

// Description 返回给模型看的用途说明。
func (t *memoryUpdateTool) Description() string {
	return "更新一条已存在的长期记忆（按 MEMORY.md 索引中的文件名定位）。" +
		"用户纠正了既有记忆、或信息需要合并时使用；空字段保持不变，内容未变时不落盘。"
}

// InputSchema 返回 JSON Schema。
func (t *memoryUpdateTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` +
		`"fileName":{"type":"string","description":"要更新的记忆文件名，来自 MEMORY.md 索引"},` +
		`"scope":{"type":"string","description":"user 或 project，与目标记忆一致，默认 user"},` +
		`"name":{"type":"string","description":"新的记忆名；不修改则省略"},` +
		`"content":{"type":"string","description":"新的正文；不修改则省略"},` +
		`"description":{"type":"string","description":"新的一行描述；不修改则省略"},` +
		`"type":{"type":"string","description":"新的类型；不修改则省略"},` +
		`"keywords":{"type":"array","items":{"type":"string"},"description":"新的关键词；不修改则省略"},` +
		`"importance":{"type":"integer","description":"新的重要性 1-10；不修改则省略"}},` +
		`"required":["fileName"],"additionalProperties":false}`)
}

// ReadOnly 报告该工具会写文件。
func (t *memoryUpdateTool) ReadOnly(input json.RawMessage) bool { return false }

// ConcurrencySafe 报告该工具需要串行执行。
func (t *memoryUpdateTool) ConcurrencySafe(input json.RawMessage) bool { return false }

// Run 更新记忆。
func (t *memoryUpdateTool) Run(ctx context.Context, input json.RawMessage, progress tool.ProgressFunc) (tool.Result, error) {
	var in updateInput
	if err := json.Unmarshal(input, &in); err != nil {
		return tool.Result{}, fmt.Errorf("memory: decode input: %w", err)
	}
	if strings.TrimSpace(in.FileName) == "" {
		return tool.Result{IsError: true}, ErrNotFound
	}
	hub, workDir := t.impl.hub, t.impl.workDir
	mem, changed, err := hub.Update(ctx, in.Scope, workDir, in.FileName, EntryInput{
		Name:        in.Name,
		Description: in.Description,
		Type:        in.Type,
		Keywords:    in.Keywords,
		Importance:  in.Importance,
		Body:        in.Content,
	})
	if err != nil {
		return tool.Result{}, err
	}
	if !changed {
		return tool.Result{
			Content: fmt.Sprintf("记忆 %s 未发生变化。", mem.FileName),
			Meta:    map[string]string{"fileName": mem.FileName, "scope": mem.Scope},
		}, nil
	}
	return tool.Result{
		Content: fmt.Sprintf("已更新记忆 %s。", mem.FileName),
		Meta:    map[string]string{"fileName": mem.FileName, "scope": mem.Scope},
	}, nil
}

// deleteInput 是 MemoryDelete 的入参。
type deleteInput struct {
	Scope    string `json:"scope"`
	FileName string `json:"fileName"`
}

// memoryDeleteTool 软删一条记忆。
type memoryDeleteTool struct {
	impl *memoryTools
}

// Name 返回工具名。
func (t *memoryDeleteTool) Name() string { return ToolNameMemoryDelete }

// Description 返回给模型看的用途说明。
func (t *memoryDeleteTool) Description() string {
	return "删除一条长期记忆（软删：标记归档并从索引移除，文件保留可恢复）。" +
		"仅当用户明确要求删除或记忆与现状矛盾且无法修正时使用。"
}

// InputSchema 返回 JSON Schema。
func (t *memoryDeleteTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` +
		`"fileName":{"type":"string","description":"要删除的记忆文件名，来自 MEMORY.md 索引"},` +
		`"scope":{"type":"string","description":"user 或 project，与目标记忆一致，默认 user"}},` +
		`"required":["fileName"],"additionalProperties":false}`)
}

// ReadOnly 报告该工具会写文件。
func (t *memoryDeleteTool) ReadOnly(input json.RawMessage) bool { return false }

// ConcurrencySafe 报告该工具需要串行执行。
func (t *memoryDeleteTool) ConcurrencySafe(input json.RawMessage) bool { return false }

// Run 归档记忆。
func (t *memoryDeleteTool) Run(ctx context.Context, input json.RawMessage, progress tool.ProgressFunc) (tool.Result, error) {
	var in deleteInput
	if err := json.Unmarshal(input, &in); err != nil {
		return tool.Result{}, fmt.Errorf("memory: decode input: %w", err)
	}
	if strings.TrimSpace(in.FileName) == "" {
		return tool.Result{IsError: true}, ErrNotFound
	}
	hub, workDir := t.impl.hub, t.impl.workDir
	mem, err := hub.Archive(ctx, in.Scope, workDir, in.FileName)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{
		Content: fmt.Sprintf("已删除记忆 %s（归档保留）。", mem.FileName),
		Meta:    map[string]string{"fileName": mem.FileName, "scope": mem.Scope},
	}, nil
}
