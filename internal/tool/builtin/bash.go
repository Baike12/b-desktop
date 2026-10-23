// Package builtin 提供内置工具实现。
package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"anc-dsk/internal/tool"
)

const (
	// defaultBashTimeout 是 timeout_ms 未提供时的命令超时。
	defaultBashTimeout = 120 * time.Second
	// maxBashTimeout 是 timeout_ms 允许的最大值。
	maxBashTimeout = 600 * time.Second
	// defaultBashOutputLimit 是 stdout 与 stderr 各自保留的字节上限。
	defaultBashOutputLimit = 30000
	// bashWaitDelay 是取消后等待输出管道关闭的上限，避免被孤儿子进程拖住。
	bashWaitDelay = 2 * time.Second
)

// bashSchema 是 Bash 的输入 schema。
var bashSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": {
      "type": "string",
      "description": "Shell command line. Interpreted by /bin/sh -c on Unix and %COMSPEC% /d /s /c on Windows."
    },
    "argv": {
      "type": "array",
      "items": {"type": "string"},
      "description": "Program and arguments executed directly without a shell. Mutually exclusive with command."
    },
    "timeout_ms": {
      "type": "integer",
      "minimum": 0,
      "maximum": 600000,
      "description": "Execution timeout in milliseconds. Default 120000, capped at 600000."
    },
    "description": {
      "type": "string",
      "description": "Short human-readable description of what the command does."
    }
  }
}`)

// Bash 执行一条命令。它实现了 tool.Tool。
//
// 命令按 argv 直接执行，或显式交给系统 shell 解释（Unix `/bin/sh -c`，
// Windows `%COMSPEC% /d /s /c`）；任何参数都不会拼接进命令字符串。
// stdout 与 stderr 分别捕获，各自最多保留 defaultBashOutputLimit 字节。
type Bash struct {
	root           string
	outputLimit    int
	defaultTimeout time.Duration
	maxTimeout     time.Duration
}

// NewBash 创建以 root 为工作目录的 Bash 工具。
// root 为空时使用进程当前目录。
func NewBash(root string) tool.Tool {
	dir := root
	if dir == "" {
		dir = "."
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return &Bash{
		root:           dir,
		outputLimit:    defaultBashOutputLimit,
		defaultTimeout: defaultBashTimeout,
		maxTimeout:     maxBashTimeout,
	}
}

// Name 返回工具名。
func (b *Bash) Name() string { return "Bash" }

// Description 返回给模型看的说明。
func (b *Bash) Description() string {
	return "Execute a command in the session working directory. " +
		"Prefer the argv form when no shell features are needed; use command when pipes, redirects or shell syntax are required. " +
		"stdout and stderr are captured separately and truncated to 30000 bytes each. " +
		"Non-zero exit codes are reported in the result instead of failing the call. " +
		"The command is killed together with its descendants on timeout or cancellation."
}

// InputSchema 返回 JSON Schema。
func (b *Bash) InputSchema() json.RawMessage { return bashSchema }

// ReadOnly 报告该调用是否只读。命令可以修改任意文件与进程状态，因此恒为 false。
func (b *Bash) ReadOnly(json.RawMessage) bool { return false }

// ConcurrencySafe 报告该调用能否并行。命令存在未知副作用，因此恒为 false。
func (b *Bash) ConcurrencySafe(json.RawMessage) bool { return false }

// bashInput 是 Bash 的输入。
type bashInput struct {
	Command     string   `json:"command"`
	Argv        []string `json:"argv"`
	TimeoutMS   int      `json:"timeout_ms"`
	Description string   `json:"description"`
}

// Run 执行命令并返回结果。
//
// 返回 error 只用于调用方取消；启动失败、超时、非零退出都转换成结构化 Result，
// 不打断 Agent 循环。非零退出不是异常：退出码写进 Content 与 Meta，IsError 保持 false。
func (b *Bash) Run(ctx context.Context, input json.RawMessage, progress tool.ProgressFunc) (tool.Result, error) {
	var in bashInput
	if err := json.Unmarshal(input, &in); err != nil {
		return tool.Result{IsError: true, Content: "invalid input: " + err.Error()}, nil
	}
	if len(in.Argv) > 0 && strings.TrimSpace(in.Command) != "" {
		return tool.Result{IsError: true, Content: "invalid input: provide either command or argv, not both"}, nil
	}
	if len(in.Argv) == 0 && strings.TrimSpace(in.Command) == "" {
		return tool.Result{IsError: true, Content: "invalid input: command or argv is required"}, nil
	}

	timeout := b.timeout(in.TimeoutMS)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	name, args := b.command(in)
	cmd := exec.CommandContext(runCtx, name, args...)
	cmd.Dir = b.root
	cmd.Env = commandEnv()
	cmd.Stdin = nil
	cmd.WaitDelay = bashWaitDelay

	stdout := newLimitedBuffer(b.outputLimit)
	stderr := newLimitedBuffer(b.outputLimit)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	// 默认的 Cancel 只杀直接子进程；这里扩展到整棵进程树。
	cmd.Cancel = func() error {
		killProcessTree(cmd.Process)
		return nil
	}

	start := time.Now()
	if err := cmd.Start(); err != nil {
		if ctx.Err() != nil {
			return tool.Result{}, fmt.Errorf("bash: %w", ctx.Err())
		}
		return tool.Result{
			IsError: true,
			Content: fmt.Sprintf("failed to start command: %v", err),
			Meta:    map[string]string{"duration_ms": strconv.FormatInt(time.Since(start).Milliseconds(), 10)},
		}, nil
	}

	done := make(chan struct{})
	progressDone := make(chan struct{})
	if progress != nil {
		go func() {
			defer close(progressDone)
			ticker := time.NewTicker(250 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					progress(tool.Progress{
						Bytes:     stdout.Total() + stderr.Total(),
						ElapsedMS: time.Since(start).Milliseconds(),
					})
				}
			}
		}()
	} else {
		close(progressDone)
	}

	err := cmd.Wait()
	close(done)
	// 等进度 goroutine 退出后再发终态，保证 progress 不会被并发调用。
	<-progressDone
	elapsed := time.Since(start)

	if progress != nil {
		progress(tool.Progress{
			Bytes:     stdout.Total() + stderr.Total(),
			ElapsedMS: elapsed.Milliseconds(),
			Text:      "done",
		})
	}

	if err != nil {
		// 调用方取消优先于内部超时：两者都让 Wait 返回 ctx 错误。
		if ctx.Err() != nil {
			return tool.Result{}, fmt.Errorf("bash: %w", ctx.Err())
		}
		if runCtx.Err() == context.DeadlineExceeded {
			res := b.result(-1, stdout, stderr, elapsed)
			res.IsError = true
			res.Content = fmt.Sprintf("command timed out after %s\n%s", timeout, res.Content)
			res.Display = res.Content
			res.Meta["timed_out"] = "true"
			return res, nil
		}
	}

	res := b.result(exitCode(err), stdout, stderr, elapsed)
	return res, nil
}

// timeout 把 timeout_ms 归一化到 [defaultTimeout, maxTimeout]。
func (b *Bash) timeout(ms int) time.Duration {
	d := b.defaultTimeout
	if ms > 0 {
		d = time.Duration(ms) * time.Millisecond
	}
	if d > b.maxTimeout {
		d = b.maxTimeout
	}
	if d <= 0 {
		d = defaultBashTimeout
	}
	return d
}

// command 返回要执行的程序与参数。两种形式互斥，调用前已校验。
func (b *Bash) command(in bashInput) (string, []string) {
	if len(in.Argv) > 0 {
		return in.Argv[0], in.Argv[1:]
	}
	if runtime.GOOS == "windows" {
		comspec := os.Getenv("COMSPEC")
		if comspec == "" {
			comspec = "cmd.exe"
		}
		return comspec, []string{"/d", "/s", "/c", in.Command}
	}
	return "/bin/sh", []string{"-c", in.Command}
}

// result 把退出状态与两路输出渲染成工具结果。
func (b *Bash) result(exitCode int, stdout, stderr *limitedBuffer, elapsed time.Duration) tool.Result {
	var sb strings.Builder
	if exitCode >= 0 {
		fmt.Fprintf(&sb, "exit code: %d\n", exitCode)
	} else {
		sb.WriteString("exit code: terminated by signal\n")
	}
	writeStream(&sb, "stdout", stdout, b.outputLimit)
	writeStream(&sb, "stderr", stderr, b.outputLimit)

	meta := map[string]string{
		"exit_code":    strconv.Itoa(exitCode),
		"stdout_bytes": strconv.FormatInt(stdout.Total(), 10),
		"stderr_bytes": strconv.FormatInt(stderr.Total(), 10),
		"duration_ms":  strconv.FormatInt(elapsed.Milliseconds(), 10),
	}
	truncated := stdout.Truncated() || stderr.Truncated()
	if truncated {
		meta["truncated"] = "true"
	}

	content := sb.String()
	return tool.Result{
		Content:   content,
		Display:   content,
		Truncated: truncated,
		Meta:      meta,
	}
}

// writeStream 追加一段输出；流为空时不输出标题。
func writeStream(sb *strings.Builder, name string, buf *limitedBuffer, limit int) {
	total := buf.Total()
	if total == 0 {
		return
	}
	fmt.Fprintf(sb, "--- %s ---\n", name)
	sb.WriteString(buf.String())
	if !strings.HasSuffix(buf.String(), "\n") {
		sb.WriteByte('\n')
	}
	if buf.Truncated() {
		fmt.Fprintf(sb, "[%s truncated: kept %d of %d bytes]\n", name, limit, total)
	}
}

// exitCode 从 Wait 错误中取出退出码；无法取得时返回 -1。
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// commandEnv 返回过滤后的环境变量：只保留解释器与常规命令需要的项，
// 不把应用配置（API Key 等）泄漏给子进程。
func commandEnv() []string {
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "USERPROFILE": true, "LANG": true,
		"LC_ALL": true, "LC_CTYPE": true, "TERM": true,
		"TMPDIR": true, "TEMP": true, "TMP": true,
		"SYSTEMROOT": true, "COMSPEC": true, "PATHEXT": true, "SYSTEMDRIVE": true,
	}
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if allowed[strings.ToUpper(key)] {
			out = append(out, kv)
		}
	}
	return out
}

// killProcessTree 终止进程及其全部后代。先枚举后代再杀父子进程，
// 避免父进程先退出后子进程被 init 收养、无法再按父 PID 找到。
// 平台差异用运行时分支处理，不依赖额外文件。
func killProcessTree(p *os.Process) {
	if p == nil {
		return
	}
	if runtime.GOOS == "windows" {
		_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(p.Pid)).Run()
		return
	}
	for _, pid := range descendantPIDs(p.Pid, 0) {
		if child, err := os.FindProcess(pid); err == nil {
			_ = child.Kill()
		}
	}
	_ = p.Kill()
}

// descendantPIDs 递归收集 pid 的后代，depth 用于限制异常深的进程树。
func descendantPIDs(pid, depth int) []int {
	if depth > 8 {
		return nil
	}
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(pid)).Output()
	if err != nil {
		return nil
	}
	var pids []int
	for _, field := range strings.Fields(string(out)) {
		child, err := strconv.Atoi(field)
		if err != nil {
			continue
		}
		pids = append(pids, child)
		pids = append(pids, descendantPIDs(child, depth+1)...)
	}
	return pids
}

// limitedBuffer 是带字节上限的并发安全输出缓冲：超出上限后继续计数但不再保存。
type limitedBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
	total int64
}

// newLimitedBuffer 创建上限为 limit 字节的缓冲；limit <= 0 时使用默认值。
func newLimitedBuffer(limit int) *limitedBuffer {
	if limit <= 0 {
		limit = defaultBashOutputLimit
	}
	return &limitedBuffer{limit: limit}
}

// Write 实现 io.Writer，始终返回 len(p)，避免上游把截断当成写失败。
func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	b.total += int64(n)
	if room := b.limit - b.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		b.buf.Write(p)
	}
	return n, nil
}

// String 返回已保存的内容。
func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Total 返回写入的总字节数（含被截断部分）。
func (b *limitedBuffer) Total() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total
}

// Truncated 报告是否有内容被丢弃。
func (b *limitedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total > int64(b.limit)
}
