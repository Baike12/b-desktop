package task

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// bashWaitDelay 是取消后等待输出管道关闭的上限，避免被孤儿子进程拖住。
const bashWaitDelay = 2 * time.Second

// StartBashInput 是一次后台命令的启动参数。
type StartBashInput struct {
	SessionID   string
	Description string
	Command     string
	Argv        []string
	Dir         string
	Env         []string
	NewID       func(prefix string) string
}

// BashHandle 是一个后台命令的句柄。
type BashHandle struct {
	id       string
	reg      *Registry
	entry    *entry
	cmd      *exec.Cmd
	startErr error
}

// TaskID 返回任务 ID。
func (h *BashHandle) TaskID() string { return h.id }

// OutputFile 返回输出文件路径。
func (h *BashHandle) OutputFile() string { return h.reg.outputPath(h.id) }

// Err 报告启动失败原因；成功启动时为 nil。
func (h *BashHandle) Err() error { return h.startErr }

// StartBash 以后台任务方式启动一条命令：stdout/stderr 直接追加写入输出文件，
// 生命周期归 Registry（应用级 ctx），不随发起它的那一轮对话结束而被杀。
// 返回的句柄立即可用；启动失败通过 Err 表达，任务以 failed 状态落表。
func (r *Registry) StartBash(in StartBashInput) *BashHandle {
	id := "bash_" + newTaskID(in.NewID)
	e := r.register(Task{
		ID:          id,
		Kind:        KindBash,
		SessionID:   in.SessionID,
		Description: firstNonEmpty(in.Description, in.Command),
		Command:     in.Command,
		Status:      StatusRunning,
		StartedAt:   r.now(),
		OutputFile:  r.outputPath(id),
	})

	h := &BashHandle{id: id, reg: r, entry: e}
	runCtx, cancel := context.WithCancel(r.root)
	e.mu.Lock()
	e.cancel = cancel
	e.mu.Unlock()

	name, args := command(in)
	cmd := exec.CommandContext(runCtx, name, args...)
	cmd.Dir = firstNonEmpty(in.Dir, ".")
	if len(in.Env) > 0 {
		cmd.Env = in.Env
	} else {
		cmd.Env = commandEnv()
	}
	cmd.Stdin = nil
	cmd.WaitDelay = bashWaitDelay
	// 默认的 Cancel 只杀直接子进程；这里扩展到整棵进程树。
	cmd.Cancel = func() error {
		KillProcessTree(cmd.Process)
		return nil
	}

	file, err := os.OpenFile(h.OutputFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err == nil {
		cmd.Stdout = file
		cmd.Stderr = file
	}
	h.cmd = cmd
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		h.startErr = err
		go r.finish(e, StatusFailed, func(t *Task) {
			t.Error = fmt.Sprintf("failed to start command: %v", err)
			_ = file.Close()
		})
		return h
	}

	go func() {
		waitErr := cmd.Wait()
		_ = file.Close()
		code := exitCodeOf(waitErr)
		switch {
		case runCtx.Err() != nil:
			// 被 Stop / Close 终止：写成 stopped；通知是否抑制由 Stop 侧决定。
			r.finish(e, StatusStopped, func(t *Task) { t.ExitCode = code })
		case waitErr == nil:
			r.finish(e, StatusCompleted, func(t *Task) { t.ExitCode = 0 })
		case code != 0:
			r.finish(e, StatusFailed, func(t *Task) {
				t.ExitCode = code
				t.Error = fmt.Sprintf("command failed with exit code %d", code)
			})
		default:
			r.finish(e, StatusFailed, func(t *Task) { t.Error = waitErr.Error() })
		}
	}()
	return h
}

