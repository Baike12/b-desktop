package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Baike12/b-desktop/internal/llm"
	"github.com/Baike12/b-desktop/internal/session"
)

// RewindPointDTO 是一个可回溯的历史用户消息锚点。
type RewindPointDTO struct {
	MessageID string `json:"messageId"`
	// Preview 是消息文本预览（截断到 160 字符）。
	Preview string `json:"preview"`
	// ImageCount 是消息携带的图片数。
	ImageCount int   `json:"imageCount"`
	TimeMs     int64 `json:"timeMs"`
}

// RewindResultDTO 是回溯结果：被回退消息的完整内容，前端据此回填输入框。
type RewindResultDTO struct {
	MessageID string        `json:"messageId"`
	Text      string        `json:"text"`
	Images    []RewindImage `json:"images,omitempty"`
}

// RewindImage 是回退消息里的一张图片。
type RewindImage struct {
	MediaType string `json:"mediaType"`
	Data      string `json:"data"`
}

// maxRewindPreview 是锚点列表预览的字符上限。
const maxRewindPreview = 160

// ListRewindPoints 返回当前 transcript 里全部用户消息（按时间正序），
// 前端据此渲染回溯选择器。system-reminder 附件块（@ 引用、/ 命令展开）
// 与纯图片消息的锚点按 cc 的行为跳过：回溯锚点是用户自己的输入。
func (a *App) ListRewindPoints(sessionID string) ([]RewindPointDTO, error) {
	store, _, err := a.sessionStore()
	if err != nil {
		return nil, err
	}
	if err := session.ValidateID(sessionID); err != nil {
		return nil, fmt.Errorf("app: list rewind points: %w", err)
	}
	recs, err := store.Load(a.lifeCtx(), sessionID)
	if err != nil && !errors.Is(err, session.ErrCorruptLines) {
		return nil, fmt.Errorf("app: list rewind points: %w", err)
	}
	out := make([]RewindPointDTO, 0, 8)
	for _, rec := range recs {
		if rec.Type != session.RecordMessage || rec.Message == nil || rec.Message.Role != llm.RoleUser {
			continue
		}
		text := userPromptText(rec.Message)
		if strings.TrimSpace(text) == "" {
			continue
		}
		out = append(out, RewindPointDTO{
			MessageID:  rec.Message.ID,
			Preview:    truncateRunes(strings.TrimSpace(text), maxRewindPreview),
			ImageCount: userImageCount(rec.Message),
			TimeMs:     unixMilli(rec.Message.Time),
		})
	}
	return out, nil
}

// RewindSession 把会话回退到指定用户消息之前：该消息及其后的全部记录作废，
// 消息内容返回给前端回填输入框。运行中的会话拒绝回溯（前端先停止本轮）。
// transcript 整体替换后，引擎实例作废，下一轮发送时按截断后的历史重建。
func (a *App) RewindSession(sessionID, messageID string) (RewindResultDTO, error) {
	store, _, err := a.sessionStore()
	if err != nil {
		return RewindResultDTO{}, err
	}
	if err := session.ValidateID(sessionID); err != nil {
		return RewindResultDTO{}, fmt.Errorf("app: rewind session: %w", err)
	}
	rt, err := a.runtimeOf(sessionID)
	if err != nil {
		return RewindResultDTO{}, err
	}

	rt.mu.Lock()
	closed, running := rt.closed, rt.running
	rt.mu.Unlock()
	switch {
	case closed:
		return RewindResultDTO{}, ErrSessionClosed
	case running:
		return RewindResultDTO{}, fmt.Errorf("%w: stop the running turn first", ErrTurnRunning)
	}

	// 截断前先取出锚点消息内容：RewindBefore 之后记录已不可读。
	rewound, err := rewindMessageContent(store, a.lifeCtx(), sessionID, messageID)
	if err != nil {
		return RewindResultDTO{}, err
	}
	if err := store.RewindBefore(a.lifeCtx(), sessionID, messageID); err != nil {
		return RewindResultDTO{}, fmt.Errorf("app: rewind session: %w", err)
	}

	// 引擎内存历史仍是截断前的：作废实例并强制下一轮重建（Restore 重放截断后的 transcript）。
	rt.mu.Lock()
	engine := rt.engine
	if rt.engine != nil {
		rt.engine = nil
		rt.engineGen = -1
	}
	rt.mu.Unlock()
	if engine != nil {
		_ = engine.Close()
	}
	return rewound, nil
}

// rewindMessageContent 从 transcript 找到锚点消息并提取文本与图片。
func rewindMessageContent(store session.Store, ctx context.Context, sessionID, messageID string) (RewindResultDTO, error) {
	recs, err := store.Load(ctx, sessionID)
	if err != nil && !errors.Is(err, session.ErrCorruptLines) {
		return RewindResultDTO{}, fmt.Errorf("app: rewind session: %w", err)
	}
	for _, rec := range recs {
		if rec.Type != session.RecordMessage || rec.Message == nil || rec.Message.ID != messageID {
			continue
		}
		if rec.Message.Role != llm.RoleUser {
			return RewindResultDTO{}, fmt.Errorf("app: rewind session: %w: %s is not a user message", ErrInvalidOption, messageID)
		}
		return RewindResultDTO{
			MessageID: messageID,
			Text:      userPromptText(rec.Message),
			Images:    userMessageImages(rec.Message),
		}, nil
	}
	return RewindResultDTO{}, fmt.Errorf("app: rewind session: %w: %s", session.ErrMessageNotFound, messageID)
}

// userPromptText 拼接用户消息的普通文本块；system-reminder 附件块不计入。
func userPromptText(msg *llm.Message) string {
	parts := make([]string, 0, len(msg.Blocks))
	for _, block := range msg.Blocks {
		if block.Type != llm.BlockText || strings.Contains(block.Text, "<system-reminder>") {
			continue
		}
		parts = append(parts, block.Text)
	}
	return strings.Join(parts, "\n")
}

// userImageCount 统计用户消息里的图片块。
func userImageCount(msg *llm.Message) int {
	count := 0
	for _, block := range msg.Blocks {
		if block.Type == llm.BlockImage && block.Data != "" {
			count++
		}
	}
	return count
}

// userMessageImages 提取用户消息里的全部图片。
func userMessageImages(msg *llm.Message) []RewindImage {
	out := make([]RewindImage, 0, userImageCount(msg))
	for _, block := range msg.Blocks {
		if block.Type == llm.BlockImage && block.Data != "" {
			out = append(out, RewindImage{MediaType: block.MediaType, Data: block.Data})
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
