package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sysctx "github.com/Baike12/b-desktop/internal/context"
	"github.com/Baike12/b-desktop/internal/llm"
	"github.com/Baike12/b-desktop/internal/tool"
)

// TestLimitToolResult 验证兜底截断：超限结果保头保尾、插入自述标记并置 Truncated。
func TestLimitToolResult(t *testing.T) {
	long := strings.Repeat("a", 60000)
	got := limitToolResult(toolResult(long), defaultMaxResultBytes)
	if !got.Truncated {
		t.Fatal("超限结果未标记 Truncated")
	}
	if len(got.Content) >= 60000 {
		t.Fatalf("截断后长度 %d 应小于原长 60000", len(got.Content))
	}
	if !strings.HasPrefix(got.Content, strings.Repeat("a", 100)) {
		t.Fatal("截断结果未保留头部")
	}
	if !strings.HasSuffix(strings.TrimRight(got.Content, "\n"), strings.Repeat("a", 100)) {
		t.Fatalf("截断结果未保留尾部: %q", got.Content[len(got.Content)-120:])
	}
	if !strings.Contains(got.Content, "bytes truncated") || !strings.Contains(got.Content, "60000") {
		t.Fatalf("截断标记缺少事实自述: %q", got.Content)
	}

	// 未超限、负上限、错误结果：原样返回。
	same := toolResult("short")
	if got := limitToolResult(same, defaultMaxResultBytes); got.Content != "short" || got.Truncated {
		t.Fatalf("未超限结果被改动: %+v", got)
	}
	if got := limitToolResult(toolResult(long), -1); got.Content != long {
		t.Fatal("负上限应表示不限")
	}
}

// TestLimitToolResultUTF8Boundary 验证截断不切断多字节字符。
func TestLimitToolResultUTF8Boundary(t *testing.T) {
	// 头部边界落在多字节字符中间：39999 个 ASCII + 三个汉字。
	body := strings.Repeat("a", resultHeadBytes-1) + "中文"
	res := toolResult(body + strings.Repeat("b", 5000))
	got := limitToolResult(res, defaultMaxResultBytes)
	if !strings.Contains(got.Content, "中文") {
		t.Fatalf("头部多字节字符被破坏: %q", got.Content[:resultHeadBytes+80])
	}
}

// TestContentLimitOverride 验证 ContentLimiter 接口覆盖默认上限。
type limitedTool struct {
	*fakeTool
	limit int
}

func (l limitedTool) ContentLimit() int { return l.limit }

func TestContentLimitOverride(t *testing.T) {
	if got := contentLimit(nil); got != defaultMaxResultBytes {
		t.Fatalf("nil tool limit = %d, want %d", got, defaultMaxResultBytes)
	}
	if got := contentLimit(newFakeTool("X", false, false)); got != defaultMaxResultBytes {
		t.Fatalf("默认 limit = %d, want %d", got, defaultMaxResultBytes)
	}
	if got := contentLimit(limitedTool{limit: 100}); got != 100 {
		t.Fatalf("接口覆盖 limit = %d, want 100", got)
	}
	if got := contentLimit(limitedTool{limit: -1}); got != -1 {
		t.Fatalf("接口负 limit = %d, want -1", got)
	}
}

// TestHugeToolResultTruncatedInHistory 验证一轮执行中超出默认上限的工具结果
// 以截断形态进入历史。
func TestHugeToolResultTruncatedInHistory(t *testing.T) {
	huge := strings.Repeat("H", 200000)
	big := newFakeTool("Huge", false, false)
	big.runFn = func(context.Context, json.RawMessage) (tool.Result, error) {
		return tool.Result{Content: huge}, nil
	}
	h := newHarness(t, harnessOptions{
		tools: []tool.Tool{big},
		steps: []fakeStep{
			toolUseStep("m1", plannedCall{id: "t1", name: "Huge", input: `{}`}),
			textStep("m2", "done"),
		},
	})
	rec := newRecorder()
	res, err := h.engine.Run(context.Background(), Input{Prompt: "go"}, rec.sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != StopCompleted {
		t.Fatalf("StopReason = %s, want completed", res.StopReason)
	}
	var result *llm.ContentBlock
	for mi, msg := range h.engine.History() {
		for bi, b := range msg.Blocks {
			if b.Type == llm.BlockToolResult && b.ToolUseID == "t1" {
				result = &h.engine.History()[mi].Blocks[bi]
			}
		}
	}
	if result == nil {
		t.Fatal("历史中找不到工具结果")
	}
	if len(result.Text) >= len(huge) {
		t.Fatalf("结果未截断: %d bytes", len(result.Text))
	}
	if !strings.Contains(result.Text, "bytes truncated") {
		t.Fatalf("结果缺少截断标记")
	}
}

// TestCompactRunsBeforeBlockingLimit 验证阻塞线判定发生在压缩之后：
// 一条巨量历史中间结果把估算推过阻塞线时，先自动压缩自救并继续本轮，
// 而不是直接以 blocking 结束（对齐 cc query.ts 的流水线顺序）。
func TestCompactRunsBeforeBlockingLimit(t *testing.T) {
	huge := strings.Repeat("H", 200000) // ≈50000 tokens，超过 keepMax 会被摘要掉
	steps := []fakeStep{
		toolUseStep("m1", plannedCall{id: "t1", name: "Bash", input: `{}`}),
		toolUseStep("m2", plannedCall{id: "t2", name: "Bash", input: `{}`}),
		toolUseStep("m3", plannedCall{id: "t3", name: "Bash", input: `{}`}),
		toolUseStep("m4", plannedCall{id: "t4", name: "Bash", input: `{}`}),
		textStep("m5", "done"),
	}
	results := []string{"r1", huge, "r3", "r4"}
	i := 0
	bash := newFakeTool("Bash", false, false)
	bash.runFn = func(context.Context, json.RawMessage) (tool.Result, error) {
		r := results[i]
		i++
		return tool.Result{Content: r}, nil
	}
	h := newHarness(t, harnessOptions{
		tools: []tool.Tool{bash},
		steps: steps,
		// 32768 → auto 12288 / blocking 21576（见 BudgetThresholds 用例）。
		budget:     sysctx.Budget{ContextWindow: 32768, MaxOutputTokens: 8192},
		summarizer: fakeSummarizer{summary: "earlier work summarized"},
	})
	rec := newRecorder()
	res, err := h.engine.Run(context.Background(), Input{Prompt: "go"}, rec.sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != StopCompleted {
		t.Fatalf("StopReason = %s (err=%s), want completed: 压缩应先于阻塞线自救", res.StopReason, res.Err)
	}
	if rec.count(EventCompactEnd) == 0 {
		t.Fatal("没有发生压缩")
	}
	for _, msg := range h.engine.History() {
		for _, b := range msg.Blocks {
			if strings.Contains(b.Text, huge) {
				t.Fatal("巨量结果应在压缩中被摘要掉")
			}
		}
	}
}

// toolResult 构造一个普通成功结果。
func toolResult(content string) tool.Result {
	return tool.Result{Content: content}
}
