package mcp

import (
	"context"
	"errors"
	"testing"
)

// TestStartStdioValidation 覆盖不启动进程即可验证的错误路径。
func TestStartStdioValidation(t *testing.T) {
	if _, err := StartStdio(context.Background(), "s", ServerConfig{}); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want ErrConfigInvalid", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := StartStdio(ctx, "s", ServerConfig{Command: "unused"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
