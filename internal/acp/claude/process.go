package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// 进程管理的常量：stdout 行缓冲上限与优雅退出宽限期。
const (
	maxLineBytes   = 16 * 1024 * 1024
	closeGrace     = 3 * time.Second
	requestTimeout = 120 * time.Second
)

// frameHandler 接收一帧 stdout 消息。实现必须快速返回，不得阻塞读循环。
type frameHandler func(frame outEnvelope)

// controlHandler 处理 CLI→SDK 的控制请求（目前只有 can_use_tool）。
// 返回的 payload 作为 control_response 的 response 字段。
type controlHandler func(ctx context.Context, req controlRequestIn) (json.RawMessage, error)

// process 是一个 claude CLI 子进程：stdout 读循环 + 控制请求/响应配对。
type process struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	writeMu sync.Mutex

	pendingMu sync.Mutex
	pending   map[string]chan controlResponseIn
	nextID    int64

	onFrame   frameHandler
	onControl controlHandler
	onExit    func(err error)

	stderrTail *tailBuffer
	closeOnce  sync.Once
	done       chan struct{}
	procErr    error
}

// startProcess 启动 claude CLI。args 是完整命令行参数；stderr 收进尾部缓冲。
// 读循环在后台 goroutine 运行；进程退出后所有 in-flight 控制请求失败，
// 并回调 onExit。
func startProcess(ctx context.Context, exe string, args []string, env []string, dir string, onFrame frameHandler, onControl controlHandler, onExit func(err error)) (*process, error) {
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Env = env
	cmd.Dir = dir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("claude: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("claude: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("claude: stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("claude: 启动 %s: %w", exe, err)
	}

	p := &process{
		cmd:        cmd,
		stdin:      stdin,
		pending:    make(map[string]chan controlResponseIn),
		onFrame:    onFrame,
		onControl:  onControl,
		onExit:     onExit,
		stderrTail: newTailBuffer(8 * 1024),
		done:       make(chan struct{}),
	}
	go p.readLoop(stdout)
	go p.drainStderr(stderr)
	go p.waitExit()
	return p, nil
}

// request 发送控制请求并等待响应。
func (p *process) request(ctx context.Context, payload controlPayload) (json.RawMessage, error) {
	p.pendingMu.Lock()
	p.nextID++
	id := fmt.Sprintf("req_%d", p.nextID)
	ch := make(chan controlResponseIn, 1)
	p.pending[id] = ch
	p.pendingMu.Unlock()

	encoded, err := json.Marshal(controlRequestOut{Type: "control_request", RequestID: id, Request: payload})
	if err != nil {
		p.removePending(id)
		return nil, fmt.Errorf("claude: encode control request: %w", err)
	}
	if err := p.writeLine(encoded); err != nil {
		p.removePending(id)
		return nil, fmt.Errorf("claude: send control request: %w", err)
	}

	if ctx == nil {
		ctx = context.Background()
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	select {
	case resp, open := <-ch:
		if !open {
			return nil, fmt.Errorf("claude: control request %s 失败：进程已退出（%v）", payload.Subtype, p.exitError())
		}
		if resp.Response.Subtype == "error" {
			return nil, fmt.Errorf("claude: control request %s 被拒绝：%s", payload.Subtype, resp.Response.Error)
		}
		return resp.Response.Response, nil
	case <-timeoutCtx.Done():
		p.removePending(id)
		return nil, fmt.Errorf("claude: control request %s 超时：%w", payload.Subtype, timeoutCtx.Err())
	}
}

// writeLine 写一行 JSON 到 stdin。
func (p *process) writeLine(payload []byte) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_, err := p.stdin.Write(append(payload, '\n'))
	return err
}

func (p *process) removePending(id string) {
	p.pendingMu.Lock()
	delete(p.pending, id)
	p.pendingMu.Unlock()
}

// readLoop 逐行读取 stdout 并按 type 分发。
func (p *process) readLoop(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 || line[0] != '{' {
			// CLI 会在 stdout 混入非 JSON 噪音行（如 [SandboxDebug]），跳过。
			continue
		}
		p.dispatch(line)
	}
}

// dispatch 解析一帧并分发：control_response 配对、control_request 交给
// handler（异步，避免阻塞读循环）、其余帧交给 frameHandler。
func (p *process) dispatch(line []byte) {
	var envelope outEnvelope
	if err := json.Unmarshal(line, &envelope); err != nil {
		return
	}
	envelope.Raw = line
	switch envelope.Type {
	case "control_response":
		var resp controlResponseIn
		if err := json.Unmarshal(line, &resp); err != nil {
			return
		}
		p.resolve(resp.Response.RequestID, resp)
	case "control_request":
		var req controlRequestIn
		if err := json.Unmarshal(line, &req); err != nil {
			return
		}
		if req.Subtype() != "can_use_tool" {
			// 未知控制请求：回错误，避免 CLI 无限等待。
			go p.respondControl(req.RequestID, nil, fmt.Errorf("unsupported control request %q", req.Subtype()))
			return
		}
		go func() {
			payload, err := p.onControl(context.Background(), req)
			p.respondControl(req.RequestID, payload, err)
		}()
	default:
		if p.onFrame != nil {
			p.onFrame(envelope)
		}
	}
}

