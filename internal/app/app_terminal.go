package app

import (
	"encoding/base64"
	"fmt"

	"github.com/Baike12/b-desktop/internal/session"
	"github.com/Baike12/b-desktop/internal/terminal"
)

// 终端专属的 Wails 事件名。数据通道高频且独立于 agent:event，
// 不占用 Emitter 的全局 seq（终端输出无需与 Agent 事件排序）。
const (
	// EventNameTerminalData 是 PTY 输出事件，data 为 base64。
	EventNameTerminalData = "terminal:data"
	// EventNameTerminalExit 是终端进程退出事件。
	EventNameTerminalExit = "terminal:exit"
	// EventNameTerminalClosed 是终端被显式关闭的事件。
	EventNameTerminalClosed = "terminal:closed"
)

// TerminalInfoDTO 是终端实例的前端视图。
type TerminalInfoDTO struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	WorkDir   string `json:"workDir"`
}

// terminalComponent 返回进程级终端管理器（惰性创建，跨会话共享）。
func (a *App) terminalComponent() *terminal.Manager {
	value, err := a.extensionStore("terminal-manager", func() (any, error) {
		return terminal.NewManager(nil), nil
	})
	if err != nil {
		return nil
	}
	manager, _ := value.(*terminal.Manager)
	return manager
}

// terminalManagerIfAny 返回已创建的终端管理器；不存在时不创建。
func (a *App) terminalManagerIfAny() *terminal.Manager {
	a.extMu.Lock()
	defer a.extMu.Unlock()
	if value, ok := a.extData["terminal-manager"]; ok {
		manager, _ := value.(*terminal.Manager)
		return manager
	}
	return nil
}

// CreateTerminal 打开（或复用）会话的交互终端，返回实例信息。
// 输出通过 "terminal:data"（base64）推送，进程退出通过 "terminal:exit" 推送。
// 每个会话至多一个终端：重复调用返回同一实例。
func (a *App) CreateTerminal(sessionID string) (TerminalInfoDTO, error) {
	if err := session.ValidateID(sessionID); err != nil {
		return TerminalInfoDTO{}, fmt.Errorf("app: create terminal: %w", err)
	}
	rt, err := a.runtimeForSend(sessionID)
	if err != nil {
		return TerminalInfoDTO{}, err
	}
	workDir := rt.workDirSnapshot()
	if workDir == "" {
		workDir = a.cfg.WorkDir
	}
	manager := a.terminalComponent()
	if manager == nil {
		return TerminalInfoDTO{}, ErrClosed
	}
	if existing, ok := manager.GetBySession(sessionID); ok {
		return TerminalInfoDTO{ID: existing.ID, SessionID: sessionID, WorkDir: existing.WorkDir}, nil
	}

	id := newID("tty")
	sess, err := manager.Open(a.lifeCtx(), id, sessionID, workDir,
		func(data []byte) {
			a.emitRaw(EventNameTerminalData, map[string]string{
				"terminalId": id,
				"sessionId":  sessionID,
				"data":       base64.StdEncoding.EncodeToString(data),
			})
		},
		func(code int, exitErr error) {
			payload := map[string]any{
				"terminalId": id,
				"sessionId":  sessionID,
				"code":       code,
			}
			if exitErr != nil {
				payload["error"] = exitErr.Error()
			}
			a.emitRaw(EventNameTerminalExit, payload)
		},
	)
	if err != nil {
		return TerminalInfoDTO{}, fmt.Errorf("app: create terminal: %w", err)
	}
	return TerminalInfoDTO{ID: sess.ID, SessionID: sessionID, WorkDir: sess.WorkDir}, nil
}

// TerminalWrite 把用户键盘输入写进终端。
func (a *App) TerminalWrite(terminalID, data string) error {
	manager := a.terminalComponent()
	if manager == nil {
		return ErrClosed
	}
	return manager.Write(terminalID, []byte(data))
}

// TerminalResize 调整终端网格尺寸。
func (a *App) TerminalResize(terminalID string, cols, rows int) error {
	manager := a.terminalComponent()
	if manager == nil {
		return ErrClosed
	}
	return manager.Resize(terminalID, uint16(cols), uint16(rows))
}

// CloseTerminal 关闭终端并回收进程；终端不存在时是幂等空操作。
func (a *App) CloseTerminal(terminalID string) error {
	manager := a.terminalManagerIfAny()
	if manager == nil {
		return nil
	}
	if err := manager.Close(terminalID); err != nil {
		// 已退出或不存在：幂等成功，前端状态以 closed 事件为准。
		return nil
	}
	a.emitRaw(EventNameTerminalClosed, map[string]string{"terminalId": terminalID})
	return nil
}

// closeTerminals 在应用退出时回收全部终端。
func (a *App) closeTerminals() {
	if manager := a.terminalManagerIfAny(); manager != nil {
		manager.CloseAll()
	}
}

// emitRaw 用进程级 Sender 直接发送非 agent 事件。
// Wails 生命周期未启动时跳过（runtime 层需要真实生命周期 context）。
func (a *App) emitRaw(name string, payload any) {
	if !a.isStarted() {
		return
	}
	a.mu.Lock()
	sender, ctx := a.sender, a.ctx
	a.mu.Unlock()
	if sender == nil {
		return
	}
	sender(ctx, name, payload)
}
