package mcp

import (
	"context"
	"os"
	"testing"
	"time"
)

// testTimeout 是测试中单次 MCP 调用的上限。
const testTimeout = 5 * time.Second

// contextWithTimeout 返回带超时的 ctx，测试结束时自动取消。
func contextWithTimeout(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	t.Cleanup(cancel)
	return ctx
}

// contextWithTimeoutValue 返回指定超时的 ctx 与取消函数。
func contextWithTimeoutValue(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// writeFile 写入测试文件，所有调用点用 0600，避免测试内容被其它用户读取。
func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}
