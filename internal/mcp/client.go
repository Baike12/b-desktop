package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

const (
	// protocolVersion 是客户端声明的 MCP 协议版本。
	// 取基线 2024-11-05：所有现行服务器都接受，新版服务器会在 initialize
	// 响应里回自己的版本，客户端按响应处理可选字段。
	protocolVersion = "2024-11-05"
	// clientName 是 initialize 中上报的客户端名。
	clientName = "b-desktop"
	// clientVersion 是 initialize 中上报的客户端版本。
	clientVersion = "0.1.0"
	// maxToolPages 是 tools/list 分页的上限，防止服务器返回循环游标。
	maxToolPages = 100
)

// 客户端错误哨兵。
var (
	// ErrInvalidArguments 表示工具参数不是 JSON 对象。
	ErrInvalidArguments = errors.New("mcp: invalid tool arguments")
	// ErrClientClosed 表示会话已关闭。
	ErrClientClosed = errors.New("mcp: client closed")
)

// ServerInfo 是服务器在 initialize 响应中返回的标识。
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ToolAnnotations 是 MCP 工具的行为提示。
type ToolAnnotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    *bool  `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool  `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool  `json:"openWorldHint,omitempty"`
}

// ToolInfo 是 tools/list 返回的远端工具描述。
type ToolInfo struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	InputSchema json.RawMessage  `json:"inputSchema"`
	Annotations *ToolAnnotations `json:"annotations,omitempty"`
}

// CallToolResult 是 tools/call 的原始结果；Content 保持服务器给出的事实格式。
type CallToolResult struct {
	Content           []json.RawMessage `json:"content"`
	StructuredContent json.RawMessage   `json:"structuredContent,omitempty"`
	IsError           bool              `json:"isError,omitempty"`
}

// rpcReply 是一条响应的投递结果。
type rpcReply struct {
	result json.RawMessage
	err    error
}

// rpcResponse 是客户端回给服务器的响应（目前只用于 ping）。
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// Client 是一条已建立传输的 MCP 会话。
//
// 并发安全：请求可并行发出；写入串行化；响应按 id 分发。
// 生命周期由 Close 结束，传输断开后所有在途请求立即返回错误。
type Client struct {
	name      string
	transport io.ReadWriteCloser

	writeMu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan rpcReply
	closed  bool
	readErr error
	done    chan struct{}

	closeOnce sync.Once

	initMu          sync.Mutex
	initialized     bool
	protocolVersion string
	serverInfo      ServerInfo
	instructions    string
	toolsSupported  bool

	dirtyMu sync.Mutex
	dirty   bool
}

// NewClient 在给定传输上创建会话并启动读取循环，不发送任何请求。
// 调用方必须先调用 Initialize 再使用其它方法。
func NewClient(name string, transport io.ReadWriteCloser) *Client {
	c := &Client{
		name:      name,
		transport: transport,
		pending:   make(map[int64]chan rpcReply),
		done:      make(chan struct{}),
	}
	go c.readLoop()
	return c
}

// Name 返回会话对应的服务器配置名。
func (c *Client) Name() string { return c.name }

