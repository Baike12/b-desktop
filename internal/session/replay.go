// 本文件实现 transcript 的重放：定位最后的压缩边界、重建工具调用配对。
// 算法见 docs/agent-architecture.md §8.2–§8.4。
package session

import (
	"fmt"

	"github.com/Baike12/b-desktop/internal/llm"
)

// interruptedResultText 是重放时为缺失的工具结果填写的占位文本（§8.4）。
const interruptedResultText = "[interrupted before resume]"

// Replay 从记录重建消息历史：
//  0. 首行必须是 session_meta，重建 Loaded.Meta；否则返回 ErrInvalidTranscript；
//  1. 取最后一条 compact 记录作为边界，其 Summary 成为第一条 user 消息；
//  2. 只载入边界之后的消息记录；
//  3. 按 tool_use 顺序重建每个 assistant 消息之后的 tool_result 消息；
//  4. 丢弃没有内容块的 assistant 消息；
//  5. usage 记录用 Sum 累加，最后一条记入 LastUsage；mode 取最后一条；
//     LastAssistantAt 取最后一条 assistant 的时间。
func Replay(recs []Record) (Loaded, error) {
	var out Loaded
	if len(recs) == 0 {
		return out, fmt.Errorf("%w: empty transcript", ErrInvalidTranscript)
	}
	head := recs[0]
	if head.Type != RecordSessionMeta {
		return out, fmt.Errorf("%w: first record is %q, want %q", ErrInvalidTranscript, head.Type, RecordSessionMeta)
	}
	out.Meta = Meta{
		ID:        head.SessionID,
		Title:     head.Meta["title"],
		WorkDir:   head.Meta["workDir"],
		Model:     head.Meta["model"],
		CreatedAt: head.Time,
	}
	out.Records = len(recs)

	// usage 与 mode 覆盖整个文件，包括压缩边界之前（§8.3 第 6 点）。
	boundaryIdx := -1
	for i, r := range recs {
		switch r.Type {
		case RecordCompact:
			if r.Compact != nil {
				boundaryIdx = i
				c := *r.Compact
				out.Boundary = &c
			}
		case RecordUsage:
			if r.Usage != nil {
				out.Usage = sumUsage(out.Usage, *r.Usage)
				// 文件按时间追加，最后一条 usage 即最近一次请求的用量。
				out.LastUsage = *r.Usage
			}
		case RecordMode:
			if v, ok := r.Meta["mode"]; ok {
				out.Mode = v
			}
		case RecordTitle:
			// 标题更新是追加记录：位置无关，最后一条生效。
			if v := r.Meta["title"]; v != "" {
				out.Meta.Title = v
			}
		case RecordAgentState:
			// agent_state 也是追加记录：最后一条生效。
			if len(r.Meta) > 0 {
				out.AgentState = make(map[string]string, len(r.Meta))
				for key, value := range r.Meta {
					out.AgentState[key] = value
				}
			}
		}
	}

	start := 0
	if boundaryIdx >= 0 {
		b := recs[boundaryIdx]
		if b.Compact.Summary != "" {
			out.Messages = append(out.Messages, llm.Message{
				ID:     compactMessageID(b.ID),
				Role:   llm.RoleUser,
				Blocks: []llm.ContentBlock{{Type: llm.BlockText, Text: b.Compact.Summary}},
				Time:   b.Time,
			})
		}
		start = boundaryIdx + 1
	}

	raw := make([]llm.Message, 0, len(recs)-start)
	for _, r := range recs[start:] {
		if r.Type != RecordMessage || r.Message == nil {
			continue
		}
		m := cloneMessage(*r.Message)
		if m.Time.IsZero() {
			m.Time = r.Time
		}
		raw = append(raw, m)
	}

	paired, interrupted := rebuildToolPairs(raw)
	out.Interrupted = interrupted
	for _, m := range paired {
		if m.Role == llm.RoleAssistant && len(m.Blocks) == 0 {
			continue
		}
		out.Messages = append(out.Messages, m)
	}
	for i := len(out.Messages) - 1; i >= 0; i-- {
		if out.Messages[i].Role == llm.RoleAssistant {
			out.LastAssistantAt = out.Messages[i].Time
			break
		}
	}
	return out, nil
}

// rebuildToolPairs 为每个含 tool_use 的 assistant 消息补齐紧随其后的结果消息。
// 返回的 bool 表示最后一条 assistant 消息是否存在未配对的 tool_use。
func rebuildToolPairs(msgs []llm.Message) ([]llm.Message, bool) {
	out := make([]llm.Message, 0, len(msgs)+2)
	lastIncomplete := false

	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		if m.Role != llm.RoleAssistant {
			out = append(out, m)
			continue
		}
		ids := toolUseIDs(m)
		if len(ids) == 0 {
			lastIncomplete = false
			out = append(out, m)
			continue
		}

		m = cloneMessage(m)
		out = append(out, m)

		var next *llm.Message
		if i+1 < len(msgs) && msgs[i+1].Role == llm.RoleUser && hasToolResult(msgs[i+1]) {
			n := cloneMessage(msgs[i+1])
			next = &n
		}

		existing := make(map[string]llm.ContentBlock, len(ids))
		var others []llm.ContentBlock
		if next != nil {
			for _, b := range next.Blocks {
				if b.Type != llm.BlockToolResult {
					others = append(others, b)
					continue
				}
				if _, dup := existing[b.ToolUseID]; !dup {
					existing[b.ToolUseID] = b
				}
			}
		}

		blocks := make([]llm.ContentBlock, 0, len(ids)+len(others))
		incomplete := false
		for _, id := range ids {
			if b, ok := existing[id]; ok {
				blocks = append(blocks, b)
				continue
			}
			incomplete = true
			blocks = append(blocks, syntheticToolResult(id))
		}
		blocks = append(blocks, others...)
		lastIncomplete = incomplete

		if next != nil {
			next.Blocks = blocks
			out = append(out, *next)
			i++
			continue
		}
		out = append(out, llm.Message{
			ID:     syntheticUserID(m.ID),
			Role:   llm.RoleUser,
			Blocks: blocks,
			Time:   m.Time,
		})
	}
	return out, lastIncomplete
}

func toolUseIDs(m llm.Message) []string {
	var ids []string
	for _, b := range m.Blocks {
		if b.Type == llm.BlockToolUse {
			ids = append(ids, b.ToolUseID)
		}
	}
	return ids
}

func hasToolResult(m llm.Message) bool {
	for _, b := range m.Blocks {
		if b.Type == llm.BlockToolResult {
			return true
		}
	}
	return false
}

func syntheticToolResult(toolUseID string) llm.ContentBlock {
	return llm.ContentBlock{
		Type:      llm.BlockToolResult,
		ToolUseID: toolUseID,
		Text:      interruptedResultText,
		IsError:   true,
	}
}

func compactMessageID(recordID string) string {
	if recordID == "" {
		return "msg_compact"
	}
	return "msg_compact_" + recordID
}

func syntheticUserID(assistantID string) string {
	if assistantID == "" {
		return "msg_tool_results"
	}
	return "msg_tool_results_" + assistantID
}

func sumUsage(a, b llm.Usage) llm.Usage {
	return llm.Usage{
		InputTokens:              a.InputTokens + b.InputTokens,
		OutputTokens:             a.OutputTokens + b.OutputTokens,
		CacheReadInputTokens:     a.CacheReadInputTokens + b.CacheReadInputTokens,
		CacheCreationInputTokens: a.CacheCreationInputTokens + b.CacheCreationInputTokens,
	}
}
