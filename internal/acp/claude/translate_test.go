package claude

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Baike12/b-desktop/internal/acp"
)

// strPtr 是 *string 的快捷构造。
func strPtr(s string) *string { return &s }

// frameAssistant 构造一条 assistant 帧 JSON。
func frameAssistant(t *testing.T, uuid, msgID string, parent *string, blocks []apiContentBlock) assistantFrame {
	t.Helper()
	raw, err := json.Marshal(blocks)
	if err != nil {
		t.Fatalf("marshal blocks: %v", err)
	}
	return assistantFrame{
		Type:            "assistant",
		UUID:            uuid,
		ParentToolUseID: parent,
		Message:         apiMessage{ID: msgID, Role: "assistant", Model: "claude-sonnet-4-5", Content: raw},
	}
}

func TestTranslatorStreamAndDedup(t *testing.T) {
	tr := newTranslator("/tmp")

	// message_start 开启分组并发边界空块。
	updates := tr.handleStreamEvent(streamEventFrame{
		Event: apiStreamEvent{Type: "message_start", Message: &apiMessage{ID: "msg_01"}},
	})
	if len(updates) != 1 {
		t.Fatalf("message_start updates = %d, want 1 (boundary)", len(updates))
	}
	if updates[0].MessageID != "msg_01" || updates[0].Content.Text != "" {
		t.Fatalf("boundary update = %+v", updates[0])
	}

	// 文本增量。
	updates = tr.handleStreamEvent(streamEventFrame{
		Event: apiStreamEvent{Type: "content_block_delta", Delta: &apiStreamDelta{Type: "text_delta", Text: "你好"}},
	})
	if len(updates) != 1 || updates[0].Kind != acp.UpdateAgentMessageChunk || updates[0].Content.Text != "你好" {
		t.Fatalf("text delta updates = %+v", updates)
	}

	// 思考增量。
	updates = tr.handleStreamEvent(streamEventFrame{
		Event: apiStreamEvent{Type: "content_block_delta", Delta: &apiStreamDelta{Type: "thinking_delta", Thinking: "想一想"}},
	})
	if len(updates) != 1 || updates[0].Kind != acp.UpdateAgentThoughtChunk {
		t.Fatalf("thinking delta updates = %+v", updates)
	}

	// assistant 全量帧：已流式的部分不再重复。
	full := frameAssistant(t, "u1", "msg_01", nil, []apiContentBlock{
		{Type: "text", Text: "你好，世界"},
		{Type: "thinking", Thinking: "想一想"},
	})
	updates = tr.handleAssistant(full)
	var text string
	for _, update := range updates {
		if update.Kind == acp.UpdateAgentMessageChunk && update.Content != nil {
			text += update.Content.Text
		}
	}
	if text != "，世界" {
		t.Fatalf("unstreamed remainder = %q, want %q", text, "，世界")
	}
}

func TestTranslatorToolUseFlow(t *testing.T) {
	tr := newTranslator("/work")

	// 流式工具：block_start → input delta → block_stop。
	tr.handleStreamEvent(streamEventFrame{
		Event: apiStreamEvent{Type: "message_start", Message: &apiMessage{ID: "m1"}},
	})
	tr.handleStreamEvent(streamEventFrame{
		Event: apiStreamEvent{Type: "content_block_start", Index: 1, ContentBlock: &apiContentBlock{Type: "tool_use", ID: "toolu_1", Name: "Read"}},
	})
	updates := tr.handleStreamEvent(streamEventFrame{
		Event: apiStreamEvent{Type: "content_block_delta", Index: 1, Delta: &apiStreamDelta{Type: "input_json_delta", PartialJSON: `{"file_pa`}},
	})
	if len(updates) != 1 || updates[0].ToolCallUpdate == nil || updates[0].ToolCallUpdate.InputDelta != `{"file_pa` {
		t.Fatalf("input delta update = %+v", updates)
	}
	updates = tr.handleStreamEvent(streamEventFrame{
		Event: apiStreamEvent{Type: "content_block_delta", Index: 1, Delta: &apiStreamDelta{Type: "input_json_delta", PartialJSON: `th":"demo/main.go"}`}},
	})
	updates = tr.handleStreamEvent(streamEventFrame{
		Event: apiStreamEvent{Type: "content_block_stop", Index: 1},
	})
	if len(updates) == 0 || updates[0].ToolCallUpdate == nil {
		t.Fatalf("block stop updates = %+v", updates)
	}
	stop := updates[0].ToolCallUpdate
	if stop.Title != "Read main.go" {
		t.Fatalf("refined title = %q", stop.Title)
	}
	if stop.Kind != acp.ToolRead {
		t.Fatalf("kind = %q", stop.Kind)
	}
	if string(stop.RawInput) != `{"file_path":"demo/main.go"}` {
		t.Fatalf("raw input = %s", stop.RawInput)
	}

	// assistant 帧：同一 tool_use 不再首发，只 refine。
	updates = tr.handleAssistant(frameAssistant(t, "u1", "m1", nil, []apiContentBlock{
		{Type: "tool_use", ID: "toolu_1", Name: "Read", Input: json.RawMessage(`{"file_path":"demo/main.go"}`)},
	}))
	for _, update := range updates {
		if update.Kind == acp.UpdateToolCall {
			t.Fatalf("duplicate tool_call first emit: %+v", update)
		}
	}
}

