package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// fakeRequest 是测试服务器收到的一条请求。
type fakeRequest struct {
	id     json.RawMessage
	method string
	params json.RawMessage
}

// fakePeer 在内存传输上扮演 MCP 服务器：逐行读 JSON-RPC，按 handler 响应。
type fakePeer struct {
	t       *testing.T
	conn    io.ReadWriteCloser
	handler func(*fakePeer, fakeRequest)

	writeMu sync.Mutex

	mu      sync.Mutex
	methods []string
	params  []json.RawMessage
	done    chan struct{}
}

// newFakePeer 启动读循环；handler 收到请求后通过 respond / respondError 回复。
func newFakePeer(t *testing.T, conn io.ReadWriteCloser, handler func(*fakePeer, fakeRequest)) *fakePeer {
	t.Helper()
	p := &fakePeer{t: t, conn: conn, handler: handler, done: make(chan struct{})}
	go p.loop()
	return p
}

// loop 读取换行分隔的消息并交给 handler。
func (p *fakePeer) loop() {
	defer close(p.done)
	reader := bufio.NewReader(p.conn)
	for {
		line, err := reader.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			var msg struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if decodeErr := json.Unmarshal(trimmed, &msg); decodeErr != nil {
				p.t.Errorf("fake peer: decode line %q: %v", trimmed, decodeErr)
				return
			}
			p.mu.Lock()
			p.methods = append(p.methods, msg.Method)
			p.params = append(p.params, msg.Params)
			p.mu.Unlock()
			if p.handler != nil {
				p.handler(p, fakeRequest{id: msg.ID, method: msg.Method, params: msg.Params})
			}
		}
		if err != nil {
			return
		}
	}
}

// respond 回一条成功响应。
func (p *fakePeer) respond(id json.RawMessage, result any) {
	p.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

// respondError 回一条 JSON-RPC 错误。
func (p *fakePeer) respondError(id json.RawMessage, code int, message string) {
	p.send(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
}

// notify 发一条服务器通知。
func (p *fakePeer) notify(method string, params any) {
	p.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// send 写入一条消息。
func (p *fakePeer) send(msg any) {
	data, err := json.Marshal(msg)
	if err != nil {
		p.t.Errorf("fake peer: encode message: %v", err)
		return
	}
	data = append(data, '\n')
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if _, err := p.conn.Write(data); err != nil {
		p.t.Logf("fake peer: write: %v", err)
	}
}

// Close 关闭服务器端连接。
func (p *fakePeer) Close() { _ = p.conn.Close() }

// waitMethod 等待服务器收到指定方法的消息（通知是异步到达的）。
func (p *fakePeer) waitMethod(method string) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		for _, got := range p.methods {
			if got == method {
				p.mu.Unlock()
				return true
			}
		}
		p.mu.Unlock()
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

// methodsSnapshot 返回已收到的方法名副本。
func (p *fakePeer) methodsSnapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.methods...)
}

// paramsSnapshot 返回已收到的参数副本。
func (p *fakePeer) paramsSnapshot() []json.RawMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]json.RawMessage(nil), p.params...)
}

// newTestClient 建立一对内存连接与假服务器。
func newTestClient(t *testing.T, handler func(*fakePeer, fakeRequest)) (*Client, *fakePeer) {
	t.Helper()
	clientSide, serverSide := net.Pipe()
	peer := newFakePeer(t, serverSide, handler)
	t.Cleanup(peer.Close)
	return NewClient("demo", clientSide), peer
}

