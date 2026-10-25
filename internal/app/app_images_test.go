package app

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Baike12/b-desktop/internal/agent"
	"github.com/Baike12/b-desktop/internal/llm"
	"github.com/Baike12/b-desktop/internal/session"
)

// pngMagic 是能通过 magic bytes 识别的最小 PNG 头。
var pngMagic = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}

func pngImageInput() ImageInput {
	return ImageInput{MediaType: "image/png", Data: base64.StdEncoding.EncodeToString(pngMagic)}
}

func TestSendMessageImageValidation(t *testing.T) {
	app, _ := newTestApp(t, newFakeProvider())

	// 文本与图片不允许同时为空。
	if _, err := app.SendMessage("s1", "   ", nil); !errors.Is(err, ErrEmptyPrompt) {
		t.Fatalf("empty input error = %v", err)
	}
	// 纯图片输入放行（在会话校验之前先失败会掩盖图片路径，这里用合法会话验证）。
	if _, err := app.SendMessage("bad/id", "", []ImageInput{pngImageInput()}); !errors.Is(err, session.ErrInvalidID) {
		t.Fatalf("纯图片输入也应先过会话校验: %v", err)
	}

	// 数量上限。
	many := make([]ImageInput, llm.MaxImagesPerMessage+1)
	for i := range many {
		many[i] = pngImageInput()
	}
	if _, err := app.SendMessage("s1", "hi", many); !errors.Is(err, ErrTooManyImages) {
		t.Fatalf("too many images error = %v", err)
	}

	// 非法图片。
	if _, err := app.SendMessage("s1", "hi", []ImageInput{{Data: "!!!bad!!!"}}); !errors.Is(err, ErrInvalidImage) {
		t.Fatalf("invalid image error = %v", err)
	}
	payload := base64.StdEncoding.EncodeToString([]byte("plain text file"))
	if _, err := app.SendMessage("s1", "hi", []ImageInput{{Data: payload}}); !errors.Is(err, ErrInvalidImage) {
		t.Fatalf("non-image bytes error = %v", err)
	}
}

func TestSendMessageWithImagesRunsTurn(t *testing.T) {
	provider := newFakeProvider(textStep("m1", "好的，已看到图片"))
	app, collector := newTestApp(t, provider)
	info := createTestSession(t, app)

	result, err := app.SendMessage(info.ID, "看这张图", []ImageInput{pngImageInput()})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if !result.Running {
		t.Fatalf("result = %+v", result)
	}
	collector.waitFor(t, agent.EventTurnEnd, 3*time.Second)

	calls := provider.requests()
	if len(calls) != 1 {
		t.Fatalf("model calls = %d", len(calls))
	}
	var user *llm.Message
	for i := range calls[0].Messages {
		if calls[0].Messages[i].Role == llm.RoleUser {
			user = &calls[0].Messages[i]
			break
		}
	}
	if user == nil || len(user.Blocks) != 2 ||
		user.Blocks[0].Text != "看这张图" ||
		user.Blocks[1].Type != llm.BlockImage || user.Blocks[1].MediaType != "image/png" {
		t.Fatalf("user 消息应含 1 文本 + 1 图片: %+v", user)
	}
}

func TestImageInputOversizeRejected(t *testing.T) {
	app, _ := newTestApp(t, newFakeProvider())
	huge := "Z" + strings.Repeat("A", llm.MaxImageBase64Bytes)
	if _, err := app.SendMessage("s1", "hi", []ImageInput{{Data: huge}}); !errors.Is(err, ErrInvalidImage) {
		t.Fatalf("oversize image error = %v", err)
	}
}
