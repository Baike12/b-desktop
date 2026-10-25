// Package context 负责系统提示组装、token 估算与上下文压缩。
// 引用本包时使用别名 sysctx，避免与标准库 context 冲突。
package context

import (
	"github.com/Baike12/b-desktop/internal/llm"
)

// Estimator 估算文本与消息的 token 数。Compactor 通过它计算阈值与节省量，
// 注入不同实现即可替换估算策略（例如换成真实 tokenizer）。
type Estimator interface {
	// EstimateString 估算一段文本的 token 数。
	EstimateString(s string) int
	// EstimateMessages 估算一组消息的 token 数，包含消息与块的结构开销。
	EstimateMessages(msgs []llm.Message) int
}

// DefaultEstimator 是字符比例估算：ASCII 约 4 字符/token，非 ASCII 约 1 字符/token。
// 它是 EstimateString / EstimateMessages 的默认实现，也是 Compactor 的默认估算器。
type DefaultEstimator struct{}

// EstimateString 按默认字符比例估算文本 token 数：
// ASCII 每 4 字符记 1 token（向上取整），其余每个 rune 记 1 token。
func (DefaultEstimator) EstimateString(s string) int {
	ascii, nonASCII := 0, 0
	for _, r := range s {
		if r < 128 {
			ascii++
			continue
		}
		nonASCII++
	}
	return (ascii+3)/4 + nonASCII
}

// imageTokenEstimate 是单个图片内容块的 token 估算值。
// Anthropic 按 (宽×高)/750 计费，1568px 图片最高约 3277 token；取 2000
// 与参考实现一致，宁可高估，避免 auto-compact 触发过晚。
const imageTokenEstimate = 2000

// EstimateMessages 按默认字符比例估算消息 token 数：每条消息 +4，每个块 +6，
// 再加上块内容本身的估算；图片块按固定常数计。
func (DefaultEstimator) EstimateMessages(msgs []llm.Message) int {
	total := 0
	for _, m := range msgs {
		total += 4
		for _, b := range m.Blocks {
			if b.Type == llm.BlockImage {
				total += imageTokenEstimate
				continue
			}
			if b.Type == llm.BlockToolResult {
				total += imageTokenEstimate * len(b.Images)
			}
			total += 6 + DefaultEstimator{}.EstimateString(blockText(b))
		}
	}
	return total
}

// EstimateString 用默认估算器估算文本 token 数。
func EstimateString(s string) int {
	return DefaultEstimator{}.EstimateString(s)
}

// EstimateMessages 用默认估算器估算消息数组 token 数（每条消息 +4，每个块 +6）。
func EstimateMessages(msgs []llm.Message) int {
	return DefaultEstimator{}.EstimateMessages(msgs)
}

// blockText 返回一个内容块中需要计入 token 的文本部分。
func blockText(b llm.ContentBlock) string {
	switch b.Type {
	case llm.BlockText:
		return b.Text
	case llm.BlockThinking:
		return b.Thinking + b.Signature
	case llm.BlockToolUse:
		return b.ToolName + string(b.Input)
	case llm.BlockToolResult:
		return b.Text
	default:
		return b.Text + b.Thinking
	}
}

// estimateMessage 用给定估算器估算单条消息，供保留尾部算法逐条累加。
func estimateMessage(est Estimator, m llm.Message) int {
	return est.EstimateMessages([]llm.Message{m})
}
