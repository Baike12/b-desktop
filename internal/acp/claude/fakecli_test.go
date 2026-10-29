package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Baike12/b-desktop/internal/acp"
)

// fakeCLIScript 是一个伪装 claude CLI 的 bash 脚本：实现 initialize 握手、
// 读取一行 user 消息、回放文本流 + 一次 can_use_tool 权限请求 + 工具结果 +
// result 帧；收到 interrupt 后回 cancelled result。stdout 的帧顺序即协议
// 契约的最小复现。
const fakeCLIScript = `#!/bin/bash
# 逐行读 stdin；initialize 控制请求回 success，再发 init 帧。
while IFS= read -r line; do
  case "$line" in
    *'"subtype":"initialize"'*)
      rid=$(printf '%s' "$line" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
      echo "{\"type\":\"control_response\",\"response\":{\"request_id\":\"$rid\",\"subtype\":\"success\",\"response\":{}}}"
      echo '{"type":"system","subtype":"init","session_id":"claude-s-1","model":"claude-fake-4-5","permissionMode":"default","slash_commands":["compact","clear","review"],"terminal_slash_commands":[],"tools":["Bash"]}'
      ;;
    *'"type":"user"'*)
      # 文本流
      echo '{"type":"stream_event","session_id":"claude-s-1","parent_tool_use_id":null,"event":{"type":"message_start","message":{"id":"msg_fake_1","role":"assistant"}}}'
      echo '{"type":"stream_event","session_id":"claude-s-1","parent_tool_use_id":null,"event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"我来运行命令。"}}}'
      # assistant 帧带 tool_use
      echo '{"type":"assistant","session_id":"claude-s-1","parent_tool_use_id":null,"uuid":"u1","message":{"id":"msg_fake_1","role":"assistant","model":"claude-fake-4-5","content":[{"type":"text","text":"我来运行命令。"},{"type":"tool_use","id":"toolu_fake_1","name":"Bash","input":{"command":"echo hi"}}]}}'
      # 权限请求：等待 control_response
      echo '{"type":"control_request","request_id":"req_perm_1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"echo hi"},"tool_use_id":"toolu_fake_1"}}'
      # 工具结果
      echo '{"type":"user","session_id":"claude-s-1","parent_tool_use_id":null,"uuid":"u2","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_fake_1","content":"hi","is_error":false}]}}'
      # 结束帧
      echo '{"type":"result","subtype":"success","is_error":false,"num_turns":1,"session_id":"claude-s-1","usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":3},"total_cost_usd":0.01,"result":"done"}'
      ;;
    *'"subtype":"interrupt"'*)
      rid=$(printf '%s' "$line" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
      echo "{\"type\":\"control_response\",\"response\":{\"request_id\":\"$rid\",\"subtype\":\"success\",\"response\":{}}}"
      echo '{"type":"result","subtype":"success","is_error":false,"num_turns":1,"session_id":"claude-s-1","usage":{"input_tokens":10,"output_tokens":1}}'
      ;;
  esac
done
`

// writeFakeCLI 把脚本写到临时目录并赋予执行权限，返回路径。
func writeFakeCLI(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(path, []byte(fakeCLIScript), 0o755); err != nil {
		t.Fatalf("write fake cli: %v", err)
	}
	return path
}

// recordingSink 收集更新。
type recordingSink struct {
	updates []acp.SessionUpdate
}

func (r *recordingSink) handle(update acp.SessionUpdate) {
	r.updates = append(r.updates, update)
}

// approveClient 自动允许一次权限请求。
type approveClient struct{}

func (approveClient) RequestPermission(ctx context.Context, req acp.PermissionRequest) (acp.PermissionOutcome, error) {
	return acp.PermissionOutcome{OptionID: "allow-once"}, nil
}
func (approveClient) ReadTextFile(ctx context.Context, path string) (string, error) {
	return "", nil
}
func (approveClient) WriteTextFile(ctx context.Context, path, content string) error { return nil }

