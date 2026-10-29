package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"sync"
	"time"
)

// 外部 ACP 客户端的握手超时，以及进程异常退出时随错误返回的 stderr 尾部长度。
const (
	handshakeTimeout = 60 * time.Second
	stderrTailBytes  = 8 * 1024
)

// acp 错误码（v1/error.rs）。
const errCodeAuthRequired = -32000

// ClientOptions 是外部 ACP 进程的启动参数。
type ClientOptions struct {
	// Command 是可执行文件路径。
	Command string
	// Args 是追加的参数。
	Args []string
	// Env 覆盖/追加子进程环境变量；nil 表示继承父进程。
	Env map[string]string
	// Version 是握手 clientInfo.version。
	Version string
}

// ExternalBackend 通过 ACP v1（换行分隔 JSON-RPC over stdio）驱动一个外部
// Agent 进程。后续接入 codex 等外部 Agent 时复用该实现。
type ExternalBackend struct {
	opts ClientOptions
}

// NewExternalBackend 创建外部 ACP 后端。
func NewExternalBackend(opts ClientOptions) *ExternalBackend {
	return &ExternalBackend{opts: opts}
}

// Info 返回后端描述。
func (b *ExternalBackend) Info() AgentInfo {
	return AgentInfo{ID: "external", Name: b.opts.Command, Description: "外部 ACP agent 进程"}
}

// NewSession 启动外部进程并完成 initialize 握手，然后创建会话。
func (b *ExternalBackend) NewSession(ctx context.Context, params SessionParams, sink UpdateSink, client Client) (Session, error) {
	conn, err := startExternalProcess(b.opts)
	if err != nil {
		return nil, err
	}
	cleanup := func() { _ = conn.close() }

	initCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	initParams, _ := json.Marshal(map[string]any{
		"protocolVersion": ProtocolVersion,
		"clientCapabilities": map[string]any{
			"fs":       map[string]any{"readTextFile": true, "writeTextFile": true},
			"terminal": false,
		},
		"clientInfo": map[string]any{"name": "b-desktop", "version": b.opts.Version},
	})
	var initResult struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if err := conn.request(initCtx, "initialize", initParams, &initResult); err != nil {
		cleanup()
		return nil, fmt.Errorf("acp: initialize %s: %w", b.opts.Command, err)
	}
	if initResult.ProtocolVersion < ProtocolVersion {
		cleanup()
		return nil, fmt.Errorf("acp: agent %s unsupported protocol version %d", b.opts.Command, initResult.ProtocolVersion)
	}

	servers := make([]map[string]any, 0, len(params.MCPServers))
	for _, server := range params.MCPServers {
		servers = append(servers, wireMCPServer(server))
	}
	newParams, _ := json.Marshal(map[string]any{"cwd": params.CWD, "mcpServers": servers})
	var newResult struct {
		SessionID string        `json:"sessionId"`
		Modes     *SessionModes `json:"modes"`
	}
	if err := conn.request(initCtx, "session/new", newParams, &newResult); err != nil {
		cleanup()
		return nil, fmt.Errorf("acp: session/new %s: %w", b.opts.Command, err)
	}

	session := &externalSession{
		conn:      conn,
		client:    client,
		sink:      sink,
		sessionID: newResult.SessionID,
	}
	if newResult.Modes != nil {
		session.modes = *newResult.Modes
	}
	conn.onUpdate = session.handleUpdate
	conn.onRequest = session.handleAgentRequest
	return session, nil
}

// wireMCPServer 把内部 MCP 配置转成 ACP 线格式。
func wireMCPServer(server MCPServer) map[string]any {
	base := map[string]any{"name": server.Name}
	switch server.Type {
	case "http", "sse":
		base["type"] = server.Type
		base["url"] = server.URL
		base["headers"] = namedValues(server.Headers)
	default:
		base["command"] = server.Command
		base["args"] = server.Args
		base["env"] = namedValues(server.Env)
	}
	return base
}

