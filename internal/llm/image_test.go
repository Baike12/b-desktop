package llm

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// tinyPNG 是 1x1 像素的合法 PNG 文件头部分（足够通过 magic bytes 识别）。
var tinyPNG = []byte{
	0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D,
}

func TestSniffImageMediaType(t *testing.T) {
	cases := []struct {
		data []byte
		want string
	}{
		{tinyPNG, "image/png"},
		{[]byte{0xFF, 0xD8, 0xFF, 0xE0}, "image/jpeg"},
		{[]byte("GIF89a....."), "image/gif"},
		{append([]byte("RIFF####WEBP"), make([]byte, 4)...), "image/webp"},
		{[]byte("<svg>not raster</svg>"), ""},
		{nil, ""},
	}
	for _, tc := range cases {
		if got := SniffImageMediaType(tc.data); got != tc.want {
			t.Errorf("SniffImageMediaType(% x) = %q, want %q", tc.data, got, tc.want)
		}
	}
}

func TestNormalizeImageInput(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString(tinyPNG)

	t.Run("raw base64 with wrong mediaType is corrected by magic bytes", func(t *testing.T) {
		mt, data, err := NormalizeImageInput("image/jpeg", b64)
		if err != nil {
			t.Fatalf("NormalizeImageInput: %v", err)
		}
		if mt != "image/png" || data != b64 {
			t.Fatalf("got %q %q", mt, data)
		}
	})

	t.Run("data URL payload is stripped and typed", func(t *testing.T) {
		mt, data, err := NormalizeImageInput("", "data:image/png;base64,"+b64)
		if err != nil {
			t.Fatalf("NormalizeImageInput: %v", err)
		}
		if mt != "image/png" || data != b64 {
			t.Fatalf("got %q %q", mt, data)
		}
	})

	t.Run("invalid base64 rejected", func(t *testing.T) {
		if _, _, err := NormalizeImageInput("image/png", "!!!not base64!!!"); err == nil {
			t.Fatal("expected error for invalid base64")
		}
	})

	t.Run("non-image bytes rejected", func(t *testing.T) {
		payload := base64.StdEncoding.EncodeToString([]byte("just text, no image here"))
		if _, _, err := NormalizeImageInput("", payload); err == nil {
			t.Fatal("expected error for non-image payload")
		}
	})

	t.Run("oversize rejected", func(t *testing.T) {
		huge := strings.Repeat("A", MaxImageBase64Bytes+1)
		if _, _, err := NormalizeImageInput("image/png", huge); err == nil {
			t.Fatal("expected error for oversize image")
		}
	})

	t.Run("empty data rejected", func(t *testing.T) {
		if _, _, err := NormalizeImageInput("image/png", "  "); err == nil {
			t.Fatal("expected error for empty data")
		}
	})
}

