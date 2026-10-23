package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/Baike12/b-desktop/internal/llm"
	"github.com/Baike12/b-desktop/internal/session"
)

// 自动标题参数。
const (
	// defaultSessionTitle 是新建会话的占位标题；仍是这个标题的会话才会被自动命名。
	defaultSessionTitle = "新会话"
	// sessionTitleTimeout 是生成标题的模型调用上限。超时后回退到用户首条消息。
	sessionTitleTimeout = 20 * time.Second
	// sessionTitleWriteTimeout 是标题记录的落盘上限。
	sessionTitleWriteTimeout = 5 * time.Second
	// sessionTitleMaxRunes 是展示用标题的最大字符数。
	sessionTitleMaxRunes = 24
	// sessionTitlePromptRunes 是送给模型的首条消息长度上限。
	sessionTitlePromptRunes = 2000
)

// sessionTitleSystemPrompt 约束标题的格式与语言，避免模型输出解释或多行内容。
const sessionTitleSystemPrompt = "你为编码 Agent 的会话生成标题。标题要具体、能区分不同会话，概括用户要做的事。" +
	"只输出标题本身：不超过 20 个汉字或 40 个字符，不要引号、书名号、结尾标点，不要解释。" +
	"使用与用户消息相同的语言。"

// SessionTitleDTO 是 "session:title" 事件的载荷。
type SessionTitleDTO struct {
	SessionID string `json:"sessionId"`
	Title     string `json:"title"`
}

// EmitSessionTitle 通知前端会话标题已更新。它复用 Agent 事件的发送通道，
// 但不分配 seq：标题更新与流式事件无关，不需要参与前端的顺序归并。
func (e *Emitter) EmitSessionTitle(ctx context.Context, payload SessionTitleDTO) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.snd != nil {
		e.snd(ctx, EventNameSessionTitle, payload)
	}
}

// startSessionTitle 在会话首次发送消息时安排一次标题生成。
// 调用方必须持有 rt.mu，且已确认没有进行中的轮次；本方法只做一次
// transcript 读取并启动后台 goroutine，不阻塞 SendMessage。
func (a *App) startSessionTitle(rt *sessionRuntime, prompt string, cfg Config) {
	if rt.titleChecked {
		return
	}
	rt.titleChecked = true
	if title, ok := a.currentSessionTitle(rt.id); ok && !isDefaultSessionTitle(title) {
		return
	}
	fallback := fallbackSessionTitle(prompt)
	if fallback == "" {
		return
	}
	sessionID := rt.id
	go a.generateSessionTitle(sessionID, prompt, fallback, cfg)
}

// currentSessionTitle 从 transcript 读取当前标题；读取失败时 ok 为 false，
// 由调用方按「未命名」处理。
func (a *App) currentSessionTitle(sessionID string) (string, bool) {
	store, _, err := a.sessionStore()
	if err != nil {
		return "", false
	}
	recs, err := store.Load(a.lifeCtx(), sessionID)
	if err != nil && !errors.Is(err, session.ErrCorruptLines) {
		return "", false
	}
	loaded, err := session.Replay(recs)
	if err != nil {
		return "", false
	}
	return loaded.Meta.Title, true
}

// titleScheduled 报告该会话已安排过自动标题（模型结果可能尚未落盘）。
func (rt *sessionRuntime) titleScheduled() bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.titleChecked
}

// deriveSessionTitle 为没有标题记录的旧会话从首条用户消息生成标题并落盘。
// 只读取一次 transcript；读取或写入失败时保留占位标题。
func (a *App) deriveSessionTitle(store session.Store, sessionID string) (string, bool) {
	recs, err := store.Load(a.lifeCtx(), sessionID)
	if err != nil && !errors.Is(err, session.ErrCorruptLines) {
		return "", false
	}
	loaded, err := session.Replay(recs)
	if err != nil {
		return "", false
	}
	first := ""
	for _, msg := range loaded.Messages {
		if msg.Role != llm.RoleUser {
			continue
		}
		for _, block := range msg.Blocks {
			if block.Type == llm.BlockText && strings.TrimSpace(block.Text) != "" {
				first = block.Text
				break
			}
		}
		if first != "" {
			break
		}
	}
	title := fallbackSessionTitle(first)
	if title == "" {
		return "", false
	}
	writeCtx, cancel := context.WithTimeout(context.Background(), sessionTitleWriteTimeout)
	defer cancel()
	if err := session.SetTitle(writeCtx, store, sessionID, title); err != nil {
		return "", false
	}
	return title, true
}