// namedValues 把 map 转成 ACP 的 {name,value} 列表，按名字排序保证稳定。
func namedValues(values map[string]string) []map[string]string {
	out := make([]map[string]string, 0, len(values))
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		out = append(out, map[string]string{"name": name, "value": values[name]})
	}
	return out
}

// wireUpdate 是 session/update 通知的原始载荷。content 字段在不同变体里形状
// 不同（chunk 是单个 ContentBlock，tool_call 是数组），必须按 sessionUpdate
// 分流后各自解析。
type wireUpdate struct {
	SessionUpdate string          `json:"sessionUpdate"`
	Raw           json.RawMessage `json:"-"`
}

// wireContentChunk 是 chunk 类变体。
type wireContentChunk struct {
	Content   *ContentBlock `json:"content"`
	MessageID string        `json:"messageId,omitempty"`
}

// wireToolCallUpdate 是 tool_call / tool_call_update 变体（字段平铺）。
type wireToolCallUpdate struct {
	ToolCallID string             `json:"toolCallId"`
	Title      string             `json:"title,omitempty"`
	Kind       ToolKind           `json:"kind,omitempty"`
	Status     ToolCallStatus     `json:"status,omitempty"`
	Content    []ToolCallContent  `json:"content,omitempty"`
	Locations  []ToolCallLocation `json:"locations,omitempty"`
	RawInput   json.RawMessage    `json:"rawInput,omitempty"`
	RawOutput  json.RawMessage    `json:"rawOutput,omitempty"`
}

// wirePlanUpdate 是 plan 变体。
type wirePlanUpdate struct {
	Entries []PlanEntry `json:"entries"`
}

// wireCommandsUpdate 是 available_commands_update 变体。
type wireCommandsUpdate struct {
	AvailableCommands []AvailableCommand `json:"availableCommands"`
}

// convertWireUpdate 把线格式更新转成内部 SessionUpdate。
func convertWireUpdate(sessionID string, raw wireUpdate) (SessionUpdate, error) {
	out := SessionUpdate{Kind: UpdateKind(raw.SessionUpdate), SessionID: sessionID}
	switch UpdateKind(raw.SessionUpdate) {
	case UpdateUserMessageChunk, UpdateAgentMessageChunk, UpdateAgentThoughtChunk:
		var chunk wireContentChunk
		if err := json.Unmarshal(raw.Raw, &chunk); err != nil {
			return out, fmt.Errorf("acp: decode chunk update: %w", err)
		}
		out.Content = chunk.Content
		out.MessageID = chunk.MessageID
	case UpdateToolCall:
		var call wireToolCallUpdate
		if err := json.Unmarshal(raw.Raw, &call); err != nil {
			return out, fmt.Errorf("acp: decode tool_call: %w", err)
		}
		out.ToolCall = &ToolCall{
			ToolCallID: call.ToolCallID,
			Title:      call.Title,
			Kind:       call.Kind,
			Status:     call.Status,
			Content:    call.Content,
			Locations:  call.Locations,
			RawInput:   call.RawInput,
			RawOutput:  call.RawOutput,
		}
	case UpdateToolCallUpdate:
		var call wireToolCallUpdate
		if err := json.Unmarshal(raw.Raw, &call); err != nil {
			return out, fmt.Errorf("acp: decode tool_call_update: %w", err)
		}
		out.ToolCallUpdate = &ToolCallUpdate{
			ToolCallID: call.ToolCallID,
			Title:      call.Title,
			Kind:       call.Kind,
			Status:     call.Status,
			Content:    call.Content,
			Locations:  call.Locations,
			RawInput:   call.RawInput,
			RawOutput:  call.RawOutput,
		}
	case UpdatePlan:
		var plan wirePlanUpdate
		if err := json.Unmarshal(raw.Raw, &plan); err != nil {
			return out, fmt.Errorf("acp: decode plan: %w", err)
		}
		out.Plan = &Plan{Entries: plan.Entries}
	case UpdateAvailableCommands:
		var commands wireCommandsUpdate
		if err := json.Unmarshal(raw.Raw, &commands); err != nil {
			return out, fmt.Errorf("acp: decode commands: %w", err)
		}
		out.Commands = commands.AvailableCommands
	case UpdateCurrentMode:
		var mode struct {
			CurrentModeID string `json:"currentModeId"`
		}
		if err := json.Unmarshal(raw.Raw, &mode); err != nil {
			return out, fmt.Errorf("acp: decode mode update: %w", err)
		}
		out.CurrentModeID = mode.CurrentModeID
	default:
		// 未知变体忽略（前向兼容），调用方按零更新跳过。
		return out, errors.New("acp: unknown update kind")
	}
	return out, nil
}