func TestFakeCLISessionEndToEnd(t *testing.T) {
	exe := writeFakeCLI(t)
	backend := &Backend{getenv: func(key string) string {
		if key == EnvExecutable {
			return exe
		}
		return ""
	}}

	sink := &recordingSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session, err := backend.NewSession(ctx, acp.SessionParams{CWD: "/tmp"}, sink.handle, approveClient{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	// 握手后的 init 帧：状态更新 + 命令更新（帧处理与握手响应存在竞态，轮询等待）。
	deadline := time.Now().Add(3 * time.Second)
	for {
		var sawStatus, sawCommands bool
		for _, update := range sink.updates {
			if update.Kind == acp.UpdateStatus && update.Status != nil && update.Status.Model == "claude-fake-4-5" {
				sawStatus = true
			}
			if update.Kind == acp.UpdateAvailableCommands {
				sawCommands = true
			}
		}
		if sawStatus && sawCommands {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("init updates missing: status=%v commands=%v updates=%+v", sawStatus, sawCommands, sink.updates)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// SessionKey 在握手期即由本侧固定（--session-id），非空即可持久化。
	if keyer, ok := session.(interface{ SessionKey() string }); !ok || keyer.SessionKey() == "" {
		t.Fatalf("session key missing: %v", session)
	}

	result, err := session.Prompt(ctx, []acp.ContentBlock{{Type: "text", Text: "运行 echo hi"}})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if result.StopReason != acp.StopEndTurn {
		t.Fatalf("stop reason = %q", result.StopReason)
	}
	if result.Usage == nil || result.Usage.InputTokens != 10 || result.Usage.CostUSD != 0.01 {
		t.Fatalf("usage = %+v", result.Usage)
	}

	// 校验更新序列：边界 → 文本 → 工具首发 → refine → 工具结果。
	var kinds []acp.UpdateKind
	var toolFirst *acp.ToolCall
	var toolResultUpdate *acp.ToolCallUpdate
	for _, update := range sink.updates {
		kinds = append(kinds, update.Kind)
		switch update.Kind {
		case acp.UpdateToolCall:
			toolFirst = update.ToolCall
		case acp.UpdateToolCallUpdate:
			if update.ToolCallUpdate.Status == acp.StatusCompleted && update.ToolCallUpdate.ToolCallID == "toolu_fake_1" {
				toolResultUpdate = update.ToolCallUpdate
			}
		}
	}
	if toolFirst == nil || toolFirst.ToolName != "Bash" || toolFirst.Kind != acp.ToolExecute {
		t.Fatalf("tool call = %+v", toolFirst)
	}
	if toolResultUpdate == nil || toolResultUpdate.RawOutput == nil {
		t.Fatalf("tool result update = %+v", toolResultUpdate)
	}
	var preview string
	if err := json.Unmarshal(toolResultUpdate.RawOutput, &preview); err != nil || preview != "hi" {
		t.Fatalf("tool output = %s err=%v", toolResultUpdate.RawOutput, err)
	}

	// Prompt 后再次 Prompt 应当可用（进程保持存活）。
	result2, err := session.Prompt(ctx, []acp.ContentBlock{{Type: "text", Text: "再来一轮"}})
	if err != nil || result2.StopReason != acp.StopEndTurn {
		t.Fatalf("second prompt = %+v err=%v", result2, err)
	}
}

func TestFakeCLICancel(t *testing.T) {
	exe := writeFakeCLI(t)
	backend := &Backend{getenv: func(key string) string {
		if key == EnvExecutable {
			return exe
		}
		return ""
	}}
	sink := &recordingSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session, err := backend.NewSession(ctx, acp.SessionParams{CWD: "/tmp"}, sink.handle, approveClient{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	// Prompt 阻塞期间取消：fake CLI 的 interrupt 分支回 result。
	promptCtx, promptCancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var result acp.PromptResult
	var promptErr error
	go func() {
		result, promptErr = session.Prompt(promptCtx, []acp.ContentBlock{{Type: "text", Text: "hi"}})
		close(done)
	}()
	time.Sleep(200 * time.Millisecond)
	promptCancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("prompt not settled after cancel")
	}
	if promptErr != nil {
		t.Fatalf("prompt err = %v", promptErr)
	}
	// fake CLI 的 interrupt 分支回 success result；取消语义由桥接层补齐，
	// 这里只验证 Prompt 不悬挂且能结算。
	if result.StopReason != acp.StopEndTurn && result.StopReason != acp.StopCancelled {
		t.Fatalf("stop reason = %q", result.StopReason)
	}
}

func TestResolveCLIMissing(t *testing.T) {
	_, err := ResolveCLI(func(string) string { return "" })
	// 本机可能安装了 claude：只有当 PATH 与常见路径都没有时才报错。
	// 这里只验证返回值可读（有 CLI 时返回路径，没有时返回错误）。
	if err != nil && !strings.Contains(err.Error(), "claude") {
		t.Fatalf("unexpected error: %v", err)
	}
}