func TestTranslatorSubagentRouting(t *testing.T) {
	tr := newTranslator("/work")
	parent := strPtr("toolu_task")

	updates := tr.handleStreamEvent(streamEventFrame{
		ParentToolUseID: parent,
		Event:           apiStreamEvent{Type: "message_start", Message: &apiMessage{ID: "msg_sub"}},
	})
	if updates[0].AgentID != "toolu_task" {
		t.Fatalf("subagent boundary agentId = %q", updates[0].AgentID)
	}
	updates = tr.handleStreamEvent(streamEventFrame{
		ParentToolUseID: parent,
		Event:           apiStreamEvent{Type: "content_block_delta", Delta: &apiStreamDelta{Type: "text_delta", Text: "hi"}},
	})
	if updates[0].AgentID != "toolu_task" {
		t.Fatalf("subagent chunk agentId = %q", updates[0].AgentID)
	}

	// 主线程帧把路由键归零。
	updates = tr.handleAssistant(frameAssistant(t, "u1", "msg_main", nil, []apiContentBlock{{Type: "text", Text: "main"}}))
	if updates[0].AgentID != "" {
		t.Fatalf("main assistant agentId = %q", updates[0].AgentID)
	}

	// 子线程 tool_result 路由到子线程。
	toolResult := `{"type":"tool_result","tool_use_id":"toolu_9","content":"ok"}`
	updates = tr.handleUser(userFrame{
		ParentToolUseID: parent,
		Message:         apiMessage{Role: "user", Content: json.RawMessage("[" + toolResult + "]")},
	})
	if len(updates) != 1 || updates[0].AgentID != "toolu_task" {
		t.Fatalf("subagent tool_result = %+v", updates)
	}
	if updates[0].ToolCallUpdate.Status != acp.StatusCompleted {
		t.Fatalf("tool result status = %q", updates[0].ToolCallUpdate.Status)
	}
}

func TestTranslatorTodoWritePlan(t *testing.T) {
	tr := newTranslator("/work")
	input := json.RawMessage(`{"todos":[{"content":"调研","status":"completed"},{"content":"实现","status":"in_progress","activeForm":"正在实现"}]}`)
	updates := tr.handleAssistant(frameAssistant(t, "u1", "m1", nil, []apiContentBlock{
		{Type: "tool_use", ID: "toolu_todo", Name: "TodoWrite", Input: input},
	}))
	var plan *acp.Plan
	var toolCall *acp.ToolCall
	for _, update := range updates {
		if update.Kind == acp.UpdatePlan {
			plan = update.Plan
		}
		if update.Kind == acp.UpdateToolCall {
			toolCall = update.ToolCall
		}
	}
	// TodoWrite 既产出计划快照，也保留工具卡片（与本地 agent 的 UI 一致）。
	if plan == nil || len(plan.Entries) != 2 || plan.Entries[1].Content != "实现" {
		t.Fatalf("plan = %+v", plan)
	}
	if toolCall == nil || toolCall.Kind != acp.ToolThink {
		t.Fatalf("tool call = %+v", toolCall)
	}
}