// command 返回要执行的程序与参数，与前台 Bash 的解释规则一致。
func command(in StartBashInput) (string, []string) {
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

// OutputTail 返回任务输出的尾部内容与总量。maxBytes 限制返回的字符数，
// 从尾部截取并保证不切断 UTF-8 序列。任务没有输出文件时返回 ErrOutputUnavailable。
func (r *Registry) OutputTail(id string, maxBytes int) (text string, truncated bool, total int64, err error) {
	r.mu.Lock()
	e, ok := r.tasks[id]
	r.mu.Unlock()
	if !ok {
		return "", false, 0, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	e.mu.Lock()
	path := e.OutputFile
	e.mu.Unlock()
	if strings.TrimSpace(path) == "" {
		return "", false, 0, fmt.Errorf("%w: %s", ErrOutputUnavailable, id)
	}
	return readTail(path, maxBytes)
}

// readTail 读取文件尾部最多 maxBytes 字节；跨过 1MB 后按 64KB 尾窗读取。
func readTail(path string, maxBytes int) (string, bool, int64, error) {
	if maxBytes <= 0 {
		maxBytes = 32 * 1024
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, 0, fmt.Errorf("%w: %s", ErrOutputUnavailable, path)
		}
		return "", false, 0, fmt.Errorf("task: open output %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return "", false, 0, fmt.Errorf("task: stat output %s: %w", path, err)
	}
	total := info.Size()
	window := int64(maxBytes)
	if total <= window {
		data := make([]byte, total)
		if _, err := file.ReadAt(data, 0); err != nil {
			return "", false, total, fmt.Errorf("task: read output %s: %w", path, err)
		}
		return string(data), false, total, nil
	}
	data := make([]byte, window)
	if _, err := file.ReadAt(data, total-window); err != nil {
		return "", true, total, fmt.Errorf("task: read output %s: %w", path, err)
	}
	// 对齐到 UTF-8 边界，避免截断多字节字符。
	start := 0
	for start < len(data) && !isRuneStart(data[start]) {
		start++
	}
	return string(data[start:]), true, total, nil
}

// isRuneStart 报告字节是否是 UTF-8 序列的首字节。
func isRuneStart(b byte) bool { return b&0xC0 != 0x80 } // exitCodeOf 从 Wait 错误中取出退出码；无法取得时返回 -1。
func exitCodeOf(err error) int {
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

// KillProcessTree 终止进程及其全部后代。先枚举后代再杀父子进程，
// 避免父进程先退出后子进程被 init 收养、无法再按父 PID 找到。
// 平台差异用运行时分支处理，不依赖额外文件。
func KillProcessTree(p *os.Process) {
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

// AgentInfo 是注册一个后台子 Agent 任务所需的信息。
type AgentInfo struct {
	ID          string
	SessionID   string
	Description string
	AgentType   string
	// Cancel 终止该子 Agent；TaskStop 调用它。
	Cancel func()
}

// AgentHandle 是子 Agent 任务在注册表中的句柄。
type AgentHandle struct {
	id    string
	reg   *Registry
	entry *entry
}

// TaskID 返回任务 ID。
func (h *AgentHandle) TaskID() string { return h.id }

// AppendProgress 追加一条进度行（转发到注册表）。
func (h *AgentHandle) AppendProgress(line string) { h.reg.AppendProgress(h.id, line) }

// Restart 把已终态的 agent 任务标记回 running（SendMessage 续跑时调用）。
func (h *AgentHandle) Restart() {
	h.entry.mu.Lock()
	defer h.entry.mu.Unlock()
	if h.entry.Status.Terminal() {
		h.entry.Status = StatusRunning
		h.entry.FinishedAt = time.Time{}
		h.entry.Result = ""
		h.entry.Error = ""
		h.entry.suppressNotify = false
		h.entry.done = make(chan struct{})
	}
}

// Finish 写入 agent 任务的终态并触发完成通知。
func (h *AgentHandle) Finish(status Status, resultText, errMsg string) {
	h.reg.finish(h.entry, status, func(t *Task) {
		t.Result = resultText
		t.Error = errMsg
	})
}

// SuppressNotify 抑制下一次完成通知（主动停止场景由 Stop 统一处理，
// 这里供子 Agent 内部取消等已知路径使用）。
func (h *AgentHandle) SuppressNotify() {
	h.entry.mu.Lock()
	h.entry.suppressNotify = true
	h.entry.mu.Unlock()
}

// RegisterAgent 登记一个后台子 Agent 任务。
func (r *Registry) RegisterAgent(in AgentInfo) *AgentHandle {
	e := r.register(Task{
		ID:          in.ID,
		Kind:        KindAgent,
		SessionID:   in.SessionID,
		Description: in.Description,
		AgentType:   in.AgentType,
		Status:      StatusRunning,
		StartedAt:   r.now(),
	})
	e.mu.Lock()
	// cancel 包装一层：Registry 关闭时 root cancel 不会触发它，需要显式调用。
	e.cancel = in.Cancel
	e.mu.Unlock()
	return &AgentHandle{id: in.ID, reg: r, entry: e}
}

// newTaskID 生成任务 ID：注入器优先，否则用纳秒时间戳。
func newTaskID(inject func(prefix string) string) string {
	if inject != nil {
		return inject("t")
	}
	return fmt.Sprintf("t_%d", time.Now().UnixNano())
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
