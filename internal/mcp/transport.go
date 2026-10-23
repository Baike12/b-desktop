package mcp

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// stderrTailLimit 是子进程 stderr 保留的末尾字节数，用于连接断开时补充错误信息。
	stderrTailLimit = 4096
	// transportCloseTimeout 是 Close 等待子进程退出的上限，避免被僵尸进程拖住。
	transportCloseTimeout = 5 * time.Second
)

// DialFunc 建立一条与 MCP 服务器的双向消息流。生产实现是 StartStdio；
// 测试可注入 net.Pipe 等内存实现，不依赖外部进程。
type DialFunc func(ctx context.Context, name string, cfg ServerConfig) (io.ReadWriteCloser, error)

// StartStdio 启动配置中的子进程，返回绑定其 stdin/stdout 的传输。
// args 不经过 shell 解释；env 合并父进程环境后覆盖同名变量。
// 返回的传输关闭时会杀死子进程。
func StartStdio(ctx context.Context, name string, cfg ServerConfig) (io.ReadWriteCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("mcp: start server %q: %w", name, err)
	}
	if strings.TrimSpace(cfg.Command) == "" {
		return nil, fmt.Errorf("%w: server %q has empty command", ErrConfigInvalid, name)
	}

	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Env = mergeEnv(os.Environ(), cfg.Env)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp: server %q stdin: %w", name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("mcp: server %q stdout: %w", name, err)
	}
	tail := &tailBuffer{limit: stderrTailLimit}
	cmd.Stderr = tail

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("mcp: server %q start %s: %w%s", name, cfg.Command, err, tailSuffix(tail.String()))
	}

	t := &stdioTransport{
		name:   name,
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
		stderr: tail,
		done:   make(chan struct{}),
	}
	go t.wait()
	return t, nil
}

// stdioTransport 是 stdio 会话的 io.ReadWriteCloser。
type stdioTransport struct {
	name   string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr *tailBuffer

	closeOnce sync.Once
	waitErr   error
	done      chan struct{}
}

// Read 从子进程 stdout 读取；进程退出时返回底层错误（通常是 EOF）。
func (t *stdioTransport) Read(p []byte) (int, error) { return t.stdout.Read(p) }

// Write 写入子进程 stdin。
func (t *stdioTransport) Write(p []byte) (int, error) { return t.stdin.Write(p) }

// StderrTail 返回子进程 stderr 的末尾内容，供错误信息补充。
func (t *stdioTransport) StderrTail() string { return t.stderr.String() }

// Close 关闭 stdin 并杀死子进程，等待 Wait 返回。可重复调用。
func (t *stdioTransport) Close() error {
	var err error
	t.closeOnce.Do(func() {
		_ = t.stdin.Close()
		if t.cmd.Process != nil {
			if killErr := t.cmd.Process.Kill(); killErr != nil {
				err = fmt.Errorf("mcp: kill server %q: %w", t.name, killErr)
			}
		}
		select {
		case <-t.done:
		case <-time.After(transportCloseTimeout):
			err = fmt.Errorf("mcp: server %q did not exit in %s", t.name, transportCloseTimeout)
		}
		_ = t.stdout.Close()
	})
	return err
}

// wait 回收子进程。
func (t *stdioTransport) wait() {
	t.waitErr = t.cmd.Wait()
	close(t.done)
}

// tailBuffer 是只保留末尾 limit 字节的并发安全写入缓冲。
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

// Write 实现 io.Writer。
func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.limit {
		n := copy(b.buf, b.buf[len(b.buf)-b.limit:])
		b.buf = b.buf[:n]
	}
	return len(p), nil
}

// String 返回当前保留的内容。
func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.buf))
}

// mergeEnv 把覆盖项合并进 baseline，返回 K=V 列表；同名后者生效，输出按键排序。
func mergeEnv(baseline []string, overrides map[string]string) []string {
	merged := make(map[string]string, len(baseline)+len(overrides))
	for _, entry := range baseline {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		merged[key] = value
	}
	for key, value := range overrides {
		merged[key] = value
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+merged[key])
	}
	return out
}

// tailSuffix 把 stderr 末尾格式化成可选的错误后缀。
func tailSuffix(tail string) string {
	if tail == "" {
		return ""
	}
	return ": " + tail
}
