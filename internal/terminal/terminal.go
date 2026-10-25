// Package terminal 用 PTY 承载每个会话的交互式终端。
// 每个会话至多一个终端实例：面板关闭只停止前端订阅，PTY 保持运行；
// 退出应用或显式关闭时回收进程。
package terminal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/creack/pty"
)

// 默认终端尺寸；前端 xterm 就绪后会立即 Resize 到真实网格。
const (
	defaultCols = 80
	defaultRows = 24
	// maxWriteBytes 是单次写入上限，防止误传超大粘贴。
	maxWriteBytes = 1 << 20
)

// 错误哨兵。
var (
	// ErrNotFound 表示终端不存在或已退出。
	ErrNotFound = errors.New("terminal: not found")
	// ErrClosed 表示终端已关闭。
	ErrClosed = errors.New("terminal: closed")
	// ErrUnsupported 表示当前平台没有 PTY 实现。
	ErrUnsupported = errors.New("terminal: pty unsupported on this platform")
)

// Session 是一个运行中的 PTY 终端。
type Session struct {
	ID        string
	SessionID string
	WorkDir   string

	cmd    *exec.Cmd
	ptmx   *os.File
	cancel context.CancelFunc
	done   chan struct{}
	closed atomic.Bool
	// exitCode 在进程退出后由 reader goroutine 写入一次。
	exitCode atomic.Int32

	// Output 在 reader goroutine 上被调用，实现必须快速返回（写事件即返回）。
	Output func(data []byte)
	// Exit 在 reader goroutine 上被调用一次，之后终端自动从 Manager 摘除。
	Exit func(code int, err error)
}

// Manager 管理全部终端：按会话 ID 建立索引，保证一会话一终端。
type Manager struct {
	mu     sync.Mutex
	bySess map[string]*Session
	byID   map[string]*Session
	newID  func(prefix string) string
}

// NewManager 创建空的管理器。
func NewManager(newID func(prefix string) string) *Manager {
	if newID == nil {
		newID = defaultNewID
	}
	return &Manager{
		bySess: make(map[string]*Session),
		byID:   make(map[string]*Session),
		newID:  newID,
	}
}

// defaultNewID 生成 "prefix_<16 位随机十六进制>" 形式的终端 ID。
func defaultNewID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

// Open 为会话打开终端并返回实例。id 为空时自动生成；
// 已有该会话的终端时忽略 id 直接返回现有实例。output 与 exit 不能为 nil。
func (m *Manager) Open(ctx context.Context, id, sessionID, workDir string, output func([]byte), exit func(int, error)) (*Session, error) {
	if output == nil || exit == nil {
		return nil, errors.New("terminal: output and exit callbacks are required")
	}
	m.mu.Lock()
	if existing := m.bySess[sessionID]; existing != nil {
		m.mu.Unlock()
		return existing, nil
	}
	if id == "" {
		id = m.newID("tty")
	}
	m.mu.Unlock()

	session, err := startPTY(ctx, id, sessionID, workDir, output, exit)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	// 双检：并发 Open 时保留先创建者，关闭后创建者。
	if existing := m.bySess[sessionID]; existing != nil {
		m.mu.Unlock()
		_ = session.close()
		return existing, nil
	}
	m.bySess[sessionID] = session
	m.byID[id] = session
	m.mu.Unlock()
	return session, nil
}

