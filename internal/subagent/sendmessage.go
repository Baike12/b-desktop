package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Baike12/b-desktop/internal/tool"
)

// SendMessageToolName 是 SendMessage 工具对模型暴露的名称。
const SendMessageToolName = "SendMessage"

// sendMessageMaxContentChars 是 message 字段的长度上限。
const sendMessageMaxContentChars = 20000

// sendMessageSchema 是 SendMessage 的输入 schema。
var sendMessageSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "to": {
      "type": "string",
      "description": "Recipient: the agent ID returned by the Agent tool (format subagent_xxx). One recipient per call."
    },
    "summary": {
      "type": "string",
      "description": "A 5-10 word summary shown as a preview in the UI. Required."
    },
    "message": {
      "type": "string",
      "description": "The message text delivered to the agent."
    }
  },
  "required": ["to", "summary", "message"],
  "additionalProperties": false
}`)

// sendMessageTool 把协调消息投递给一个子 Agent。它实现了 tool.Tool。
// 运行中的任务：消息入队，子 Agent 在下一个工具边界看到它（steer）；
// 已结束的任务：以该消息后台续跑，沿用完整历史（resume）。
type sendMessageTool struct {
	mgr *Manager
}

// 编译期断言：sendMessageTool 实现 tool.Tool 与 tool.AlwaysAllow。
var _ tool.Tool = (*sendMessageTool)(nil)
var _ tool.AlwaysAllow = (*sendMessageTool)(nil)

// NewSendMessageTool 返回调用 m 的 SendMessage 工具实例。
func NewSendMessageTool(m *Manager) tool.Tool { return &sendMessageTool{mgr: m} }

// Name 返回工具名。
func (t *sendMessageTool) Name() string { return SendMessageToolName }

// Description 返回工具说明。
func (t *sendMessageTool) Description() string {
	return "Send a message to a background sub-agent started with the Agent tool. " +
		"If the agent is still running, the message is queued and injected at its next tool round (steer); " +
		"if it already finished, it resumes in the background with its full conversation history and the message as new input. " +
		"Use it to continue a previous agent with follow-up instructions instead of starting a new one. " +
		"The recipient is the agent ID returned by the Agent tool, e.g. subagent_xxx."
}

// InputSchema 返回工具输入 schema。
func (t *sendMessageTool) InputSchema() json.RawMessage { return sendMessageSchema }

// ReadOnly 报告该调用只读：投递消息不修改文件系统。
func (t *sendMessageTool) ReadOnly(json.RawMessage) bool { return true }

// ConcurrencySafe 报告该调用可与其他安全调用并行。
func (t *sendMessageTool) ConcurrencySafe(json.RawMessage) bool { return true }

// AlwaysAllow 报告该调用默认放行（消息投递无副作用）。
func (t *sendMessageTool) AlwaysAllow() bool { return true }

// Run 解析输入并调用 Manager.SendMessageToTask。
func (t *sendMessageTool) Run(ctx context.Context, input json.RawMessage, progress tool.ProgressFunc) (tool.Result, error) {
	var in struct {
		To      string `json:"to"`
		Summary string `json:"summary"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return tool.Result{}, fmt.Errorf("%s: decode input: %w", SendMessageToolName, err)
	}
	if strings.TrimSpace(in.Summary) == "" {
		return tool.Result{}, fmt.Errorf("%s: summary is required", SendMessageToolName)
	}
	if len(in.Message) > sendMessageMaxContentChars {
		return tool.Result{}, fmt.Errorf("%s: message exceeds %d characters", SendMessageToolName, sendMessageMaxContentChars)
	}
	reply, err := t.mgr.SendMessageToTask(in.To, in.Summary, in.Message)
	if err != nil {
		return tool.Result{IsError: true, Content: err.Error()}, nil
	}
	return tool.Result{Content: reply, Display: reply}, nil
}
