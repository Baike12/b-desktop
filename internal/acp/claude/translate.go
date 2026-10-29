package claude

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Baike12/b-desktop/internal/acp"
)

// UNSUPPORTED_SLASH_COMMANDS 是不下发前端的 CLI 内置命令，移植适配器的
// UNSUPPORTED_COMMANDS；clear/compact 保留：b-desktop 的 claude 会话会把
// 它们原文转发给 CLI。
var UNSUPPORTED_SLASH_COMMANDS = map[string]bool{
	"cost":               true,
	"keybindings-help":   true,
	"login":              true,
	"logout":             true,
	"output-style:new":   true,
	"release-notes":      true,
	"todos":              true,
	"statusline":         true,
	"install-github-app": true,
}

// streamedCounters 记录一条消息内每个文本/思考块已流式发出的字节数，
// 用于 assistant 全量帧到达时只补发剩余部分。
type streamedCounters struct {
	text  []int
	think []int
}

// translator 把 claude CLI 的 stdout 帧翻译成 ACP 会话更新。
// 它是有状态的：维护流分组键（同时是宿主的 MessageID）、子线程路由键、
// 流式去重计数与已见工具调用。帧按到达顺序串行处理，无并发。
type translator struct {
	cwd string

	// currentGroup 是当前流分组键（Anthropic message.id 或帧 uuid）。
	currentGroup string
	// currentParent 是当前子线程路由键（parent_tool_use_id），主线程为空。
	// 每个帧处理入口都会按帧自身的 parent_tool_use_id 刷新。
	currentParent string
	// counters 按分组键记录流式进度。
	counters map[string]*streamedCounters
	// seenTools 记录已首发过 tool_call 的工具调用 id。
	seenTools map[string]bool
	// streamingTools 记录流式中的工具输入累积。
	streamingTools map[string]*toolAccumulator
	// nextGroup 兜底分组键计数。
	nextGroup int
}

// toolAccumulator 是一个流式工具输入的累积器。
type toolAccumulator struct {
	toolUseID string
	name      string
	builder   strings.Builder
}

// newTranslator 创建翻译器。
func newTranslator(cwd string) *translator {
	return &translator{
		cwd:            cwd,
		counters:       make(map[string]*streamedCounters),
		seenTools:      make(map[string]bool),
		streamingTools: make(map[string]*toolAccumulator),
	}
}

// chunk 构造一条内容块更新，路由到当前子线程。
func (t *translator) chunk(kind acp.UpdateKind, messageID string, block acp.ContentBlock) acp.SessionUpdate {
	return acp.SessionUpdate{Kind: kind, MessageID: messageID, AgentID: t.currentParent, Content: &block}
}

// toolUpdate 构造首发工具更新。
func (t *translator) toolUpdate(call acp.ToolCall) acp.SessionUpdate {
	return acp.SessionUpdate{Kind: acp.UpdateToolCall, AgentID: t.currentParent, ToolCall: &call}
}

// toolPatch 构造工具增量更新。
func (t *translator) toolPatch(update acp.ToolCallUpdate) acp.SessionUpdate {
	return acp.SessionUpdate{Kind: acp.UpdateToolCallUpdate, AgentID: t.currentParent, ToolCallUpdate: &update}
}

// ensureBoundary 在进入新消息前发一条空文本块：宿主以 MessageID 变化作为
// 消息边界信号，纯工具调用消息（没有文本块）也需要它来开启新的消息分组。
// 返回 true 表示边界切换了。
func (t *translator) ensureBoundary(updates []acp.SessionUpdate, messageID string) []acp.SessionUpdate {
	if messageID == "" || messageID == t.currentGroup {
		return updates
	}
	t.currentGroup = messageID
	return append(updates, t.chunk(acp.UpdateAgentMessageChunk, messageID, acp.ContentBlock{Type: "text"}))
}

