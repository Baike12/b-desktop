package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Baike12/b-desktop/internal/tool"
)

// 工具名与说明。
const (
	// ToolNameSkill 是技能加载工具的名称。
	ToolNameSkill = "Skill"
)

// skillInput 是 Skill 工具的入参。
type skillInput struct {
	Name string `json:"name"`
}

// skillTool 按名称读取 SKILL.md 正文。技能清单已在系统提示中，工具只负责取正文。
type skillTool struct {
	loader *Loader
}

// Tools 返回技能模块提供的工具集合。
func (l *Loader) Tools() []tool.Tool {
	return []tool.Tool{&skillTool{loader: l}}
}

// Name 返回工具名。
func (t *skillTool) Name() string { return ToolNameSkill }

// Description 返回给模型看的用途说明。
func (t *skillTool) Description() string {
	return "按名称加载一个技能的完整说明（SKILL.md 正文）。当任务与系统提示中列出的某个技能相关时调用；入参是技能名。"
}

// InputSchema 返回 JSON Schema。
func (t *skillTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"要加载的技能名称，必须与可用技能清单中的名称一致"}},"required":["name"],"additionalProperties":false}`)
}

// ReadOnly 报告该工具只读。
func (t *skillTool) ReadOnly(input json.RawMessage) bool { return true }

// ConcurrencySafe 报告该工具可与其他只读调用并行。
func (t *skillTool) ConcurrencySafe(input json.RawMessage) bool { return true }

// Run 读取技能正文。技能不存在时返回 ErrSkillNotFound。
func (t *skillTool) Run(ctx context.Context, input json.RawMessage, progress tool.ProgressFunc) (tool.Result, error) {
	var in skillInput
	if err := json.Unmarshal(input, &in); err != nil {
		return tool.Result{}, fmt.Errorf("skill: decode input: %w", err)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return tool.Result{}, ErrEmptyName
	}
	body, err := t.loader.Body(ctx, name)
	if err != nil {
		return tool.Result{}, err
	}
	if body == "" {
		return tool.Result{Content: fmt.Sprintf("技能 %q 没有正文。", name)}, nil
	}
	if progress != nil {
		progress(tool.Progress{Text: "已加载技能 " + name, Bytes: int64(len(body))})
	}
	return tool.Result{
		Content: fmt.Sprintf("# 技能：%s\n\n%s", name, body),
		Meta:    map[string]string{"skill": name},
	}, nil
}
