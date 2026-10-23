package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Baike12/b-desktop/internal/tool"
)

// fakeDialer 用内存传输模拟多个 MCP 服务器，并记录连接次数。
type fakeDialer struct {
	t       *testing.T
	handler func(server string, p *fakePeer, req fakeRequest)

	mu      sync.Mutex
	calls   int
	servers []string
	peers   map[string]*fakePeer
	fail    map[string]error
	conns   []io.ReadWriteCloser
}

// newFakeDialer 创建拨号器；handler 为 nil 时使用默认的 initialize/tools 应答。
func newFakeDialer(t *testing.T) *fakeDialer {
	d := &fakeDialer{
		t:     t,
		peers: make(map[string]*fakePeer),
		fail:  make(map[string]error),
	}
	t.Cleanup(func() {
		d.mu.Lock()
		conns := append([]io.ReadWriteCloser(nil), d.conns...)
		d.mu.Unlock()
		for _, conn := range conns {
			_ = conn.Close()
		}
	})
	return d
}

// dial 建立一对内存连接并启动假服务器。
func (d *fakeDialer) dial(ctx context.Context, name string, cfg ServerConfig) (io.ReadWriteCloser, error) {
	d.mu.Lock()
	d.calls++
	d.servers = append(d.servers, name)
	failErr := d.fail[name]
	handler := d.handler
	d.mu.Unlock()
	if failErr != nil {
		return nil, failErr
	}

	clientSide, serverSide := net.Pipe()
	peerHandler := func(p *fakePeer, req fakeRequest) {
		if handler != nil {
			handler(name, p, req)
			return
		}
		defaultMCPHandler(name)(p, req)
	}
	peer := newFakePeer(d.t, serverSide, peerHandler)
	d.mu.Lock()
	d.peers[name] = peer
	d.conns = append(d.conns, serverSide)
	d.mu.Unlock()
	return clientSide, nil
}

// callCount 返回累计连接次数。
func (d *fakeDialer) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// peer 返回某个服务器的假实现。
func (d *fakeDialer) peer(name string) *fakePeer {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.peers[name]
}

// defaultMCPHandler 是标准应答：initialize 声明 tools 能力，tools/list 给出一个只读工具，
// tools/call 返回 "ok from <server>"。
func defaultMCPHandler(server string) func(*fakePeer, fakeRequest) {
	return func(p *fakePeer, req fakeRequest) {
		switch req.method {
		case "initialize":
			p.respond(req.id, map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{"tools": map[string]any{"listChanged": true}},
				"serverInfo":      map[string]any{"name": server + "-server", "version": "9.9.9"},
			})
		case "tools/list":
			p.respond(req.id, map[string]any{"tools": []map[string]any{{
				"name":        "search",
				"description": "Search " + server,
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"q": map[string]any{"type": "string"}},
				},
				"annotations": map[string]any{"readOnlyHint": true},
			}}})
		case "tools/call":
			p.respond(req.id, map[string]any{
				"content": []map[string]any{{"type": "text", "text": "ok from " + server}},
			})
		}
	}
}

// writeMCPConfig 在临时目录写入 mcp.json 并返回路径。
func writeMCPConfig(t *testing.T, servers map[string]ServerConfig) string {
	t.Helper()
	path := t.TempDir() + "/mcp.json"
	writeMCPConfigAt(t, path, servers)
	return path
}