func TestAnthropicImageEncoding(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString(tinyPNG)
	req := Request{
		Model:     "claude-sonnet-4",
		MaxTokens: 1024,
		Messages: []Message{
			{Role: RoleUser, Blocks: []ContentBlock{
				{Type: BlockText, Text: "看这张图"},
				{Type: BlockImage, MediaType: "image/png", Data: b64},
				// 空数据图片应被丢弃而不是发出坏块。
				{Type: BlockImage, MediaType: "image/png", Data: ""},
				{Type: BlockToolResult, ToolUseID: "toolu_1", Text: "截图如下",
					Images: []Image{{MediaType: "image/png", Data: b64}, {MediaType: "image/png", Data: ""}}},
			}},
		},
	}

	payload, err := encodeAnthropicRequest(req)
	if err != nil {
		t.Fatalf("encodeAnthropicRequest: %v", err)
	}
	var out struct {
		Messages []struct {
			Content []json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("请求体不是合法 JSON: %v", err)
	}
	if len(out.Messages) != 1 || len(out.Messages[0].Content) != 3 {
		t.Fatalf("应为 3 个有效块（空图片被丢弃），得到 %+v", out.Messages)
	}

	var img struct {
		Type   string `json:"type"`
		Source struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		} `json:"source"`
	}
	if err := json.Unmarshal(out.Messages[0].Content[1], &img); err != nil {
		t.Fatalf("image 块解码失败: %v", err)
	}
	if img.Type != "image" || img.Source.Type != "base64" || img.Source.MediaType != "image/png" || img.Source.Data != b64 {
		t.Fatalf("image 块编码不正确: %+v", img)
	}

	var tr struct {
		Type      string            `json:"type"`
		ToolUseID string            `json:"tool_use_id"`
		Content   []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(out.Messages[0].Content[2], &tr); err != nil {
		t.Fatalf("tool_result 块解码失败: %v", err)
	}
	if tr.Type != "tool_result" || tr.ToolUseID != "toolu_1" || len(tr.Content) != 2 {
		t.Fatalf("tool_result 应为 1 文本 + 1 图片，得到 %+v", tr)
	}
	var part struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(tr.Content[0], &part); err != nil || part.Type != "text" {
		t.Fatalf("tool_result 首块应为 text: %v %+v", err, part)
	}
	if err := json.Unmarshal(tr.Content[1], &part); err != nil || part.Type != "image" {
		t.Fatalf("tool_result 第二块应为 image: %v %+v", err, part)
	}
}

func TestOpenAIImageEncoding(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString(tinyPNG)
	req := Request{
		Model: "gpt-4o",
		Messages: []Message{
			{Role: RoleUser, Blocks: []ContentBlock{
				{Type: BlockText, Text: "看这张图"},
				{Type: BlockImage, MediaType: "image/png", Data: b64},
			}},
			{Role: RoleAssistant, Blocks: []ContentBlock{
				{Type: BlockToolUse, ToolUseID: "call_1", ToolName: "Read", Input: json.RawMessage(`{"file_path":"a.png"}`)},
			}},
			{Role: RoleUser, Blocks: []ContentBlock{
				{Type: BlockToolResult, ToolUseID: "call_1", Text: "截图如下",
					Images: []Image{{MediaType: "image/png", Data: b64}}},
			}},
			// 纯文本消息必须保持字符串形式。
			{Role: RoleUser, Blocks: []ContentBlock{{Type: BlockText, Text: "纯文本"}}},
		},
	}

	payload, err := encodeOpenAIRequest(req)
	if err != nil {
		t.Fatalf("encodeOpenAIRequest: %v", err)
	}
	var out openAIRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("请求体不是合法 JSON: %v", err)
	}
	// system 为空，消息顺序：user(parts) / assistant / tool / user(text)。
	if len(out.Messages) != 4 {
		t.Fatalf("应为 4 条消息，得到 %d: %+v", len(out.Messages), out.Messages)
	}

	user, ok := out.Messages[0].Content.([]any)
	if !ok {
		t.Fatalf("带图用户消息应为 parts 数组，得到 %#v", out.Messages[0].Content)
	}
	if len(user) != 2 {
		t.Fatalf("应为 text + image_url 两个 part: %#v", user)
	}
	first, _ := user[0].(map[string]any)
	if first["type"] != "text" || first["text"] != "看这张图" {
		t.Fatalf("首 part 应为 text: %#v", first)
	}
	second, _ := user[1].(map[string]any)
	url, _ := second["image_url"].(map[string]any)
	if second["type"] != "image_url" || url["url"] != "data:image/png;base64,"+b64 {
		t.Fatalf("image_url part 编码不正确: %#v", second)
	}

	tool, ok := out.Messages[2].Content.([]any)
	if !ok || len(tool) != 2 {
		t.Fatalf("带图工具结果应为 parts 数组: %#v", out.Messages[2].Content)
	}

	text, ok := out.Messages[3].Content.(string)
	if !ok || text != "纯文本" {
		t.Fatalf("纯文本用户消息应保持字符串: %#v", out.Messages[3].Content)
	}
}
