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
	Description     string `json:"description"`
	Prompt          string `json:"prompt"`
	AgentType       string `json:"agent_type"`
	Background      bool   `json:"background"`
	RunInBackground bool   `json:"run_in_background"`
}

// agentToolSchema 是 Agent 工具的输入 schema。
var agentToolSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "description": {
      "type": "string",
      "description": "A short (3-5 word) task description used to tell this agent apart in the task list."
    },
    "prompt": {
      "type": "string",
      "description": "The complete task instructions for the sub-agent. The sub-agent cannot see this conversation: state the background, goal and expected output in the prompt."
    },
    "agent_type": {
      "type": "string",
      "description": "Sub-agent type; omit for the general-purpose sub-agent. Available types are listed in the tool description."
    },
    "run_in_background": {
      "type": "boolean",
      "description": "Set true to run the sub-agent in the background: the call returns an agent ID immediately and you are notified when it completes. Monitor with TaskOutput, steer or continue it with SendMessage, stop it with TaskStop."
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
	b.WriteString("Launch a sub-agent that runs independently and returns its final text result. ")
	b.WriteString("The sub-agent has its own conversation history, tool set and permission scope; it cannot see this dialogue. ")
	b.WriteString("Use it for research, batch changes or independent review. ")
	b.WriteString("Long or self-contained tasks accept run_in_background=true: you get an agent ID immediately and a ")
	b.WriteString("task-notification when it finishes; steer or continue the agent later with SendMessage.")
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
	background := in.Background || in.RunInBackground

	if !background && progress != nil {
		progress(tool.Progress{Text: "正在运行子 Agent"})
	}
	res, err := t.mgr.Run(ctx, agent.SubagentRequest{
		Description: in.Description,
		Prompt:      in.Prompt,
		AgentType:   in.AgentType,
		Background:  background,
	}, nil)
	if err != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", ToolName, err)
	}
	if background {
		text := fmt.Sprintf("Background agent launched successfully.\n"+
			"agentId: %s (internal ID - do not mention to the user. Use SendMessage with to: '%s' to steer or continue this agent.)\n"+
			"The agent is working in the background. You will be notified automatically when it completes; "+
			"check progress before completion with TaskOutput(task_id=%q).",
			res.ID, res.ID, res.ID)
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
		text = "(Sub-agent completed but returned no output.)"
	}
	text += fmt.Sprintf("\n\nagentId: %s (use SendMessage with to: '%s' to continue this agent)", res.ID, res.ID)
	return tool.Result{Content: text, Display: res.Text, Meta: map[string]string{"taskId": res.ID}}, nil
}