// externalSession 是外部 Agent 的一个会话。
type externalSession struct {
	conn      *externalConn
	client    Client
	sink      UpdateSink
	sessionID string
	modes     SessionModes

	closeOnce sync.Once
}

// handleUpdate 解析入站 session/update 并推给 sink。
func (s *externalSession) handleUpdate(raw json.RawMessage) {
	if s.sink == nil {
		return
	}
	update, err := convertWireUpdate(s.sessionID, wireUpdate{Raw: raw})
	if err != nil {
		return
	}
	s.sink(update)
}

// handleAgentRequest 处理 agent→client 请求，返回应答 result。
func (s *externalSession) handleAgentRequest(ctx context.Context, method string, params json.RawMessage) (any, *rpcError) {
	switch method {
	case "session/request_permission":
		var req struct {
			ToolCall ToolCallUpdate     `json:"toolCall"`
			Options  []PermissionOption `json:"options"`
		}
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid params: " + err.Error()}
		}
		outcome, err := s.client.RequestPermission(ctx, PermissionRequest{ToolCall: req.ToolCall, Options: req.Options})
		if err != nil {
			return nil, &rpcError{Code: -32603, Message: err.Error()}
		}
		if outcome.Cancelled {
			return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}, nil
		}
		return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": outcome.OptionID}}, nil
	case "fs/read_text_file":
		var req struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid params: " + err.Error()}
		}
		content, err := s.client.ReadTextFile(ctx, req.Path)
		if err != nil {
			return nil, &rpcError{Code: -32603, Message: err.Error()}
		}
		return map[string]any{"content": content}, nil
	case "fs/write_text_file":
		var req struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid params: " + err.Error()}
		}
		if err := s.client.WriteTextFile(ctx, req.Path, req.Content); err != nil {
			return nil, &rpcError{Code: -32603, Message: err.Error()}
		}
		return map[string]any{}, nil
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found: " + method}
	}
}

func (s *externalSession) Prompt(ctx context.Context, blocks []ContentBlock) (PromptResult, error) {
	params, err := json.Marshal(map[string]any{"sessionId": s.sessionID, "prompt": blocks})
	if err != nil {
		return PromptResult{}, fmt.Errorf("acp: encode prompt: %w", err)
	}
	var result struct {
		StopReason StopReason `json:"stopReason"`
	}
	if err := s.conn.request(ctx, "session/prompt", params, &result); err != nil {
		return PromptResult{}, err
	}
	return PromptResult{StopReason: result.StopReason}, nil
}

func (s *externalSession) Cancel() {
	s.conn.notify("session/cancel", map[string]any{"sessionId": s.sessionID})
}

func (s *externalSession) SetMode(ctx context.Context, modeID string) error {
	params, _ := json.Marshal(map[string]any{"sessionId": s.sessionID, "modeId": modeID})
	return s.conn.request(ctx, "session/set_mode", params, nil)
}

func (s *externalSession) Modes() SessionModes { return s.modes }