// handleStreamEvent 翻译一条 stream_event 帧。
func (t *translator) handleStreamEvent(frame streamEventFrame) []acp.SessionUpdate {
	t.currentParent = parentID(frame.ParentToolUseID)
	var updates []acp.SessionUpdate
	event := frame.Event
	group := t.currentGroup
	if event.Type == "message_start" && event.Message != nil && event.Message.ID != "" {
		group = event.Message.ID
		updates = t.ensureBoundary(updates, group)
	} else if event.Type == "message_start" {
		// message_start 没有消息 id：也发出边界，让空正文消息同样开启分组。
		updates = t.ensureBoundary(updates, group)
	}
	if group == "" {
		group = t.fallbackGroup(frame.UUID)
		updates = t.ensureBoundary(updates, group)
	}

	counters := t.counters[group]
	if counters == nil {
		counters = &streamedCounters{}
		t.counters[group] = counters
	}

	switch event.Type {
	case "content_block_start":
		if block := event.ContentBlock; block != nil {
			switch block.Type {
			case "text":
				counters.text = append(counters.text, 0)
			case "thinking":
				counters.think = append(counters.think, 0)
			case "tool_use":
				if block.ID != "" {
					t.seenTools[block.ID] = true
					t.streamingTools[t.toolKey(group, event.Index)] = &toolAccumulator{
						toolUseID: block.ID,
						name:      block.Name,
					}
					// 首发必须是 tool_call（宿主据此创建工具卡片），
					// 后续 refine 才是 tool_call_update。
					updates = append(updates, t.toolUpdate(acp.ToolCall{
						ToolCallID: block.ID,
						Title:      block.Name,
						ToolName:   block.Name,
						Kind:       acp.ToolOther,
						Status:     acp.StatusPending,
					}))
				}
			}
		}
	case "content_block_delta":
		if delta := event.Delta; delta != nil {
			switch delta.Type {
			case "text_delta":
				if len(counters.text) == 0 {
					// 容错：delta 先于 content_block_start 到达时视为隐式文本块。
					counters.text = append(counters.text, 0)
				}
				index := clampIndex(len(counters.text) - 1)
				counters.text[index] += len(delta.Text)
				if delta.Text != "" {
					updates = append(updates, t.chunk(acp.UpdateAgentMessageChunk, group, acp.ContentBlock{Type: "text", Text: delta.Text}))
				}
			case "thinking_delta":
				if len(counters.think) == 0 {
					counters.think = append(counters.think, 0)
				}
				index := clampIndex(len(counters.think) - 1)
				counters.think[index] += len(delta.Thinking)
				if delta.Thinking != "" {
					updates = append(updates, t.chunk(acp.UpdateAgentThoughtChunk, group, acp.ContentBlock{Type: "text", Text: delta.Thinking}))
				}
			case "input_json_delta":
				if acc := t.streamingTools[t.openToolKey(group, event.Index)]; acc != nil {
					acc.builder.WriteString(delta.PartialJSON)
					updates = append(updates, t.toolPatch(acp.ToolCallUpdate{
						ToolCallID: acc.toolUseID,
						InputDelta: delta.PartialJSON,
					}))
				}
			}
		}
	case "content_block_stop":
		if acc := t.streamingTools[t.openToolKey(group, event.Index)]; acc != nil {
			input := json.RawMessage(acc.builder.String())
			if !json.Valid(input) {
				input = nil
			}
			view := toolViewFor(acc.name, input, t.cwd)
			update := acp.ToolCallUpdate{
				ToolCallID: acc.toolUseID,
				Title:      view.Title,
				ToolName:   acc.name,
				Kind:       view.Kind,
				Status:     acp.StatusInProgress,
				Locations:  view.Locations,
			}
			if len(input) > 0 {
				update.RawInput = input
			}
			updates = append(updates, t.toolPatch(update))
			updates = append(updates, t.planForTool(acc.name, input)...)
			delete(t.streamingTools, t.openToolKey(group, event.Index))
		}
	}
	return updates
}

// fallbackGroup 生成兜底分组键。
func (t *translator) fallbackGroup(uuid string) string {
	if uuid != "" {
		return uuid
	}
	t.nextGroup++
	return fmt.Sprintf("msg_stream_%d", t.nextGroup)
}

// toolKey 组建流式工具累积器的 key。
func (t *translator) toolKey(group string, index int) string {
	return fmt.Sprintf("%s#%d", group, index)
}