// startPTY 启动 shell 与 PTY，并启动读取 goroutine。
func startPTY(ctx context.Context, id, sessionID, workDir string, output func([]byte), exit func(int, error)) (*Session, error) {
	shell := shellPath()
	cmd := exec.Command(shell, "-i")
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: defaultCols, Rows: defaultRows})
	if err != nil {
		if errors.Is(err, pty.ErrUnsupported) {
			return nil, ErrUnsupported
		}
		return nil, fmt.Errorf("terminal: start %s: %w", shell, err)
	}

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	session := &Session{
		ID:        id,
		SessionID: sessionID,
		WorkDir:   workDir,
		cmd:       cmd,
		ptmx:      ptmx,
		cancel:    cancel,
		done:      make(chan struct{}),
		Output:    output,
		Exit:      exit,
	}

	go func() {
		defer close(session.done)
		buf := make([]byte, 32<<10)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				output(data)
			}
			if err != nil {
				// 主动关闭（面板回收/应用退出）不再回调 exit，避免噪声事件。
				if session.closed.Load() || errors.Is(err, os.ErrClosed) || runCtx.Err() != nil {
					return
				}
				code := -1
				var exitErr error
				if waitErr := cmd.Wait(); waitErr == nil {
					code = 0
				} else {
					exitErr = waitErr
				}
				session.exitCode.Store(int32(code))
				exit(code, exitErr)
				return
			}
		}
	}()
	return session, nil
}

// shellPath 返回交互 shell：$SHELL 优先，按平台兜底。
func shellPath() string {
	if shell := strings.TrimSpace(os.Getenv("SHELL")); shell != "" {
		return shell
	}
	if runtime.GOOS == "windows" {
		return "powershell"
	}
	return "/bin/bash"
}

// Get 按终端 ID 查找。
func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.byID[id]
	return s, ok
}

// GetBySession 返回会话的终端。
func (m *Manager) GetBySession(sessionID string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.bySess[sessionID]
	return s, ok
}

// Write 把用户输入写进 PTY。
func (m *Manager) Write(id string, data []byte) error {
	if len(data) > maxWriteBytes {
		return fmt.Errorf("terminal: write %d bytes exceeds limit %d", len(data), maxWriteBytes)
	}
	s, ok := m.Get(id)
	if !ok {
		return ErrNotFound
	}
	if s.closed.Load() {
		return ErrClosed
	}
	_, err := s.ptmx.Write(data)
	if err != nil {
		return fmt.Errorf("terminal: write: %w", err)
	}
	return nil
}

// Resize 调整 PTY 窗口尺寸。
func (m *Manager) Resize(id string, cols, rows uint16) error {
	if cols == 0 || rows == 0 || cols > 500 || rows > 500 {
		return fmt.Errorf("terminal: invalid size %dx%d", cols, rows)
	}
	s, ok := m.Get(id)
	if !ok {
		return ErrNotFound
	}
	if s.closed.Load() {
		return ErrClosed
	}
	if err := pty.Setsize(s.ptmx, &pty.Winsize{Cols: cols, Rows: rows}); err != nil {
		return fmt.Errorf("terminal: resize: %w", err)
	}
	return nil
}

// Close 关闭指定终端并等待进程退出。
func (m *Manager) Close(id string) error {
	s, ok := m.Get(id)
	if !ok {
		return ErrNotFound
	}
	m.remove(s)
	return s.close()
}

// CloseSession 关闭会话的终端；没有终端是空操作。
func (m *Manager) CloseSession(sessionID string) {
	s, ok := m.GetBySession(sessionID)
	if !ok {
		return
	}
	m.remove(s)
	_ = s.close()
}

// CloseAll 关闭全部终端并等待退出，供应用 Shutdown 使用。
func (m *Manager) CloseAll() {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.byID))
	for id, s := range m.byID {
		sessions = append(sessions, s)
		delete(m.byID, id)
		delete(m.bySess, s.SessionID)
	}
	m.mu.Unlock()
	for _, s := range sessions {
		_ = s.close()
	}
}

// remove 把终端从索引摘除。
func (m *Manager) remove(s *Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.byID, s.ID)
	if current := m.bySess[s.SessionID]; current == s {
		delete(m.bySess, s.SessionID)
	}
}

// close 关闭 PTY 与进程。幂等。
func (s *Session) close() error {
	if s.closed.Swap(true) {
		return nil
	}
	s.cancel()
	_ = s.ptmx.Close()
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
		_ = s.cmd.Process.Kill()
		<-s.done
	}
	return nil
}
