package app

import (
	"fmt"

	"github.com/Baike12/b-desktop/internal/agent"
	"github.com/Baike12/b-desktop/internal/session"
)

// CompactResultDTO 是手动压缩的结果视图。
type CompactResultDTO struct {
	PreTokens  int    `json:"preTokens"`
	PostTokens int    `json:"postTokens"`
	Summary    string `json:"summary,omitempty"`
}

// CompactSession 手动压缩会话上下文（用户输入 /compact）。压缩期间与发起
// 新一轮互斥；过程通过 compact_start / compact_end 事件同步给前端，返回值
// 只带压缩前后的 token 规模。
func (a *App) CompactSession(sessionID string) (CompactResultDTO, error) {
	if err := session.ValidateID(sessionID); err != nil {
		return CompactResultDTO{}, fmt.Errorf("app: compact session: %w", err)
	}
	if a.isClosed() {
		return CompactResultDTO{}, ErrClosed
	}
	rt, err := a.runtimeForSend(sessionID)
	if err != nil {
		return CompactResultDTO{}, err
	}
	cfg, gen := a.configSnapshot()
	rt.mu.Lock()
	if rt.closed {
		rt.mu.Unlock()
		return CompactResultDTO{}, ErrSessionClosed
	}
	if rt.running {
		rt.mu.Unlock()
		return CompactResultDTO{}, ErrTurnRunning
	}
	engine, err := a.ensureEngineLocked(rt, cfg, gen)
	rt.mu.Unlock()
	if err != nil {
		return CompactResultDTO{}, err
	}

	base := a.lifeCtx()
	a.emitter.Emit(base, agent.Event{
		SessionID: sessionID,
		Type:      agent.EventCompactStart,
		Compact:   &agent.CompactView{Trigger: "manual"},
	})
	view, err := engine.Compact(base)
	end := agent.Event{
		SessionID: sessionID,
		Type:      agent.EventCompactEnd,
		Compact: &agent.CompactView{
			Trigger:    "manual",
			PreTokens:  view.PreTokens,
			PostTokens: view.PostTokens,
		},
	}
	if err != nil {
		end.Compact.Failed = true
		end.Compact.Error = err.Error()
	}
	a.emitter.Emit(base, end)
	if err != nil {
		return CompactResultDTO{}, fmt.Errorf("app: compact session: %w", err)
	}
	return CompactResultDTO{PreTokens: view.PreTokens, PostTokens: view.PostTokens}, nil
}