// openToolKey 查找打开中的工具累积器：先按精确 index，失败后回退到该分组下
// 唯一（或任意）打开的累积器。
func (t *translator) openToolKey(group string, index int) string {
	precise := t.toolKey(group, index)
	if _, ok := t.streamingTools[precise]; ok {
		return precise
	}
	prefix := group + "#"
	var fallback []string
	for key := range t.streamingTools {
		if strings.HasPrefix(key, prefix) {
			fallback = append(fallback, key)
		}
	}
	if len(fallback) > 0 {
		return fallback[0]
	}
	for key := range t.streamingTools {
		return key
	}
	return precise
}

func clampIndex(index int) int {
	if index < 0 {
		return 0
	}
	return index
}

// handleAssistant 翻译一条 assistant 帧：补发未流式部分、首发/更新工具调用。
func (t *translator) handleAssistant(frame assistantFrame) []acp.SessionUpdate {
	t.currentParent = parentID(frame.ParentToolUseID)
	var updates []acp.SessionUpdate
	group := frame.UUID
	if frame.Message.ID != "" {
		group = frame.Message.ID
	}
	if group == "" {
		t.nextGroup++
		group = fmt.Sprintf("msg_asst_%d", t.nextGroup)
	}
	updates = t.ensureBoundary(updates, group)

	if frame.Message.Model != "" {
		updates = append(updates, acp.SessionUpdate{Kind: acp.UpdateStatus, Status: &acp.SessionStatus{Model: frame.Message.Model}})
	}

	counters := t.counters[group]
	if counters == nil {
		counters = &streamedCounters{}
		t.counters[group] = counters
	}

	var textIndex, thinkIndex int
	for _, block := range decodeBlocks(frame.Message.Content) {
		switch block.Type {
		case "text":
			text := block.Text
			start := 0
			if textIndex < len(counters.text) {
				start = counters.text[textIndex]
			}
			if start < len(text) {
				updates = append(updates, t.chunk(acp.UpdateAgentMessageChunk, group, acp.ContentBlock{Type: "text", Text: text[start:]}))
			}
			if textIndex < len(counters.text) {
				counters.text[textIndex] = len(text)
			} else {
				counters.text = append(counters.text, len(text))
			}
			textIndex++
		case "thinking":
			thinking := block.Thinking
			start := 0
			if thinkIndex < len(counters.think) {
				start = counters.think[thinkIndex]
			}
			if start < len(thinking) {
				updates = append(updates, t.chunk(acp.UpdateAgentThoughtChunk, group, acp.ContentBlock{Type: "text", Text: thinking[start:]}))
			}
			if thinkIndex < len(counters.think) {
				counters.think[thinkIndex] = len(thinking)
			} else {
				counters.think = append(counters.think, len(thinking))
			}
			thinkIndex++
		case "tool_use":
			view := toolViewFor(block.Name, block.Input, t.cwd)
			update := acp.ToolCallUpdate{
				ToolCallID: block.ID,
				Title:      view.Title,
				ToolName:   block.Name,
				Kind:       view.Kind,
				Locations:  view.Locations,
			}
			if len(block.Input) > 0 {
				update.RawInput = block.Input
			}
			if t.seenTools[block.ID] {
				update.Status = acp.StatusInProgress
				updates = append(updates, t.toolPatch(update))
			} else {
				t.seenTools[block.ID] = true
				updates = append(updates, t.toolUpdate(acp.ToolCall{
					ToolCallID: update.ToolCallID,
					Title:      update.Title,
					ToolName:   update.ToolName,
					Kind:       update.Kind,
					Status:     acp.StatusPending,
					Locations:  update.Locations,
					RawInput:   update.RawInput,
				}))
			}
			updates = append(updates, t.planForTool(block.Name, block.Input)...)
		}
	}
	return updates
}