func (s *externalSession) Close() error {
	s.closeOnce.Do(func() { _ = s.conn.close() })
	return nil
}

// rpcError 是 JSON-RPC 错误对象。
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return "acp: nil error"
	}
	return fmt.Sprintf("acp: rpc error %d: %s", e.Code, e.Message)
}

// authRequired 报告错误是否为「需要登录」。
func (e *rpcError) authRequired() bool { return e != nil && e.Code == errCodeAuthRequired }

// externalConn 管理与外部 ACP 进程的双向 JSON-RPC 连接。
type externalConn struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	writeMu sync.Mutex

	nextID    int64
	pendingMu sync.Mutex
	pending   map[string]chan rpcResponse

	onUpdate  func(json.RawMessage)
	onRequest func(ctx context.Context, method string, params json.RawMessage) (any, *rpcError)

	stderrTail *stderrRing
	closeOnce  sync.Once
	done       chan struct{}
	procErr    error
}

type rpcResponse struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

// startExternalProcess 启动外部 ACP 进程并开始读循环。
func startExternalProcess(opts ClientOptions) (*externalConn, error) {
	if opts.Command == "" {
		return nil, errors.New("acp: empty command")
	}
	cmd := exec.Command(opts.Command, opts.Args...)
	cmd.Env = os.Environ()
	names := make([]string, 0, len(opts.Env))
	for name := range opts.Env {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		cmd.Env = append(cmd.Env, name+"="+opts.Env[name])
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("acp: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("acp: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("acp: stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("acp: spawn %s: %w", opts.Command, err)
	}

	conn := &externalConn{
		cmd:        cmd,
		stdin:      stdin,
		pending:    make(map[string]chan rpcResponse),
		stderrTail: newStderrRing(stderrTailBytes),
		done:       make(chan struct{}),
	}
	go conn.readLoop(stdout)
	go conn.drainStderr(stderr)
	go conn.waitExit()
	return conn, nil
}

// request 发送一个 JSON-RPC 请求并等待响应。result 可为 nil。
func (c *externalConn) request(ctx context.Context, method string, params json.RawMessage, result any) error {
	c.pendingMu.Lock()
	c.nextID++
	id := strconv.FormatInt(c.nextID, 10)
	ch := make(chan rpcResponse, 1)
	c.pending[id] = ch
	c.pendingMu.Unlock()

	payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": c.nextID, "method": method, "params": params})
	if err != nil {
		c.removePending(id)
		return fmt.Errorf("acp: encode %s: %w", method, err)
	}
	if err := c.writeLine(payload); err != nil {
		c.removePending(id)
		return fmt.Errorf("acp: send %s: %w", method, err)
	}

	select {
	case resp, open := <-ch:
		if !open {
			return fmt.Errorf("acp: %s: agent exited: %w", method, c.exitError())
		}
		if resp.Error != nil {
			return fmt.Errorf("acp: %s: %w", method, resp.Error)
		}
		if result != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, result); err != nil {
				return fmt.Errorf("acp: decode %s result: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		c.removePending(id)
		return fmt.Errorf("acp: %s: %w", method, ctx.Err())
	case <-c.done:
		c.removePending(id)
		return fmt.Errorf("acp: %s: agent exited: %w", method, c.exitError())
	}
}

// notify 发送一条 JSON-RPC 通知。
func (c *externalConn) notify(method string, params any) {
	payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	if err != nil {
		return
	}
	_ = c.writeLine(payload)
}

func (c *externalConn) writeLine(payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err := c.stdin.Write(append(payload, '\n'))
	return err
}

func (c *externalConn) removePending(id string) {
	c.pendingMu.Lock()
	delete(c.pending, id)
	c.pendingMu.Unlock()
}

// readLoop 逐行读取并分发入站消息。入站 JSON-RPC 允许任意顺序：
// 响应按 id 配对，通知与请求异步分发。
func (c *externalConn) readLoop(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		c.handleLine(line)
	}
}

// handleLine 解析一条入站 JSON-RPC 消息并分发。
func (c *externalConn) handleLine(line []byte) {
	var msg struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id,omitempty"`
		Method  string          `json:"method,omitempty"`
		Params  json.RawMessage `json:"params,omitempty"`
		Result  json.RawMessage `json:"result,omitempty"`
		Error   *rpcError       `json:"error,omitempty"`
	}
	if err := json.Unmarshal(line, &msg); err != nil {
		return // 容忍噪音行；ACP 帧一定是合法 JSON。
	}
	if msg.ID != nil && msg.Method == "" {
		c.resolvePending(string(msg.ID), rpcResponse{Result: msg.Result, Error: msg.Error})
		return
	}
	if msg.Method == "" {
		return
	}
	if msg.Method == "session/update" && c.onUpdate != nil {
		c.onUpdate(msg.Params)
		return
	}
	if c.onRequest != nil && msg.ID != nil {
		// agent→client 请求（权限审批、文件访问）可能长时间等待用户，
		// 不设超时；进程退出时随读循环结束而被放弃。
		go func(id json.RawMessage, method string, params json.RawMessage) {
			result, rpcErr := c.onRequest(context.Background(), method, params)
			c.respond(id, result, rpcErr)
		}(msg.ID, msg.Method, msg.Params)
	}
}

// resolvePending 按 id 配对响应；未配对的响应静默丢弃。
func (c *externalConn) resolvePending(id string, resp rpcResponse) {
	c.pendingMu.Lock()
	ch, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	c.pendingMu.Unlock()
	if ok {
		ch <- resp
	}
}

// respond 回应 agent→client 请求。
func (c *externalConn) respond(id json.RawMessage, result any, rpcErr *rpcError) {
	payload := map[string]any{"jsonrpc": "2.0", "id": id}
	if rpcErr != nil {
		payload["error"] = rpcErr
	} else {
		payload["result"] = result
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_ = c.writeLine(encoded)
}

// drainStderr 把 stderr 收进环形缓冲，供进程退出时拼进错误消息。
func (c *externalConn) drainStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 16*1024), 1024*1024)
	for scanner.Scan() {
		c.stderrTail.write(scanner.Bytes())
		c.stderrTail.write([]byte("\n"))
	}
}

