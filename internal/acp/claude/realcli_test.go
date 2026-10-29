package claude

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Baike12/b-desktop/internal/acp"
)

// realSessionClient 自动允许权限请求并记录请求。
type realSessionClient struct {
	asked []acp.PermissionRequest
}

func (c *realSessionClient) RequestPermission(ctx context.Context, req acp.PermissionRequest) (acp.PermissionOutcome, error) {
	c.asked = append(c.asked, req)
	return acp.PermissionOutcome{OptionID: "allow-once"}, nil
}
func (c *realSessionClient) ReadTextFile(ctx context.Context, path string) (string, error) {
	return "", nil
}
func (c *realSessionClient) WriteTextFile(ctx context.Context, path, content string) error {
	return nil
}

// TestRealCLIHandshake 用真实 claude CLI 验证握手与 init 帧；
// BDESKTOP_ACP_E2E=1 时才运行（需要本机安装并登录 claude）。
func TestRealCLIHandshake(t *testing.T) {
	if os.Getenv("BDESKTOP_ACP_E2E") != "1" {
		t.Skip("需要 BDESKTOP_ACP_E2E=1")
	}
	exe, err := ResolveCLI(os.Getenv)
	if err != nil {
		t.Fatalf("ResolveCLI: %v", err)
	}
	t.Logf("claude cli: %s", exe)
	backend := NewBackend()
	sink := &recordingSink{}
	client := &realSessionClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	session, err := backend.NewSession(ctx, acp.SessionParams{CWD: t.TempDir()}, sink.handle, client)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	keyer, ok := session.(interface{ SessionKey() string })
	if !ok || keyer.SessionKey() == "" {
		t.Fatalf("session key missing: %v", ok)
	}
	t.Logf("claude session id: %s", keyer.SessionKey())
	var sawModel, sawCommands bool
	for _, update := range sink.updates {
		if update.Kind == acp.UpdateStatus && update.Status != nil && update.Status.Model != "" {
			sawModel = true
			t.Logf("model: %s", update.Status.Model)
		}
		if update.Kind == acp.UpdateAvailableCommands {
			sawCommands = true
			for _, command := range update.Commands {
				t.Logf("command: %q desc=%q hint=%q", command.Name, command.Description, command.Hint)
			}
		}
	}
	if !sawModel || !sawCommands {
		t.Fatalf("handshake updates incomplete: model=%v commands=%v", sawModel, sawCommands)
	}
}

// TestRealCLIPrompt 用真实 claude CLI 跑一轮最小对话，验证流式输出与
// result 结算；消耗一次极小 API 调用。
func TestRealCLIPrompt(t *testing.T) {
	if os.Getenv("BDESKTOP_ACP_E2E_FULL") != "1" {
		t.Skip("需要 BDESKTOP_ACP_E2E_FULL=1")
	}
	backend := NewBackend()
	sink := &recordingSink{}
	client := &realSessionClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	session, err := backend.NewSession(ctx, acp.SessionParams{CWD: t.TempDir()}, sink.handle, client)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	result, err := session.Prompt(ctx, []acp.ContentBlock{{Type: "text", Text: "只回复两个字符：OK"}})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if result.StopReason != acp.StopEndTurn {
		t.Fatalf("stop reason = %q err=%q", result.StopReason, result.Error)
	}
	if result.Usage == nil || result.Usage.OutputTokens == 0 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	var text strings.Builder
	for _, update := range sink.updates {
		if update.Kind == acp.UpdateAgentMessageChunk && update.Content != nil {
			text.WriteString(update.Content.Text)
		}
	}
	t.Logf("reply=%q usage=%+v", strings.TrimSpace(text.String()), result.Usage)
	if !strings.Contains(strings.ToUpper(text.String()), "OK") {
		t.Fatalf("reply missing OK: %q", text.String())
	}
}

// TestRealCLIToolUse 用真实 claude CLI 跑一轮工具调用，验证 can_use_tool
// 权限链路与工具结果回灌；BDESKTOP_ACP_E2E_TOOL=1 时才运行。
func TestRealCLIToolUse(t *testing.T) {
	if os.Getenv("BDESKTOP_ACP_E2E_TOOL") != "1" {
		t.Skip("需要 BDESKTOP_ACP_E2E_TOOL=1")
	}
	backend := NewBackend()
	sink := &recordingSink{}
	client := &realSessionClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	session, err := backend.NewSession(ctx, acp.SessionParams{CWD: t.TempDir()}, sink.handle, client)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	result, err := session.Prompt(ctx, []acp.ContentBlock{{Type: "text", Text: "用 Bash 工具运行 `echo acp_tool_ok`，然后原样告诉我输出。必须使用 Bash 工具。"}})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if result.StopReason != acp.StopEndTurn || result.Error != "" {
		t.Fatalf("result = %+v", result)
	}
	// can_use_tool 是否触发取决于用户设置（已 allow 的工具不再询问）；
	// 这里只记录触发情况，强制断言在 fake CLI 测试里覆盖。
	if len(client.asked) > 0 {
		t.Logf("permission asked: tool=%s title=%q", client.asked[0].ToolName, client.asked[0].ToolCall.Title)
	} else {
		t.Logf("permission not asked (tool pre-allowed by settings)")
	}

	var toolCall, toolResult bool
	var preview string
	for _, update := range sink.updates {
		if update.Kind == acp.UpdateToolCall && update.ToolCall != nil && update.ToolCall.ToolName == "Bash" {
			toolCall = true
		}
		if update.Kind == acp.UpdateToolCallUpdate && update.ToolCallUpdate != nil && update.ToolCallUpdate.Status == acp.StatusCompleted {
			toolResult = true
			preview = localPreview(update.ToolCallUpdate.RawOutput)
		}
	}
	if !toolCall || !toolResult {
		t.Fatalf("tool call=%v result=%v", toolCall, toolResult)
	}
	t.Logf("tool output preview: %q", preview)
	if !strings.Contains(preview, "acp_tool_ok") {
		t.Fatalf("tool output missing marker: %q", preview)
	}
}

// localPreview 从 RawOutput（JSON 字符串）提取预览文本。
func localPreview(raw []byte) string {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return string(raw)
}
