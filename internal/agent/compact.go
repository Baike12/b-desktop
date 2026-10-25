package agent

import (
	"context"
	"fmt"

	sysctx "github.com/Baike12/b-desktop/internal/context"
	"github.com/Baike12/b-desktop/internal/session"
)

// Compact 手动压缩当前会话历史：生成摘要、替换内存历史并写入压缩边界，
// 与 Run 通过 runMu 互斥。没有可压缩内容（历史为空、只剩尾部保留段）或
// 压缩已熔断时返回 Compactor 的错误。与自动压缩一致：内存历史替换成功后
// 才落盘，落盘失败不回滚内存历史，只把错误返回给调用方。
func (e *Engine) Compact(ctx context.Context) (CompactView, error) {
	if !e.runMu.TryLock() {
		return CompactView{Trigger: "manual"}, ErrTurnRunning
	}
	defer e.runMu.Unlock()

	history := e.historySnapshot()
	info, msgs, err := e.cfg.Compactor.AutoCompact(ctx, history, "manual", nil)
	if err != nil {
		return CompactView{Trigger: "manual", Failed: true, Error: err.Error()}, err
	}
	e.replaceHistory(msgs)
	// 压缩已替换历史，旧的最近用量描述的是压缩前的规模，不再适用。
	e.resetLastUsage()

	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
	defer cancel()
	if err := e.appendCompactRecords(persistCtx, info); err != nil {
		return CompactView{Trigger: "manual", PreTokens: info.PreTokens, PostTokens: info.PostTokens}, err
	}
	return CompactView{
		Trigger:    "manual",
		PreTokens:  info.PreTokens,
		PostTokens: info.PostTokens,
	}, nil
}

// appendCompactRecords 写入压缩边界记录与保留的最近消息，保证会话重放时
// 「摘要 + 边界之后的消息」自洽。
func (e *Engine) appendCompactRecords(ctx context.Context, info sysctx.CompactInfo) error {
	if err := e.appendRecord(ctx, session.Record{
		Type: session.RecordCompact,
		Compact: &session.Compact{
			Trigger:    info.Trigger,
			PreTokens:  info.PreTokens,
			PostTokens: info.PostTokens,
			Summary:    info.Summary,
			Model:      info.Model,
		},
	}); err != nil {
		return err
	}
	for _, m := range info.KeptMessages {
		msg := m
		if err := e.appendRecord(ctx, session.Record{Type: session.RecordMessage, Message: &msg}); err != nil {
			return err
		}
	}
	if err := e.cfg.Store.Flush(ctx); err != nil {
		return fmt.Errorf("agent: flush compact: %w", err)
	}
	return nil
}

// appendRecord 持久化一条记录。Engine 层的落盘不绑定某一轮，时间取引擎时间源。
func (e *Engine) appendRecord(ctx context.Context, rec session.Record) error {
	rec.SessionID = e.cfg.SessionID
	if rec.Time.IsZero() {
		rec.Time = e.now()
	}
	if err := e.cfg.Store.Append(ctx, rec); err != nil {
		return fmt.Errorf("agent: append %s: %w", rec.Type, err)
	}
	return nil
}
