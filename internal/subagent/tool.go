package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Baike12/b-desktop/internal/agent"
	"github.com/Baike12/b-desktop/internal/tool"
)

// ToolName 是 Agent 工具对模型暴露的名称。
const ToolName = "Agent"

// agentToolInput 是 Agent 工具的输入。
type agentToolInput struct {
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
	AgentType   string `json:"agent_type"`
	Background  bool   `json:"background"`
}

// agentToolSchema 是 Agent 工具的输入 schema。
var agentToolSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "description": {
      "type": "string",
      "description": "任务的一句话描述（3-5 个词），用于在任务列表中区分该子 Agent。"
    },
    "prompt": {
      "type": "string",
      "description": "交给子 Agent 的完整任务说明。子 Agent 看不到当前对话，必须在 prompt 里交代背景、目标与期望输出。"
    },
    "agent_type": {
      "type": "string",
      "description": "子 Agent 类型；省略时使用通用子 Agent。可选值见工具描述中的列表。"
    },
    "background": {
      "type": "boolean",
      "description": "为 true 时立即返回任务 ID，子 Agent 在后台运行，结果通过任务列表查询。"
    }
  },
  "required": ["description", "prompt"],
  "additionalProperties": false
}`)

// agentTool 是调用 Manager.Run 的 tool.Tool 实现。
type agentTool struct {
	mgr *Manager
}

// 编译期断言：agentTool 实现 tool.Tool。
var _ tool.Tool = (*agentTool)(nil)

// NewAgentTool 返回调用 m 的 Agent 工具实例。
func NewAgentTool(m *Manager) tool.Tool { return &agentTool{mgr: m} }

// Name 返回工具名。
func (t *agentTool) Name() string { return ToolName }

// Description 返回工具说明，并列出当前可用的子 Agent 类型。
func (t *agentTool) Description() string {
	var b strings.Builder
	b.WriteString("启动一个子 Agent 独立完成指定任务，并返回它的最终文本结果。")
	b.WriteString("子 Agent 拥有独立会话历史、工具集与权限范围，看不到当前对话；")
	b.WriteString("需要让它做研究、批量修改或独立复核时使用。")
	types := t.mgr.Types()
	if len(types) == 0 {
		b.WriteString("当前没有自定义 agent_type，省略 agent_type 即可。")
		return b.String()
	}
	b.WriteString("可用的 agent_type：")
	for _, at := range types {
		b.WriteString("\n- ")
		b.WriteString(at.Name)
		b.WriteString(": ")
		b.WriteString(at.Description)
	}
	return b.String()
}

// InputSchema 返回工具输入 schema。
func (t *agentTool) InputSchema() json.RawMessage { return agentToolSchema }

// ReadOnly 报告该调用会启动可能产生副作用的子 Agent，因此不是只读调用。
func (t *agentTool) ReadOnly(json.RawMessage) bool { return false }

// ConcurrencySafe 报告该调用不能与其他写操作并行，避免子 Agent 与父 Agent 竞争同一批文件。
func (t *agentTool) ConcurrencySafe(json.RawMessage) bool { return false }

// Run 解析输入并调用 Manager.Run。
func (t *agentTool) Run(ctx context.Context, input json.RawMessage, progress tool.ProgressFunc) (tool.Result, error) {
	var in agentToolInput
	if err := json.Unmarshal(input, &in); err != nil {
		return tool.Result{}, fmt.Errorf("%s: decode input: %w", ToolName, err)
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return tool.Result{}, fmt.Errorf("%s: %w", ToolName, ErrEmptyPrompt)
	}
	if strings.TrimSpace(in.Description) == "" {
		return tool.Result{}, fmt.Errorf("%s: %w", ToolName, ErrEmptyDescription)
	}

	if progress != nil {
		progress(tool.Progress{Text: "正在运行子 Agent"})
	}
	res, err := t.mgr.Run(ctx, agent.SubagentRequest{
		Description: in.Description,
		Prompt:      in.Prompt,
		AgentType:   in.AgentType,
		Background:  in.Background,
	}, nil)
	if err != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", ToolName, err)
	}
	if in.Background {
		text := fmt.Sprintf("子 Agent 已在后台启动，任务 ID 为 %s。完成后通过任务列表查询结果。", res.ID)
		return tool.Result{
			Content: text,
			Display: text,
			Meta:    map[string]string{"taskId": res.ID, "status": string(TaskRunning)},
		}, nil
	}
	if res.Error != "" {
		return tool.Result{Content: res.Error, Display: res.Error, IsError: true, Meta: map[string]string{"taskId": res.ID}}, nil
	}
	text := res.Text
	if strings.TrimSpace(text) == "" {
		text = "子 Agent 已完成，但没有返回文本结果。"
	}
	return tool.Result{Content: text, Display: text, Meta: map[string]string{"taskId": res.ID}}, nil
}
