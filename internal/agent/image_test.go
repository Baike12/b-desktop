package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Baike12/b-desktop/internal/llm"
	"github.com/Baike12/b-desktop/internal/tool"
)

// pngMagic 是能通过 magic bytes 识别的最小 PNG 头。
var pngMagic = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}

func TestRunCarriesImagesIntoUserMessage(t *testing.T) {
	h := newHarness(t, harnessOptions{steps: []fakeStep{textStep("m1", "he", "llo")}})
	rec := newRecorder()

	b64 := base64.StdEncoding.EncodeToString(pngMagic)
	res, err := h.engine.Run(context.Background(), Input{
		TurnID: "t1",
		Prompt: "看这张图",
		Images: []llm.ContentBlock{
			llm.ImageBlock("image/png", b64),
			{Type: llm.BlockImage, MediaType: "image/png", Data: ""}, // 空图片应被丢弃
		},
	}, rec.sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != StopCompleted {
		t.Fatalf("StopReason = %s", res.StopReason)
	}

	h.provider.mu.Lock()
	calls := h.provider.calls
	h.provider.mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("模型调用次数 = %d, want 1", len(calls))
	}
	var user *llm.Message
	for i := range calls[0].Messages {
		if calls[0].Messages[i].Role == llm.RoleUser {
			user = &calls[0].Messages[i]
			break
		}
	}
	if user == nil {
		t.Fatal("请求中缺少 user 消息")
	}
	// 文本在前、图片在后；空数据图片被过滤。
	if len(user.Blocks) != 2 ||
		user.Blocks[0].Type != llm.BlockText || user.Blocks[0].Text != "看这张图" ||
		user.Blocks[1].Type != llm.BlockImage || user.Blocks[1].Data != b64 {
		t.Fatalf("user 消息块不正确: %+v", user.Blocks)
	}

	// transcript 必须原样持久化图片块（base64 进 JSONL，与参考实现一致）。
	recs, err := h.store.Load(context.Background(), "s1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	found := false
	for _, r := range recs {
		if r.Message == nil || r.Message.Role != llm.RoleUser {
			continue
		}
		for _, b := range r.Message.Blocks {
			if b.Type == llm.BlockImage && strings.Contains(b.Data, b64) {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("transcript 中未持久化图片块")
	}
}

func TestRunCarriesImageOnlyInput(t *testing.T) {
	h := newHarness(t, harnessOptions{steps: []fakeStep{textStep("m1", "ok", "")}})
	rec := newRecorder()

	b64 := base64.StdEncoding.EncodeToString(pngMagic)
	if _, err := h.engine.Run(context.Background(), Input{
		TurnID: "t1",
		Images: []llm.ContentBlock{llm.ImageBlock("image/png", b64)},
	}, rec.sink); err != nil {
		t.Fatalf("Run: %v", err)
	}

	h.provider.mu.Lock()
	calls := h.provider.calls
	h.provider.mu.Unlock()
	blocks := calls[0].Messages[0].Blocks
	if len(blocks) != 1 || blocks[0].Type != llm.BlockImage {
		t.Fatalf("纯图片输入应只含一个 image 块: %+v", blocks)
	}
}

func TestToolResultCarriesImages(t *testing.T) {
	show := newFakeTool("Show", true, true)
	show.runFn = func(context.Context, json.RawMessage) (tool.Result, error) {
		return tool.Result{
			Content: "[Image: a.png]",
			Images:  []tool.Image{{MediaType: "image/png", Data: string(pngMagic)}},
		}, nil
	}
	h := newHarness(t, harnessOptions{
		tools: []tool.Tool{show},
		steps: []fakeStep{
			toolUseStep("m1", plannedCall{id: "t1", name: "Show", input: `{"path":"a.png"}`}),
			textStep("m2", "done"),
		},
	})
	rec := newRecorder()

	if _, err := h.engine.Run(context.Background(), Input{TurnID: "t1", Prompt: "show"}, rec.sink); err != nil {
		t.Fatalf("Run: %v", err)
	}

	calls := h.provider.Calls()
	var tr *llm.ContentBlock
	for _, m := range calls[1].Messages {
		for i := range m.Blocks {
			if m.Blocks[i].Type == llm.BlockToolResult {
				tr = &m.Blocks[i]
			}
		}
	}
	if tr == nil || tr.Text != "[Image: a.png]" || len(tr.Images) != 1 || tr.Images[0].MediaType != "image/png" {
		t.Fatalf("tool_result 应携带图片: %+v", tr)
	}
}