// writeMCPConfigAt 覆盖写入指定路径的配置。
func writeMCPConfigAt(t *testing.T, path string, servers map[string]ServerConfig) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"mcpServers": servers})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := writeFile(path, data); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// TestManagerLazyStartAndCall 覆盖惰性启动、工具包装、调用与幂等关闭。
func TestManagerLazyStartAndCall(t *testing.T) {
	dialer := newFakeDialer(t)
	path := writeMCPConfig(t, map[string]ServerConfig{
		"alpha": {
			Command: "alpha-bin",
			Args:    []string{"--flag"},
			Env:     map[string]string{"TOKEN": "secret"},
		},
	})
	manager := NewManager(path, dialer.dial)
	if got := dialer.callCount(); got != 0 {
		t.Fatalf("dial called %d times before first use", got)
	}

	tools := manager.Tools()
	if len(tools) != 1 {
		t.Fatalf("Tools = %d, want 1", len(tools))
	}
	wrapped := tools[0]
	if got := wrapped.Name(); got != "mcp__alpha__search" {
		t.Fatalf("Name = %q", got)
	}
	if got := wrapped.Description(); got != "Search alpha" {
		t.Fatalf("Description = %q", got)
	}
	wrappedTool, ok := wrapped.(*ClientTool)
	if !ok {
		t.Fatalf("tool type = %T, want *ClientTool", wrapped)
	}
	if wrappedTool.Server() != "alpha" || wrappedTool.RemoteName() != "search" {
		t.Fatalf("wrapped tool origin = %q/%q", wrappedTool.Server(), wrappedTool.RemoteName())
	}
	assertJSONEqual(t, wrapped.InputSchema(), json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`))
	if !wrapped.ReadOnly(nil) || !wrapped.ConcurrencySafe(nil) {
		t.Fatal("tool should be read-only and concurrency safe")
	}

	ctx := contextWithTimeout(t)
	var progressText string
	result, err := wrapped.Run(ctx, json.RawMessage(`{"q":"hi"}`), func(p tool.Progress) { progressText = p.Text })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Content != "ok from alpha" {
		t.Fatalf("content = %q", result.Content)
	}
	if result.IsError {
		t.Fatal("IsError = true")
	}
	if result.Meta["mcpServer"] != "alpha" || result.Meta["mcpTool"] != "search" {
		t.Fatalf("Meta = %v", result.Meta)
	}
	if progressText == "" {
		t.Fatal("progress not reported")
	}

	manager.Tools()
	if got := dialer.callCount(); got != 1 {
		t.Fatalf("dial called %d times, want 1", got)
	}

	statuses, err := manager.ListServers(ctx)
	if err != nil {
		t.Fatalf("ListServers: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("statuses = %+v", statuses)
	}
	status := statuses[0]
	if status.Name != "alpha" || status.State != StateReady || status.ToolCount != 1 {
		t.Fatalf("status = %+v", status)
	}
	if status.Command != "alpha-bin" || len(status.Args) != 1 || status.Args[0] != "--flag" {
		t.Fatalf("status command = %+v", status)
	}
	if len(status.EnvKeys) != 1 || status.EnvKeys[0] != "TOKEN" {
		t.Fatalf("EnvKeys = %v", status.EnvKeys)
	}
	if status.ServerName != "alpha-server" || status.ServerVersion != "9.9.9" {
		t.Fatalf("server info = %+v", status)
	}

	if err := manager.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if tools := manager.Tools(); len(tools) != 0 {
		t.Fatalf("Tools after Close = %d, want 0", len(tools))
	}
	if _, err := wrapped.Run(ctx, nil, nil); !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("Run after Close error = %v, want ErrManagerClosed", err)
	}
}

// TestManagerReload 覆盖重读配置与重连。
func TestManagerReload(t *testing.T) {
	dialer := newFakeDialer(t)
	path := writeMCPConfig(t, map[string]ServerConfig{
		"alpha": {Command: "alpha-bin"},
	})
	manager := NewManager(path, dialer.dial)
	defer manager.Close()

	if tools := manager.Tools(); len(tools) != 1 {
		t.Fatalf("initial Tools = %d, want 1", len(tools))
	}

	writeMCPConfigAt(t, path, map[string]ServerConfig{
		"alpha": {Command: "alpha-bin"},
		"beta":  {Command: "beta-bin"},
	})
	if err := manager.Reload(contextWithTimeout(t)); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	tools := manager.Tools()
	if len(tools) != 2 {
		t.Fatalf("Tools after Reload = %d, want 2", len(tools))
	}
	if tools[0].Name() != "mcp__alpha__search" || tools[1].Name() != "mcp__beta__search" {
		t.Fatalf("tool names = %q, %q", tools[0].Name(), tools[1].Name())
	}
	if got := dialer.callCount(); got != 3 {
		t.Fatalf("dial calls = %d, want 3", got)
	}
}

// TestManagerServerFailure 覆盖单个服务器失败不影响其它服务器，且失败不重复重试。
func TestManagerServerFailure(t *testing.T) {
	dialer := newFakeDialer(t)
	dialer.fail["broken"] = errors.New("spawn failed")
	path := writeMCPConfig(t, map[string]ServerConfig{
		"broken": {Command: "broken-bin"},
		"good":   {Command: "good-bin"},
	})
	manager := NewManager(path, dialer.dial)
	defer manager.Close()

	tools := manager.Tools()
	if len(tools) != 1 || tools[0].Name() != "mcp__good__search" {
		t.Fatalf("tools = %v", tools)
	}
	statuses, err := manager.ListServers(contextWithTimeout(t))
	if err != nil {
		t.Fatalf("ListServers: %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("statuses = %+v", statuses)
	}
	if statuses[0].Name != "broken" || statuses[0].State != StateError || statuses[0].Error == "" {
		t.Fatalf("broken status = %+v", statuses[0])
	}
	if statuses[1].Name != "good" || statuses[1].State != StateReady {
		t.Fatalf("good status = %+v", statuses[1])
	}

	manager.Tools()
	if got := dialer.callCount(); got != 2 {
		t.Fatalf("dial calls = %d, want 2 (broken must not retry)", got)
	}
	if _, err := manager.callTool(contextWithTimeout(t), "broken", "search", nil); err == nil {
		t.Fatal("callTool on broken server succeeded")
	}
}

// TestManagerUnknownServer 覆盖调用未配置的服务器。
func TestManagerUnknownServer(t *testing.T) {
	dialer := newFakeDialer(t)
	path := writeMCPConfig(t, map[string]ServerConfig{"alpha": {Command: "alpha-bin"}})
	manager := NewManager(path, dialer.dial)
	defer manager.Close()
	if _, err := manager.ListServers(contextWithTimeout(t)); err != nil {
		t.Fatalf("ListServers: %v", err)
	}
	if _, err := manager.callTool(contextWithTimeout(t), "nope", "x", nil); !errors.Is(err, ErrUnknownServer) {
		t.Fatalf("error = %v, want ErrUnknownServer", err)
	}
}

// TestManagerConfigError 覆盖配置文件损坏。
func TestManagerConfigError(t *testing.T) {
	path := t.TempDir() + "/mcp.json"
	if err := writeFile(path, []byte("{")); err != nil {
		t.Fatalf("write config: %v", err)
	}
	manager := NewManager(path, newFakeDialer(t).dial)
	defer manager.Close()

	if _, err := manager.RefreshTools(contextWithTimeout(t)); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("RefreshTools error = %v, want ErrConfigInvalid", err)
	}
	if tools := manager.Tools(); len(tools) != 0 {
		t.Fatalf("Tools = %d, want 0", len(tools))
	}
}

// TestManagerListChanged 覆盖服务器通知后按需刷新工具。
func TestManagerListChanged(t *testing.T) {
	var mu sync.Mutex
	listCalls := 0
	dialer := newFakeDialer(t)
	dialer.handler = func(server string, p *fakePeer, req fakeRequest) {
		switch req.method {
		case "initialize":
			p.respond(req.id, map[string]any{"capabilities": map[string]any{"tools": map[string]any{"listChanged": true}}})
		case "tools/list":
			mu.Lock()
			listCalls++
			call := listCalls
			mu.Unlock()
			count := 1
			if call > 1 {
				count = 2
			}
			entries := make([]map[string]any, 0, count)
			for i := 0; i < count; i++ {
				entries = append(entries, map[string]any{
					"name":        fmt.Sprintf("tool%d", i),
					"inputSchema": map[string]any{"type": "object"},
				})
			}
			p.respond(req.id, map[string]any{"tools": entries})
		}
	}
	path := writeMCPConfig(t, map[string]ServerConfig{"alpha": {Command: "alpha-bin"}})
	manager := NewManager(path, dialer.dial)
	defer manager.Close()

	if tools := manager.Tools(); len(tools) != 1 {
		t.Fatalf("initial Tools = %d, want 1", len(tools))
	}
	dialer.peer("alpha").notify("notifications/tools/list_changed", map[string]any{})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if tools := manager.Tools(); len(tools) == 2 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("tools did not refresh after list_changed")
}

// assertJSONEqual 比较两段 JSON 的语义是否一致。
func assertJSONEqual(t *testing.T, got, want json.RawMessage) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode got %s: %v", got, err)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("decode want %s: %v", want, err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON mismatch:\n got: %s\nwant: %s", got, want)
	}
}