// TestClientHandshake 覆盖 initialize、notifications/initialized、tools/list 与 tools/call。
func TestClientHandshake(t *testing.T) {
	client, peer := newTestClient(t, func(p *fakePeer, req fakeRequest) {
		switch req.method {
		case "initialize":
			p.respond(req.id, map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{"tools": map[string]any{"listChanged": true}},
				"serverInfo":      map[string]any{"name": "demo-server", "version": "1.2.3"},
				"instructions":    "use me",
			})
		case "tools/list":
			p.respond(req.id, map[string]any{"tools": []map[string]any{{
				"name":        "search",
				"description": "Search things",
				"inputSchema": map[string]any{"type": "object"},
				"annotations": map[string]any{"readOnlyHint": true},
			}}})
		case "tools/call":
			p.respond(req.id, map[string]any{
				"content":           []map[string]any{{"type": "text", "text": "found it"}},
				"structuredContent": map[string]any{"n": 1},
			})
		}
	})
	defer client.Close()
	if client.Name() != "demo" {
		t.Fatalf("Name = %q", client.Name())
	}

	ctx := contextWithTimeout(t)
	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if got := client.ProtocolVersion(); got != "2024-11-05" {
		t.Fatalf("ProtocolVersion = %q", got)
	}
	if info := client.ServerInfo(); info.Name != "demo-server" || info.Version != "1.2.3" {
		t.Fatalf("ServerInfo = %+v", info)
	}
	if client.Instructions() != "use me" {
		t.Fatalf("Instructions = %q", client.Instructions())
	}
	if !client.ToolsSupported() {
		t.Fatal("ToolsSupported = false")
	}
	if !peer.waitMethod("notifications/initialized") {
		t.Fatal("server never received notifications/initialized")
	}

	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "search" {
		t.Fatalf("tools = %+v", tools)
	}

	res, err := client.CallTool(ctx, "search", json.RawMessage(`{"q":"x"}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatal("CallTool IsError = true")
	}
	want := "found it\nstructuredContent: {\"n\":1}"
	if got := renderCallResult(res); got != want {
		t.Fatalf("renderCallResult = %q, want %q", got, want)
	}
}

// TestClientInitializeWithoutToolsCapability 覆盖服务器未声明 tools 能力的情况。
func TestClientInitializeWithoutToolsCapability(t *testing.T) {
	client, _ := newTestClient(t, func(p *fakePeer, req fakeRequest) {
		if req.method == "initialize" {
			p.respond(req.id, map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}})
		}
	})
	defer client.Close()

	ctx := contextWithTimeout(t)
	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if client.ToolsSupported() {
		t.Fatal("ToolsSupported = true, want false")
	}
	tools, err := client.ListTools(ctx)
	if err != nil || tools != nil {
		t.Fatalf("ListTools = %v, %v; want nil, nil", tools, err)
	}
}

// TestClientRPCError 覆盖 tools/call 返回 JSON-RPC 错误。
func TestClientRPCError(t *testing.T) {
	client, _ := newTestClient(t, func(p *fakePeer, req fakeRequest) {
		switch req.method {
		case "initialize":
			p.respond(req.id, map[string]any{"capabilities": map[string]any{"tools": map[string]any{}}})
		case "tools/call":
			p.respondError(req.id, -32000, "boom")
		}
	})
	defer client.Close()

	ctx := contextWithTimeout(t)
	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	_, err := client.CallTool(ctx, "bad", nil)
	if err == nil {
		t.Fatal("CallTool error = nil")
	}
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != -32000 {
		t.Fatalf("error = %v, want *RPCError code -32000", err)
	}
}

// TestClientContextCanceled 覆盖调用方取消等待。
func TestClientContextCanceled(t *testing.T) {
	client, _ := newTestClient(t, func(p *fakePeer, req fakeRequest) {
		if req.method == "initialize" {
			p.respond(req.id, map[string]any{"capabilities": map[string]any{"tools": map[string]any{}}})
		}
		// tools/list 故意不响应。
	})
	defer client.Close()

	if err := client.Initialize(contextWithTimeout(t)); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	ctx, cancel := contextWithTimeoutValue(30 * time.Millisecond)
	defer cancel()
	if _, err := client.ListTools(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ListTools error = %v, want DeadlineExceeded", err)
	}
}

// TestClientToolsPagination 覆盖 nextCursor 翻页与游标参数。
func TestClientToolsPagination(t *testing.T) {
	client, peer := newTestClient(t, func(p *fakePeer, req fakeRequest) {
		switch req.method {
		case "initialize":
			p.respond(req.id, map[string]any{"capabilities": map[string]any{"tools": map[string]any{}}})
		case "tools/list":
			var params struct {
				Cursor string `json:"cursor"`
			}
			_ = json.Unmarshal(req.params, &params)
			if params.Cursor == "" {
				p.respond(req.id, map[string]any{
					"tools":      []map[string]any{{"name": "one", "inputSchema": map[string]any{"type": "object"}}},
					"nextCursor": "page-2",
				})
				return
			}
			p.respond(req.id, map[string]any{
				"tools": []map[string]any{{"name": "two", "inputSchema": map[string]any{"type": "object"}}},
			})
		}
	})
	defer client.Close()

	ctx := contextWithTimeout(t)
	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 2 || tools[0].Name != "one" || tools[1].Name != "two" {
		t.Fatalf("tools = %+v", tools)
	}
	params := peer.paramsSnapshot()
	if len(params) < 3 {
		t.Fatalf("params = %v", params)
	}
	if !bytes.Contains(params[len(params)-1], []byte(`"cursor":"page-2"`)) {
		t.Fatalf("second page params = %s, want cursor", params[len(params)-1])
	}
}

// TestClientDirtyFlag 覆盖 notifications/tools/list_changed。
func TestClientDirtyFlag(t *testing.T) {
	client, peer := newTestClient(t, func(p *fakePeer, req fakeRequest) {
		if req.method == "initialize" {
			p.respond(req.id, map[string]any{"capabilities": map[string]any{"tools": map[string]any{}}})
		}
	})
	defer client.Close()

	if err := client.Initialize(contextWithTimeout(t)); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if client.Dirty() {
		t.Fatal("Dirty = true before notification")
	}
	peer.notify("notifications/tools/list_changed", map[string]any{})
	deadline := time.Now().Add(2 * time.Second)
	for !client.Dirty() && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if !client.Dirty() {
		t.Fatal("Dirty stayed false after list_changed")
	}
	client.ClearDirty()
	if client.Dirty() {
		t.Fatal("Dirty = true after ClearDirty")
	}
}

// TestClientInvalidArguments 覆盖非对象参数。
func TestClientInvalidArguments(t *testing.T) {
	client, _ := newTestClient(t, nil)
	defer client.Close()
	if _, err := client.CallTool(contextWithTimeout(t), "x", json.RawMessage(`[1,2]`)); !errors.Is(err, ErrInvalidArguments) {
		t.Fatalf("error = %v, want ErrInvalidArguments", err)
	}
}

// TestClientClosedError 覆盖连接结束后在途请求返回 ErrClientClosed。
func TestClientClosedError(t *testing.T) {
	client, _ := newTestClient(t, func(p *fakePeer, req fakeRequest) {
		if req.method == "initialize" {
			p.respond(req.id, map[string]any{"capabilities": map[string]any{"tools": map[string]any{}}})
		}
	})
	if err := client.Initialize(contextWithTimeout(t)); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !client.Closed() {
		t.Fatal("Closed = false")
	}
	if !errors.Is(client.Err(), ErrClientClosed) {
		t.Fatalf("Err = %v, want ErrClientClosed", client.Err())
	}
	if _, err := client.ListTools(contextWithTimeout(t)); !errors.Is(err, ErrClientClosed) {
		t.Fatalf("ListTools error = %v, want ErrClientClosed", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestServerRequestPing 覆盖服务器发起的 ping 请求被正确应答。
func TestServerRequestPing(t *testing.T) {
	client, peer := newTestClient(t, func(p *fakePeer, req fakeRequest) {
		if req.method == "initialize" {
			p.respond(req.id, map[string]any{"capabilities": map[string]any{"tools": map[string]any{}}})
		}
	})
	defer client.Close()
	if err := client.Initialize(contextWithTimeout(t)); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	peer.send(map[string]any{"jsonrpc": "2.0", "id": 99, "method": "ping"})
	// 客户端对 ping 的响应没有 method，fakePeer 会把它记成空方法名。
	if !peer.waitMethod("") {
		t.Fatal("client did not answer server ping")
	}
	if client.Closed() {
		t.Fatalf("client closed after ping: %v", client.Err())
	}
}
