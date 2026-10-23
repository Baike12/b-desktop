package app

import (
	"fmt"
	"os"
	"testing"
)

// TestMain 把 HOME 指向临时目录，避免测试写入真实的 ~/.b-desktop：
// 扩展组件（记忆、技能、hooks、MCP、任务、用量）都以该目录为根。
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "anc-app-test-home-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "app tests: create temp home: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("HOME", home); err != nil {
		fmt.Fprintf(os.Stderr, "app tests: set HOME: %v\n", err)
		_ = os.RemoveAll(home)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
