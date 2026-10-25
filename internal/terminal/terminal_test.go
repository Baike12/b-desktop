package terminal

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// collectOutput 聚合 PTY 输出，返回读取函数。
func collectOutput() (func([]byte), func() string) {
	var mu sync.Mutex
	var sb strings.Builder
	return func(data []byte) {
			mu.Lock()
			sb.Write(data)
			mu.Unlock()
		}, func() string {
			mu.Lock()
			defer mu.Unlock()
			return sb.String()
		}
}

// waitForOutput 轮询直到输出匹配或超时。
func waitForOutput(t *testing.T, read func() string, pattern string) string {
	t.Helper()
	re := regexp.MustCompile(pattern)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if re.MatchString(read()) {
			return read()
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("output %q never matched %q", read(), pattern)
	return ""
}

// TestManagerOpenWriteClose 用真实 /bin/sh 验证 PTY 全链路：
// 输入 echo 命令 → 输出包含结果 → 关闭后索引摘除。
func TestManagerOpenWriteClose(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	manager := NewManager(nil)
	output, read := collectOutput()
	// 显式 Close 按设计不回调 exit（前端以 terminal:closed 事件为准）；
	// exit 回调只属于进程自然退出。
	sess, err := manager.Open(context.Background(), "tty_1", "s1", t.TempDir(), output, func(int, error) {
		t.Error("exit callback must not fire on explicit close")
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if sess.ID != "tty_1" {
		t.Fatalf("id = %s", sess.ID)
	}
	// 同会话重复 Open 返回同一实例。
	again, err := manager.Open(context.Background(), "tty_2", "s1", t.TempDir(), output, func(int, error) {})
	if err != nil || again.ID != sess.ID {
		t.Fatalf("duplicate open = %s, %v", again.ID, err)
	}

	if err := manager.Write(sess.ID, []byte("echo bdesk_pty_ok\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	waitForOutput(t, read, `bdesk_pty_ok`)

	if err := manager.Resize(sess.ID, 100, 30); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if err := manager.Resize(sess.ID, 0, 0); err == nil {
		t.Fatal("Resize 0x0 should fail")
	}

	if err := manager.Close(sess.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, ok := manager.Get(sess.ID); ok {
		t.Fatal("session should be removed from index")
	}
	if err := manager.Write(sess.ID, []byte("x")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("write after close = %v, want ErrNotFound", err)
	}
}

// TestManagerCloseAll 一次关闭全部终端并清理索引。
func TestManagerCloseAll(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	manager := NewManager(nil)
	output, _ := collectOutput()
	noop := func(int, error) {}
	s1, err := manager.Open(context.Background(), "", "s1", t.TempDir(), output, noop)
	if err != nil {
		t.Fatalf("open s1: %v", err)
	}
	s2, err := manager.Open(context.Background(), "", "s2", t.TempDir(), output, noop)
	if err != nil {
		t.Fatalf("open s2: %v", err)
	}
	manager.CloseAll()
	if _, ok := manager.Get(s1.ID); ok {
		t.Fatal("s1 should be removed")
	}
	if _, ok := manager.Get(s2.ID); ok {
		t.Fatal("s2 should be removed")
	}
}

// TestShellPath 兜底逻辑。
func TestShellPath(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	if got := shellPath(); got != "/bin/zsh" {
		t.Fatalf("shellPath = %s", got)
	}
	t.Setenv("SHELL", "")
	if got := shellPath(); got == "" {
		t.Fatal("fallback shell is empty")
	}
}