// Initialize 发送 initialize 请求与 notifications/initialized 通知。
// 重复调用直接返回 nil；失败时连接仍可用，但服务器未完成握手。
func (c *Client) Initialize(ctx context.Context) error {
	c.initMu.Lock()
	defer c.initMu.Unlock()
	if c.initialized {
		return nil
	}
	params := map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]string{
			"name":    clientName,
			"version": clientVersion,
		},
	}
	raw, err := c.request(ctx, "initialize", params)
	if err != nil {
		return fmt.Errorf("mcp: initialize %q: %w", c.name, err)
	}
	var res struct {
		ProtocolVersion string                     `json:"protocolVersion"`
		Capabilities    map[string]json.RawMessage `json:"capabilities"`
		ServerInfo      ServerInfo                 `json:"serverInfo"`
		Instructions    string                     `json:"instructions"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("mcp: initialize %q: decode result: %w", c.name, err)
	}
	c.protocolVersion = res.ProtocolVersion
	c.serverInfo = res.ServerInfo
	c.instructions = res.Instructions
	_, c.toolsSupported = res.Capabilities["tools"]

	if err := c.notify(ctx, "notifications/initialized", nil); err != nil {
		return fmt.Errorf("mcp: initialize %q: %w", c.name, err)
	}
	c.initialized = true
	return nil
}

// ProtocolVersion 返回服务器在 initialize 响应中选择的协议版本。
func (c *Client) ProtocolVersion() string {
	c.initMu.Lock()
	defer c.initMu.Unlock()
	return c.protocolVersion
}

// ServerInfo 返回服务器的自报名称与版本。
func (c *Client) ServerInfo() ServerInfo {
	c.initMu.Lock()
	defer c.initMu.Unlock()
	return c.serverInfo
}

// Instructions 返回服务器在 initialize 响应中给出的使用说明，可能为空。
func (c *Client) Instructions() string {
	c.initMu.Lock()
	defer c.initMu.Unlock()
	return c.instructions
}

// ToolsSupported 报告服务器是否声明了 tools 能力；未声明时 ListTools 返回空。
func (c *Client) ToolsSupported() bool {
	c.initMu.Lock()
	defer c.initMu.Unlock()
	return c.toolsSupported
}

// ListTools 拉取全部远端工具，自动翻页（nextCursor）。
func (c *Client) ListTools(ctx context.Context) ([]ToolInfo, error) {
	if !c.ToolsSupported() {
		return nil, nil
	}
	var out []ToolInfo
	seen := make(map[string]bool, 4)
	cursor := ""
	for page := 0; page < maxToolPages; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.request(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var res struct {
			Tools      []ToolInfo `json:"tools"`
			NextCursor string     `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, fmt.Errorf("mcp: tools/list on %q: decode result: %w", c.name, err)
		}
		out = append(out, res.Tools...)
		if res.NextCursor == "" || seen[res.NextCursor] {
			break
		}
		seen[res.NextCursor] = true
		cursor = res.NextCursor
	}
	return out, nil
}

// CallTool 调用一个远端工具。args 必须是 JSON 对象（空值按 {} 处理）。
// JSON-RPC 错误以 *RPCError 返回；工具自身的失败放在结果的 IsError 中。
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (CallToolResult, error) {
	if len(args) == 0 || string(bytes.TrimSpace(args)) == "null" {
		args = json.RawMessage(`{}`)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(args, &probe); err != nil {
		return CallToolResult{}, fmt.Errorf("%w: %s on %q: %v", ErrInvalidArguments, name, c.name, err)
	}
	params := struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}{Name: name, Arguments: args}

	raw, err := c.request(ctx, "tools/call", params)
	if err != nil {
		return CallToolResult{}, err
	}
	var res CallToolResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return CallToolResult{}, fmt.Errorf("mcp: tools/call %s on %q: decode result: %w", name, c.name, err)
	}
	return res, nil
}

// Close 结束会话：在途请求返回错误，传输被关闭。可重复调用。
func (c *Client) Close() error {
	c.fail(fmt.Errorf("%w: %q", ErrClientClosed, c.name))
	var err error
	c.closeOnce.Do(func() {
		err = c.transport.Close()
	})
	return err
}

// Closed 报告会话是否已经结束。
func (c *Client) Closed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// Err 返回会话结束的原因；正常关闭时是 ErrClientClosed。
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readErr != nil {
		return c.readErr
	}
	if c.closed {
		return ErrClientClosed
	}
	return nil
}

// Dirty 报告服务器是否发过 notifications/tools/list_changed。
func (c *Client) Dirty() bool {
	c.dirtyMu.Lock()
	defer c.dirtyMu.Unlock()
	return c.dirty
}

// ClearDirty 清除工具列表已变化的标记。
func (c *Client) ClearDirty() {
	c.dirtyMu.Lock()
	defer c.dirtyMu.Unlock()
	c.dirty = false
}

// notify 发送一条不需要响应的通知。
func (c *Client) notify(ctx context.Context, method string, params any) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("mcp: %s on %q: %w", method, c.name, err)
	}
	if err := c.write(rpcNotification{JSONRPC: jsonrpcVersion, Method: method, Params: params}); err != nil {
		return fmt.Errorf("mcp: %s on %q: %w", method, c.name, err)
	}
	return nil
}