// handleUser 翻译一条 user 帧：只关心 tool_result 回灌。
func (t *translator) handleUser(frame userFrame) []acp.SessionUpdate {
	t.currentParent = parentID(frame.ParentToolUseID)
	var updates []acp.SessionUpdate
	for _, block := range decodeBlocks(frame.Message.Content) {
		if block.Type != "tool_result" {
			continue
		}
		update := acp.ToolCallUpdate{
			ToolCallID: block.ToolUseID,
			Status:     acp.StatusCompleted,
		}
		if update.ToolCallID == "" {
			continue
		}
		if block.IsError {
			update.Status = acp.StatusFailed
		}
		if text := toolResultText(block.Content); text != "" {
			update.RawOutput = json.RawMessage(mustJSON(text))
		}
		updates = append(updates, t.toolPatch(update))
	}
	return updates
}

// handleSystem 翻译 system 帧；返回值可能为空。
func (t *translator) handleSystem(frame systemFrame) []acp.SessionUpdate {
	t.currentParent = ""
	switch frame.Subtype {
	case "init":
		var updates []acp.SessionUpdate
		if frame.Model != "" || frame.PermissionMode != "" {
			updates = append(updates, acp.SessionUpdate{
				Kind:   acp.UpdateStatus,
				Status: &acp.SessionStatus{Model: frame.Model, Mode: frame.PermissionMode},
			})
		}
		if commands := filterCommands(frame.SlashCommands, frame.TerminalSlashCommands); len(commands) > 0 {
			updates = append(updates, acp.SessionUpdate{Kind: acp.UpdateAvailableCommands, Commands: commands})
		}
		return updates
	case "commands_changed":
		if commands := filterCommands(frame.Commands, nil); len(commands) > 0 {
			return []acp.SessionUpdate{{Kind: acp.UpdateAvailableCommands, Commands: commands}}
		}
	case "local_command_output":
		if frame.Content != "" {
			return []acp.SessionUpdate{t.chunk(acp.UpdateAgentMessageChunk, t.currentGroup, acp.ContentBlock{Type: "text", Text: frame.Content})}
		}
	case "task_started":
		if frame.ToolUseID != "" {
			update := acp.ToolCallUpdate{ToolCallID: frame.ToolUseID, Status: acp.StatusInProgress}
			if frame.Description != "" {
				update.Title = frame.Description
			}
			return []acp.SessionUpdate{t.toolPatch(update)}
		}
	}
	return nil
}

// planForTool 把 TodoWrite 工具调用翻译成计划快照更新。
func (t *translator) planForTool(name string, input json.RawMessage) []acp.SessionUpdate {
	if name != "TodoWrite" {
		return nil
	}
	entries := parseTodos(input)
	if entries == nil {
		return nil
	}
	return []acp.SessionUpdate{{Kind: acp.UpdatePlan, AgentID: t.currentParent, Plan: &acp.Plan{Entries: entries}}}
}

// parentID 提取 parent_tool_use_id 的值；主线程返回空串。
func parentID(raw *string) string {
	if raw == nil {
		return ""
	}
	return *raw
}

// decodeBlocks 把 content 的线格式（字符串或块数组）解码为块列表。
func decodeBlocks(raw json.RawMessage) []apiContentBlock {
	if len(raw) == 0 {
		return nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return []apiContentBlock{{Type: "text", Text: text}}
	}
	var blocks []apiContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	return blocks
}

// toolResultText 提取 tool_result content 的纯文本预览。
func toolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var blocks []apiContentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		parts := make([]string, 0, len(blocks))
		for _, block := range blocks {
			if block.Type == "text" && block.Text != "" {
				parts = append(parts, block.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// filterCommands 过滤不支持与终端绑定的斜杠命令。
func filterCommands(names []string, terminalBound []string) []acp.AvailableCommand {
	terminal := make(map[string]bool, len(terminalBound))
	for _, name := range terminalBound {
		terminal[name] = true
	}
	out := make([]acp.AvailableCommand, 0, len(names))
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" || UNSUPPORTED_SLASH_COMMANDS[trimmed] || terminal[trimmed] {
			continue
		}
		out = append(out, acp.AvailableCommand{Name: trimmed})
	}
	return out
}

// mustJSON 编码字符串为 JSON（错误时退化为空字符串字面量）。
func mustJSON(text string) string {
	encoded, err := json.Marshal(text)
	if err != nil {
		return `""`
	}
	return string(encoded)
}