func (p *process) resolve(id string, resp controlResponseIn) {
	p.pendingMu.Lock()
	ch, ok := p.pending[id]
	if ok {
		delete(p.pending, id)
	}
	p.pendingMu.Unlock()
	if ok {
		ch <- resp
	}
}

// respondControl 回应 CLI 的控制请求。
func (p *process) respondControl(requestID string, payload json.RawMessage, reqErr error) {
	resp := controlResponse{RequestID: requestID, Subtype: "success", Response: payload}
	if reqErr != nil {
		resp = controlResponse{RequestID: requestID, Subtype: "error", Error: reqErr.Error()}
	}
	encoded, err := json.Marshal(controlResponseOut{Type: "control_response", Response: resp})
	if err != nil {
		return
	}
	_ = p.writeLine(encoded)
}

// respondCanUseTool 应答一次 can_use_tool 请求（behavior: allow|deny）。
func (p *process) respondCanUseTool(requestID string, decision canUseToolDecision) error {
	encoded, err := json.Marshal(decision)
	if err != nil {
		return err
	}
	p.respondControl(requestID, encoded, nil)
	return nil
}

// canUseToolDecision 是 can_use_tool 的应答载荷。
type canUseToolDecision struct {
	Behavior           string             `json:"behavior"` // allow | deny
	UpdatedInput       json.RawMessage    `json:"updatedInput,omitempty"`
	UpdatedPermissions []permissionUpdate `json:"updatedPermissions,omitempty"`
	Message            string             `json:"message,omitempty"`
	Interrupt          bool               `json:"interrupt,omitempty"`
}

func (p *process) drainStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 16*1024), 1024*1024)
	for scanner.Scan() {
		p.stderrTail.write(scanner.Bytes())
		p.stderrTail.write([]byte("\n"))
	}
}

// waitExit 等进程退出并唤醒所有 in-flight 控制请求。
func (p *process) waitExit() {
	_ = p.cmd.Wait()
	code := -1
	if p.cmd.ProcessState != nil {
		code = p.cmd.ProcessState.ExitCode()
	}
	p.procErr = fmt.Errorf("claude 进程已退出（exit code %d）", code)
	p.pendingMu.Lock()
	for id, ch := range p.pending {
		close(ch)
		delete(p.pending, id)
	}
	p.pendingMu.Unlock()
	close(p.done)
	if p.onExit != nil {
		p.onExit(p.exitError())
	}
}

// exitError 返回带 stderr 尾部的退出错误。
func (p *process) exitError() error {
	if tail := p.stderrTail.tail(); tail != "" {
		return fmt.Errorf("%w；stderr 尾部：%s", p.procErr, tail)
	}
	return p.procErr
}

// alive 报告进程是否仍在运行。
func (p *process) alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// interrupt 发送 interrupt 控制请求；进程已退出时是空操作。
func (p *process) interrupt(ctx context.Context) {
	if !p.alive() {
		return
	}
	_, _ = p.request(ctx, controlPayload{Subtype: "interrupt"})
}

// setPermissionMode 发送 set_permission_mode 控制请求。
func (p *process) setPermissionMode(ctx context.Context, mode string) error {
	_, err := p.request(ctx, controlPayload{Subtype: "set_permission_mode", Mode: mode})
	return err
}

// close 关闭 stdin 并在宽限期后强杀进程，幂等。
func (p *process) close() {
	p.closeOnce.Do(func() {
		_ = p.stdin.Close()
		select {
		case <-p.done:
		default:
			go func() {
				time.Sleep(closeGrace)
				select {
				case <-p.done:
				default:
					_ = p.cmd.Process.Kill()
				}
			}()
		}
	})
}

// buildEnv 组装子进程环境：继承父进程，剔除 CLAUDECODE，追加 SDK 入口标识。
func buildEnv(extra map[string]string) []string {
	env := os.Environ()
	filtered := make([]string, 0, len(env)+len(extra))
	for _, entry := range env {
		if len(entry) > 10 && entry[:10] == "CLAUDECODE" {
			continue
		}
		filtered = append(filtered, entry)
	}
	names := make([]string, 0, len(extra)+1)
	extraAll := make(map[string]string, len(extra)+1)
	for name, value := range extra {
		extraAll[name] = value
	}
	extraAll["CLAUDE_CODE_ENTRYPOINT"] = "b-desktop"
	for name := range extraAll {
		names = append(names, name)
	}
	sortStrings(names)
	for _, name := range names {
		filtered = append(filtered, name+"="+extraAll[name])
	}
	return filtered
}

// sortStrings 就地排序字符串切片（小工具，避免引入 sort 的多处置名）。
func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// tailBuffer 是固定容量的尾部缓冲。
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	cap int
}

func newTailBuffer(capacity int) *tailBuffer {
	return &tailBuffer{buf: make([]byte, 0, capacity), cap: capacity}
}

func (b *tailBuffer) write(p []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.cap {
		b.buf = b.buf[len(b.buf)-b.cap:]
	}
}

func (b *tailBuffer) tail() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