// request 发送请求并等待同 id 的响应。
func (c *Client) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id, ch, err := c.register()
	if err != nil {
		return nil, fmt.Errorf("mcp: %s on %q: %w", method, c.name, err)
	}
	if err := c.write(rpcRequest{JSONRPC: jsonrpcVersion, ID: id, Method: method, Params: params}); err != nil {
		c.unregister(id)
		return nil, fmt.Errorf("mcp: %s on %q: %w", method, c.name, err)
	}
	select {
	case <-ctx.Done():
		c.unregister(id)
		return nil, fmt.Errorf("mcp: %s on %q: %w", method, c.name, ctx.Err())
	case <-c.done:
		c.unregister(id)
		return nil, fmt.Errorf("mcp: %s on %q: %w", method, c.name, c.Err())
	case reply := <-ch:
		if reply.err != nil {
			return nil, fmt.Errorf("mcp: %s on %q: %w", method, c.name, reply.err)
		}
		return reply.result, nil
	}
}

// register 分配请求 id 并登记响应通道。
func (c *Client) register() (int64, chan rpcReply, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, nil, c.readErrOrClosedLocked()
	}
	c.nextID++
	id := c.nextID
	ch := make(chan rpcReply, 1)
	c.pending[id] = ch
	return id, ch, nil
}

// unregister 丢弃一个未完成的请求。
func (c *Client) unregister(id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.pending, id)
}

// write 序列化并写入一条消息，自动补换行（stdio 传输的帧分隔）。
func (c *Client) write(msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("encode message: %w", err)
	}
	data = append(data, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.transport.Write(data); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	return nil
}

// readLoop 逐行读取入站消息直到传输断开。
func (c *Client) readLoop() {
	reader := bufio.NewReader(c.transport)
	for {
		line, err := reader.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			c.handleMessage(trimmed)
		}
		if err != nil {
			c.fail(c.describeReadError(err))
			return
		}
	}
}

// handleMessage 分发一条入站消息：响应、服务器请求或通知。
func (c *Client) handleMessage(line []byte) {
	var msg rpcMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		// 无法解析的行直接丢弃：JSON-RPC 2.0 不允许对通知回错误。
		return
	}
	switch {
	case msg.Method != "" && len(msg.ID) > 0:
		c.handleServerRequest(msg)
	case msg.Method != "":
		if msg.Method == "notifications/tools/list_changed" {
			c.dirtyMu.Lock()
			c.dirty = true
			c.dirtyMu.Unlock()
		}
	default:
		c.handleResponse(msg)
	}
}

// handleResponse 把响应投递给等待中的请求。
func (c *Client) handleResponse(msg rpcMessage) {
	id, ok := decodeID(msg.ID)
	if !ok {
		return
	}
	c.mu.Lock()
	ch := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if ch == nil {
		return
	}
	reply := rpcReply{result: msg.Result}
	if msg.Error != nil {
		reply.err = msg.Error
	}
	ch <- reply
}

// handleServerRequest 回应服务器发起的请求；只支持 ping，其它方法回 -32601。
func (c *Client) handleServerRequest(msg rpcMessage) {
	if msg.Method == "ping" {
		_ = c.write(rpcResponse{JSONRPC: jsonrpcVersion, ID: msg.ID, Result: map[string]any{}})
		return
	}
	_ = c.write(rpcResponse{
		JSONRPC: jsonrpcVersion,
		ID:      msg.ID,
		Error: &RPCError{
			Code:    -32601,
			Message: "method not found: " + msg.Method,
		},
	})
}

// fail 标记会话结束并把错误投递给所有在途请求。可重复调用。
func (c *Client) fail(err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.readErr = err
	pending := c.pending
	c.pending = make(map[int64]chan rpcReply)
	close(c.done)
	c.mu.Unlock()

	for _, ch := range pending {
		ch <- rpcReply{err: err}
	}
}

// readErrOrClosedLocked 在持锁状态下给出会话结束原因。
func (c *Client) readErrOrClosedLocked() error {
	if c.readErr != nil {
		return c.readErr
	}
	return ErrClientClosed
}

// describeReadError 把读错误转成带上下文的错误；stdio 传输还会附上 stderr 末尾。
func (c *Client) describeReadError(err error) error {
	if errors.Is(err, io.EOF) {
		err = errors.New("connection closed by server")
	}
	detail := err.Error()
	if tailer, ok := c.transport.(interface{ StderrTail() string }); ok {
		detail += tailSuffix(tailer.StderrTail())
	}
	return fmt.Errorf("mcp: server %q: %s", c.name, detail)
}