// waitExit 等进程退出并关闭所有 in-flight 请求的通道。
func (c *externalConn) waitExit() {
	_ = c.cmd.Wait()
	c.procErr = fmt.Errorf("exit code %d", c.cmd.ProcessState.ExitCode())
	c.pendingMu.Lock()
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.pendingMu.Unlock()
	close(c.done)
}

// exitError 返回带 stderr 尾部的进程退出错误。
func (c *externalConn) exitError() error {
	if tail := c.stderrTail.tail(); tail != "" {
		return fmt.Errorf("%w; stderr: %s", c.procErr, tail)
	}
	return c.procErr
}

// close 关闭 stdin 并在宽限期后强杀进程，幂等。
func (c *externalConn) close() error {
	c.closeOnce.Do(func() {
		_ = c.stdin.Close()
		select {
		case <-c.done:
		default:
			go func() {
				time.Sleep(3 * time.Second)
				select {
				case <-c.done:
				default:
					_ = c.cmd.Process.Kill()
				}
			}()
		}
	})
	return nil
}

// stderrRing 是固定容量的 stderr 尾部缓冲。
type stderrRing struct {
	mu  sync.Mutex
	buf []byte
	cap int
}

func newStderrRing(capacity int) *stderrRing {
	return &stderrRing{buf: make([]byte, 0, capacity), cap: capacity}
}

func (r *stderrRing) write(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.cap {
		r.buf = r.buf[len(r.buf)-r.cap:]
	}
}

func (r *stderrRing) tail() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}