func TestToolViewFor(t *testing.T) {
	cases := []struct {
		name  string
		tool  string
		input string
		kind  acp.ToolKind
		title string
	}{
		{"read", "Read", `{"file_path":"/work/src/a.go","offset":10,"limit":5}`, acp.ToolRead, "Read src/a.go (off 10-14)"},
		{"edit", "Edit", `{"file_path":"/work/src/a.go"}`, acp.ToolEdit, "Edit src/a.go"},
		{"write", "Write", `{"file_path":"/abs/b.go"}`, acp.ToolEdit, "Write b.go"},
		{"bash", "Bash", `{"command":"go test ./..."}`, acp.ToolExecute, "go test ./..."},
		{"glob", "Glob", `{"pattern":"*.go","path":"/work"}`, acp.ToolSearch, `Find "/work" "*.go"`},
		{"fetch", "WebFetch", `{"url":"https://x.dev"}`, acp.ToolFetch, "Fetch https://x.dev"},
		{"task", "Task", `{"description":"调研"}`, acp.ToolThink, "调研"},
		{"plan", "ExitPlanMode", `{}`, acp.ToolSwitchMode, "ExitPlanMode"},
		{"mcp", "mcp__srv__tool", `{}`, acp.ToolOther, "tool"},
		{"unknown", "Mystery", `{}`, acp.ToolOther, "Mystery"},
	}
	for _, tc := range cases {
		view := toolViewFor(tc.tool, json.RawMessage(tc.input), "/work")
		if view.Kind != tc.kind || view.Title != tc.title {
			t.Errorf("%s: got (%s, %q), want (%s, %q)", tc.name, view.Kind, view.Title, tc.kind, tc.title)
		}
	}
}

func TestFilterCommands(t *testing.T) {
	commands := filterCommands(
		[]string{"compact", "clear", "cost", "login", "review", "mcp", "help"},
		[]string{"status"},
	)
	names := make([]string, 0, len(commands))
	for _, command := range commands {
		names = append(names, command.Name)
	}
	want := "compact,clear,review,mcp,help"
	if got := strings.Join(names, ","); got != want {
		t.Fatalf("filtered = %q, want %q", got, want)
	}
}

func TestPermissionOptionsAndDecision(t *testing.T) {
	request := canUseToolRequest{
		ToolName: "Bash",
		Input:    json.RawMessage(`{"command":"ls"}`),
		PermissionSuggestions: json.RawMessage(`[
			{"type":"addRules","destination":"localSettings","behavior":"allow","rules":[{"toolName":"Bash","ruleContent":"ls"}]}
		]`),
	}
	options := permissionOptionsFor(request)
	if len(options) != 3 || options[0].Kind != acp.AllowOnce || options[1].Kind != acp.AllowAlways || options[2].Kind != acp.RejectOnce {
		t.Fatalf("options = %+v", options)
	}

	allowAlways, err := decisionFor(request, "allow-with-updates")
	if err != nil {
		t.Fatalf("decisionFor: %v", err)
	}
	if allowAlways.Behavior != "allow" || len(allowAlways.UpdatedPermissions) != 1 {
		t.Fatalf("allow always = %+v", allowAlways)
	}
	if string(allowAlways.UpdatedInput) != `{"command":"ls"}` {
		t.Fatalf("updated input = %s", allowAlways.UpdatedInput)
	}

	reject, err := decisionFor(request, "reject")
	if err != nil || reject.Behavior != "deny" {
		t.Fatalf("reject = %+v err=%v", reject, err)
	}

	// 无建议时 allow_always 合成工具级 allow 规则。
	bare := canUseToolRequest{ToolName: "Bash", Input: json.RawMessage(`{}`)}
	fallback, err := decisionFor(bare, "allow-with-updates")
	if err != nil || len(fallback.UpdatedPermissions) != 1 || fallback.UpdatedPermissions[0].Type != "addRules" {
		t.Fatalf("fallback = %+v err=%v", fallback, err)
	}
}
