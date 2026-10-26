package agent

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Baike12/b-desktop/internal/tool"
)

// 工具结果兜底截断的默认值。四个参考实现都在 agent 层对工具结果设统一上限：
// cc 默认 50000 字符（constants/toolLimits.ts DEFAULT_MAX_RESULT_SIZE_CHARS）、
// codex 按模型策略 10k tokens 中间截断（truncate.rs 保留头尾）、ZCode
// resultBudget 默认 100KB、grok 默认 20000 字符中间截断。这里取 50KB，
// 中间截断保留头尾：头部是命令与数据开头（结构信息），尾部常有错误与统计。
const (
	defaultMaxResultBytes = 50000
	resultHeadBytes       = 40000
	resultTailBytes       = 10000
)

// contentLimit 返回一次调用的结果字节上限：工具实现 ContentLimiter 用其
// 声明值；否则按默认上限兜底。
func contentLimit(tl tool.Tool) int {
	if tl == nil {
		return defaultMaxResultBytes
	}
	if cl, ok := tl.(tool.ContentLimiter); ok {
		if n := cl.ContentLimit(); n != 0 {
			return n
		}
	}
	return defaultMaxResultBytes
}

// limitToolResult 对超限的结果做中间截断（保头保尾，不切断 UTF-8），
// 并插入自述被截断事实的标记。Images 不受影响。limit < 0 表示不限。
func limitToolResult(res tool.Result, limit int) tool.Result {
	if limit < 0 || len(res.Content) <= limit {
		return res
	}
	original := len(res.Content)
	head := utf8SafeCut(res.Content, resultHeadBytes)
	tail := utf8SafeTail(res.Content, resultTailBytes)
	marker := fmt.Sprintf(
		"\n[... %d bytes truncated (output was %d bytes, limit %d). Re-run with a narrower query, or read the source with offset/limit to retrieve the missing middle part. ...]\n",
		original-len(head)-len(tail), original, limit,
	)
	res.Content = head + marker + tail
	res.Truncated = true
	return res
}

// utf8SafeCut 返回 s 的前 max 字节，回退到 UTF-8 字符边界。
func utf8SafeCut(s string, max int) string {
	if max >= len(s) {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.ToValidUTF8(s[:cut], "")
}

// utf8SafeTail 返回 s 的最后 max 字节，回退到 UTF-8 字符边界。
func utf8SafeTail(s string, max int) string {
	if max >= len(s) {
		return s
	}
	start := len(s) - max
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}