// generateSessionTitle 先请求模型生成标题，失败时使用首条消息的截断文本，
// 落盘后通过 "session:title" 通知前端。
func (a *App) generateSessionTitle(sessionID, prompt, fallback string, cfg Config) {
	title := ""
	if strings.TrimSpace(cfg.Model) != "" {
		ctx, cancel := context.WithTimeout(a.lifeCtx(), sessionTitleTimeout)
		title, _ = a.requestSessionTitle(ctx, cfg, prompt)
		cancel()
	}
	title = normalizeSessionTitle(title, fallback)
	if title == "" {
		return
	}

	store, _, err := a.sessionStore()
	if err != nil {
		return
	}
	// 写入脱离标题请求的 ctx：模型超时不应导致标题丢失。
	writeCtx, cancel := context.WithTimeout(context.Background(), sessionTitleWriteTimeout)
	defer cancel()
	if err := session.SetTitle(writeCtx, store, sessionID, title); err != nil {
		return
	}
	a.emitter.EmitSessionTitle(a.lifeCtx(), SessionTitleDTO{SessionID: sessionID, Title: title})
}

// requestSessionTitle 用当前模型生成一次标题。任何失败都返回空字符串，
// 由调用方回退；标题生成失败不影响正常对话。
func (a *App) requestSessionTitle(ctx context.Context, cfg Config, prompt string) (string, error) {
	provider, err := a.factory(cfg)
	if err != nil {
		return "", err
	}
	// 不设置 Temperature：部分推理模型会拒绝该参数，标题请求只需要稳定输出。
	req := llm.Request{
		Model:  cfg.Model,
		System: sessionTitleSystemPrompt,
		Messages: []llm.Message{{
			Role: llm.RoleUser,
			Blocks: []llm.ContentBlock{{
				Type: llm.BlockText,
				Text: clipRunes(prompt, sessionTitlePromptRunes),
			}},
		}},
		MaxTokens: 64,
	}
	stream, err := provider.Stream(ctx, req)
	if err != nil {
		return "", err
	}
	defer stream.Close()

	var b strings.Builder
	for {
		ev, err := stream.Recv(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		switch ev.Type {
		case llm.EventTextDelta:
			b.WriteString(ev.Text)
			if b.Len() >= 4096 {
				return b.String(), nil
			}
		case llm.EventError:
			if ev.Error != nil {
				return "", ev.Error
			}
		}
	}
	return b.String(), nil
}

// isDefaultSessionTitle 报告标题是否仍为占位值。
func isDefaultSessionTitle(title string) bool {
	title = strings.TrimSpace(title)
	return title == "" || title == defaultSessionTitle
}

// normalizeSessionTitle 清洗模型输出：取首行、去引号与常见前缀、压平空白并限长。
// 模型输出为空或全被清洗掉时使用 fallback。
func normalizeSessionTitle(raw, fallback string) string {
	title := strings.TrimSpace(raw)
	if i := strings.IndexAny(title, "\r\n"); i >= 0 {
		title = title[:i]
	}
	title = strings.TrimSpace(title)
	title = strings.Trim(title, "\"'`“”‘’《》〈〉[]()（）")
	for _, prefix := range []string{"标题：", "标题:", "Title:", "title:"} {
		if strings.HasPrefix(title, prefix) {
			title = strings.TrimSpace(strings.TrimPrefix(title, prefix))
			break
		}
	}
	title = strings.TrimRight(title, "。．.!！?？;；,，、")
	title = strings.Join(strings.Fields(title), " ")
	if title == "" {
		title = fallback
	}
	return truncateTitle(title)
}

// fallbackSessionTitle 用首条消息的第一行生成标题，符合「模型不可用也能有名字」。
func fallbackSessionTitle(prompt string) string {
	text := strings.TrimSpace(prompt)
	if text == "" {
		return ""
	}
	if i := strings.IndexAny(text, "\r\n"); i >= 0 {
		text = text[:i]
	}
	text = strings.TrimSpace(text)
	text = strings.TrimLeft(text, "#>*-+• \t")
	text = strings.Trim(text, "\"'`“”‘’")
	text = strings.Join(strings.Fields(text), " ")
	return truncateTitle(text)
}

// truncateTitle 把标题限制在 sessionTitleMaxRunes 个字符内。
func truncateTitle(title string) string {
	runes := []rune(title)
	if len(runes) <= sessionTitleMaxRunes {
		return title
	}
	return strings.TrimSpace(string(runes[:sessionTitleMaxRunes])) + "…"
}

// clipRunes 截断字符串到 n 个字符。
func clipRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
