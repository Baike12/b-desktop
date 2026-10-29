package claude

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Baike12/b-desktop/internal/acp"
)

// 握手与等待窗口。
const (
	initWaitTimeout = 60 * time.Second
	// interruptWait 是取消后等 CLI 回 result 的上限。
	interruptWait = 15 * time.Second
)

// 会话级错误哨兵。
var (
	// ErrSessionClosed 表示会话已关闭。
	ErrSessionClosed = errors.New("claude: session closed")
	// ErrTurnRunning 表示同一会话已有一轮进行中。
	ErrTurnRunning = errors.New("claude: turn already running")
	// ErrResumeNotFound 表示 --resume 指定的会话不存在（调用方应去掉
	// resume 键重开新会话）。
	ErrResumeNotFound = errors.New("claude: conversation not found")
)

// Backend 是内置的 Claude Code 后端。
type Backend struct {
	getenv func(string) string
}

// NewBackend 创建 claude 后端。
func NewBackend() *Backend {
	return &Backend{getenv: os.Getenv}
}

// Info 返回后端描述。
func (b *Backend) Info() acp.AgentInfo {
	return acp.AgentInfo{
		ID:          BackendID,
		Name:        "Claude Code",
		Description: "通过内置 ACP 桥接驱动 claude CLI，与 zed 的 claude-agent-acp 行为对齐",
	}
}

// NewSession 启动 claude CLI、完成控制握手并等待 init 帧。
func (b *Backend) NewSession(ctx context.Context, params acp.SessionParams, sink acp.UpdateSink, client acp.Client) (acp.Session, error) {
	exe, err := ResolveCLI(b.getenv)
	if err != nil {
		return nil, err
	}

	// 会话键由本侧生成并经 --session-id 传给 CLI：NewSession 返回时即可
	// 持久化，不依赖 init 帧（新版 CLI 不在握手期发它）。
	key := params.ResumeKey
	fresh := false
	if key == "" {
		generated, err := newSessionUUID()
		if err != nil {
			return nil, fmt.Errorf("claude: generate session id: %w", err)
		}
		key = generated
		fresh = true
	}

	sessionCtx, cancel := context.WithCancel(context.Background())
	s := &claudeSession{
		sink:       sink,
		client:     client,
		cwd:        params.CWD,
		translator: newTranslator(params.CWD),
		ctx:        sessionCtx,
		cancel:     cancel,
		claudeIDCh: make(chan string, 1),
	}
	s.claudeID = key
	s.proc, err = startProcess(sessionCtx, exe, s.buildArgs(params, fresh, key), buildEnv(b.extraEnv()), params.CWD, s.onFrame, s.onControl, s.onExit)
	if err != nil {
		cancel()
		return nil, err
	}

	// initialize 控制握手：必须在任何用户消息之前完成；响应携带命令清单、
	// 模型目录与当前权限模式。
	initCtx, initCancel := context.WithTimeout(ctx, initWaitTimeout)
	defer initCancel()
	resp, err := s.proc.request(initCtx, controlPayload{Subtype: "initialize", ForwardSubagentText: true})
	if err != nil {
		_ = s.Close()
		// --resume 指向已不存在的会话时让调用方重开新会话。
		if strings.Contains(strings.ToLower(err.Error()), "no conversation found") {
			return nil, fmt.Errorf("claude: initialize: %w", acp.ErrResumeNotFound)
		}
		return nil, fmt.Errorf("claude: initialize: %w", err)
	}
	s.announceInitState(resp)
	return s, nil
}

// newSessionUUID 生成小写 UUID v4。
func newSessionUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// initStatePayload 是 initialize 控制响应的载荷（字段宽松解析）。
type initStatePayload struct {
	Commands []struct {
		Name         string `json:"name"`
		Description  string `json:"description"`
		ArgumentHint string `json:"argumentHint"`
	} `json:"commands"`
	Models []struct {
		Value         string `json:"value"`
		ResolvedModel string `json:"resolvedModel"`
	} `json:"models"`
	CurrentPermissionMode string `json:"current_permission_mode"`
}

// announceInitState 把握手响应里的状态推给宿主。
func (s *claudeSession) announceInitState(raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var state initStatePayload
	if err := json.Unmarshal(raw, &state); err != nil {
		return
	}
	model := ""
	for _, entry := range state.Models {
		if entry.Value == "default" && entry.ResolvedModel != "" {
			model = entry.ResolvedModel
			break
		}
	}
	if model != "" || state.CurrentPermissionMode != "" {
		s.emit(acp.SessionUpdate{
			Kind:   acp.UpdateStatus,
			Status: &acp.SessionStatus{Model: model, Mode: state.CurrentPermissionMode},
		})
	}
	if len(state.Commands) > 0 {
		commands := make([]acp.AvailableCommand, 0, len(state.Commands))
		for _, command := range state.Commands {
			name := strings.TrimSpace(command.Name)
			if name == "" || UNSUPPORTED_SLASH_COMMANDS[name] {
				continue
			}
			commands = append(commands, acp.AvailableCommand{Name: name, Description: command.Description, Hint: command.ArgumentHint})
		}
		if len(commands) > 0 {
			s.emit(acp.SessionUpdate{Kind: acp.UpdateAvailableCommands, Commands: commands})
		}
	}
}

// extraEnv 附加给子进程的环境变量。
func (b *Backend) extraEnv() map[string]string {
	return map[string]string{
		// 让 CLI 主动上报会话状态变化（idle/running），便于宿主跟踪轮次。
		"CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS": "1",
	}
}

// buildArgs 组装 claude CLI 参数，对齐 claude-agent-sdk-python 的
// _build_command（流式输入输出、部分消息、stdio 权限回调）。
func (s *claudeSession) buildArgs(params acp.SessionParams, fresh bool, sessionKey string) []string {
	args := []string{
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--input-format", "stream-json",
		"--setting-sources=user,project,local",
		"--permission-prompt-tool", "stdio",
	}
	if params.PermissionMode != "" {
		args = append(args, "--permission-mode", params.PermissionMode)
	}
	if fresh {
		// 固定会话键：宿主在握手后即可持久化，重启后据此 resume。
		args = append(args, "--session-id="+sessionKey)
	} else {
		args = append(args, "--resume="+sessionKey)
	}
	if servers := mcpConfigJSON(params.MCPServers); servers != "" {
		args = append(args, "--mcp-config", servers)
	}
	return args
}

// mcpConfigJSON 把 MCP 服务器配置编码成 --mcp-config 的 JSON；空列表返回空串。
func mcpConfigJSON(servers []acp.MCPServer) string {
	if len(servers) == 0 {
		return ""
	}
	config := map[string]any{"mcpServers": map[string]any{}}
	named := config["mcpServers"].(map[string]any)
	for _, server := range servers {
		switch server.Type {
		case "http", "sse":
			entry := map[string]any{"type": server.Type, "url": server.URL}
			if headers := namedValues(server.Headers); len(headers) > 0 {
				entry["headers"] = headers
			}
			named[server.Name] = entry
		default:
			entry := map[string]any{"type": "stdio", "command": server.Command}
			if len(server.Args) > 0 {
				entry["args"] = server.Args
			}
			if env := namedValues(server.Env); len(env) > 0 {
				entry["env"] = env
			}
			named[server.Name] = entry
		}
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// namedValues 把 map 转成 {name,value} 列表，按名字排序保证稳定。
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

// turnState 是一轮进行中的提示。
type turnState struct {
	ctx      context.Context
	cancel   context.CancelFunc
	resultCh chan resultFrame
}

// claudeSession 是 claude CLI 的一个会话。
type claudeSession struct {
	sink   acp.UpdateSink
	client acp.Client
	cwd    string

	proc       *process
	translator *translator
	ctx        context.Context
	cancel     context.CancelFunc

	mu       sync.Mutex
	turn     *turnState
	closed   bool
	claudeID string
	// modes 由外部维护（b-desktop 权限模式与 claude 模式同名）。
	modes acp.SessionModes

	claudeIDCh chan string
	closeOnce  sync.Once
	// onKeyChange 在 claude 会话键变化时回调（conversation_reset）。
	onKeyChange func(string)
}

// SessionKey 返回 claude 会话 ID，供宿主持久化以便下次 resume。
func (s *claudeSession) SessionKey() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claudeID
}

// Modes 返回会话模式状态。
func (s *claudeSession) Modes() acp.SessionModes {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.modes
}

// Prompt 提交一轮用户输入并等待 result 帧。
func (s *claudeSession) Prompt(ctx context.Context, blocks []acp.ContentBlock) (acp.PromptResult, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return acp.PromptResult{}, ErrSessionClosed
	}
	if s.turn != nil {
		s.mu.Unlock()
		return acp.PromptResult{}, ErrTurnRunning
	}
	turnCtx, turnCancel := context.WithCancel(ctx)
	turn := &turnState{ctx: turnCtx, cancel: turnCancel, resultCh: make(chan resultFrame, 1)}
	s.turn = turn
	s.mu.Unlock()

	defer func() {
		turnCancel()
		s.mu.Lock()
		s.turn = nil
		s.mu.Unlock()
	}()

	message, err := promptUserMessage(blocks)
	if err != nil {
		return acp.PromptResult{}, err
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		return acp.PromptResult{}, fmt.Errorf("claude: encode user message: %w", err)
	}
	if err := s.proc.writeLine(encoded); err != nil {
		return acp.PromptResult{}, fmt.Errorf("claude: send user message: %w", err)
	}

	select {
	case result := <-turn.resultCh:
		return mapResult(result), nil
	case <-turnCtx.Done():
		// 用户取消：答复挂起的 can_use_tool（deny+interrupt 由权限回调负责，
		// 因为它监听同一个 turnCtx）并发送 interrupt，然后等 CLI 回 result。
		s.proc.interrupt(context.Background())
		select {
		case result := <-turn.resultCh:
			return mapResult(result), nil
		case <-time.After(interruptWait):
			return acp.PromptResult{StopReason: acp.StopCancelled}, nil
		}
	case <-s.ctx.Done():
		return acp.PromptResult{}, ErrSessionClosed
	}
}

// promptUserMessage 构造 stdin 的 user 消息帧。
func promptUserMessage(blocks []acp.ContentBlock) (userMessageIn, error) {
	content := make([]apiContentBlock, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text == "" {
				continue
			}
			content = append(content, apiContentBlock{Type: "text", Text: block.Text})
		case "image":
			if block.Data == "" {
				continue
			}
			content = append(content, apiContentBlock{
				Type:   "image",
				Source: &apiImageSource{Type: "base64", MimeType: block.MimeType, Data: block.Data},
			})
		default:
			// resource_link / resource 降级为文本链接，与适配器的 formatUriAsLink 一致。
			if block.URI != "" {
				name := block.Name
				if name == "" {
					name = block.URI
				}
				content = append(content, apiContentBlock{Type: "text", Text: fmt.Sprintf("[%s](%s)", name, block.URI)})
			}
		}
	}
	if len(content) == 0 {
		return userMessageIn{}, errors.New("claude: 空提示")
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return userMessageIn{}, err
	}
	return userMessageIn{
		Type:            "user",
		ParentToolUseID: nil,
		Message:         apiMessage{Role: "user", Content: raw},
	}, nil
}

// mapResult 把 result 帧映射成 ACP 结算结果。
func mapResult(result resultFrame) acp.PromptResult {
	out := acp.PromptResult{}
	if result.Usage != nil {
		out.Usage = &acp.UsageSummary{
			InputTokens:  result.Usage.InputTokens,
			OutputTokens: result.Usage.OutputTokens,
			CachedRead:   result.Usage.CacheReadInputTokens,
			CachedWrite:  result.Usage.CacheCreationInputTokens,
			CostUSD:      result.TotalCostUSD,
		}
	}
	switch {
	case result.IsError:
		out.Error = errorsText(result)
		out.StopReason = acp.StopEndTurn
	case result.Subtype == "success" && result.StopReason != nil && *result.StopReason == "max_tokens":
		out.StopReason = acp.StopMaxTokens
	case result.Subtype == "refusal":
		out.StopReason = acp.StopRefusal
	case strings.HasPrefix(result.Subtype, "error_max"):
		out.StopReason = acp.StopMaxTurnReqs
	default:
		out.StopReason = acp.StopEndTurn
	}
	return out
}

// errorsText 提取错误 result 的可展示文本。
func errorsText(result resultFrame) string {
	if len(result.Errors) > 0 {
		return strings.Join(result.Errors, "\n")
	}
	if result.Result != "" {
		return result.Result
	}
	return "工具执行失败（" + result.Subtype + "）"
}

// Cancel 取消进行中的一轮：interrupt 控制请求；result 帧随后到达。
func (s *claudeSession) Cancel() {
	s.proc.interrupt(context.Background())
}

// SetMode 通过控制请求切换权限模式。
func (s *claudeSession) SetMode(ctx context.Context, modeID string) error {
	s.mu.Lock()
	s.modes.Current = modeID
	s.mu.Unlock()
	if err := s.proc.setPermissionMode(ctx, modeID); err != nil {
		return err
	}
	s.emit(acp.SessionUpdate{Kind: acp.UpdateCurrentMode, CurrentModeID: modeID})
	return nil
}

// Close 关闭会话与子进程，幂等。
func (s *claudeSession) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		s.cancel()
		if s.proc != nil {
			s.proc.close()
		}
	})
	return nil
}

// emit 把更新推给宿主。
func (s *claudeSession) emit(update acp.SessionUpdate) {
	if s.sink == nil {
		return
	}
	s.sink(update)
}

// onExit 是进程退出回调：终结进行中的一轮。
func (s *claudeSession) onExit(err error) {
	s.mu.Lock()
	turn := s.turn
	s.mu.Unlock()
	_ = err
	if turn != nil {
		// resultCh 永不再有值；Prompt 的 s.ctx.Done 分支负责报错退出。
		s.cancel()
	}
}

// onControl 处理 can_use_tool：转成宿主审批请求，等待答复。
func (s *claudeSession) onControl(ctx context.Context, req controlRequestIn) (json.RawMessage, error) {
	if req.Subtype() != "can_use_tool" {
		return nil, fmt.Errorf("claude: 不支持的控制请求 %q", req.Subtype())
	}
	request := req.canUseTool()

	s.mu.Lock()
	turn := s.turn
	s.mu.Unlock()
	if turn == nil {
		encoded, err := json.Marshal(canUseToolDecision{Behavior: "deny", Message: "没有进行中的轮次"})
		return encoded, err
	}

	// 工具调用卡片可能尚未随流发出（无流式模式时），先补发。
	if request.ToolUseID != "" && !s.translator.seenTools[request.ToolUseID] {
		s.translator.seenTools[request.ToolUseID] = true
		view := toolViewFor(request.ToolName, request.Input, s.cwd)
		s.emit(s.translator.toolUpdate(acp.ToolCall{
			ToolCallID: request.ToolUseID,
			Title:      view.Title,
			ToolName:   request.ToolName,
			Kind:       view.Kind,
			Status:     acp.StatusPending,
			Locations:  view.Locations,
			RawInput:   request.Input,
		}))
	}

	permissionReq := acp.PermissionRequest{
		ToolCall: acp.ToolCallUpdate{
			ToolCallID: request.ToolUseID,
			Title:      permissionTitle(request),
			ToolName:   request.ToolName,
			Kind:       toolKindFor(request.ToolName),
			Status:     acp.StatusPending,
			RawInput:   request.Input,
		},
		ToolName: request.ToolName,
		Reason:   request.DecisionReason,
		Options:  permissionOptionsFor(request),
	}
	outcome, err := s.client.RequestPermission(turn.ctx, permissionReq)
	if err != nil || outcome.Cancelled {
		// 取消也必须应答 CLI，否则它永远等待：deny + interrupt。
		encoded, marshalErr := json.Marshal(canUseToolDecision{Behavior: "deny", Message: "用户取消了本轮", Interrupt: true})
		if marshalErr != nil {
			return nil, marshalErr
		}
		return encoded, nil
	}
	decision, err := decisionFor(request, outcome.OptionID)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(decision)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// permissionTitle 生成权限弹窗的标题：优先 CLI 给的标题，其次工具视图标题。
func permissionTitle(request canUseToolRequest) string {
	if request.Title != "" {
		return request.Title
	}
	if request.DisplayName != "" {
		return request.DisplayName
	}
	view := toolViewFor(request.ToolName, request.Input, "")
	return view.Title
}

// onFrame 处理 stdout 帧：握手期捕获 init，轮次中翻译成宿主更新。
func (s *claudeSession) onFrame(envelope outEnvelope) {
	switch envelope.Type {
	case "system":
		var frame systemFrame
		if err := json.Unmarshal(envelope.Raw, &frame); err != nil {
			return
		}
		s.handleSystem(frame)
	case "stream_event":
		var frame streamEventFrame
		if err := json.Unmarshal(envelope.Raw, &frame); err != nil {
			return
		}
		for _, update := range s.translator.handleStreamEvent(frame) {
			s.emit(update)
		}
	case "assistant":
		var frame assistantFrame
		if err := json.Unmarshal(envelope.Raw, &frame); err != nil {
			return
		}
		for _, update := range s.translator.handleAssistant(frame) {
			s.emit(update)
		}
	case "user":
		var frame userFrame
		if err := json.Unmarshal(envelope.Raw, &frame); err != nil {
			return
		}
		for _, update := range s.translator.handleUser(frame) {
			s.emit(update)
		}
	case "result":
		var frame resultFrame
		if err := json.Unmarshal(envelope.Raw, &frame); err != nil {
			return
		}
		s.mu.Lock()
		turn := s.turn
		s.mu.Unlock()
		if turn != nil {
			select {
			case turn.resultCh <- frame:
			default:
			}
		}
	}
}

// handleSystem 处理 system 帧：init 捕获会话键，其余转发翻译器。
func (s *claudeSession) handleSystem(frame systemFrame) {
	switch frame.Subtype {
	case "init":
		// 会话键已在构造时经 --session-id 固定；init 帧只补空（旧版 CLI）。
		if frame.SessionID != "" {
			s.mu.Lock()
			empty := s.claudeID == ""
			if empty {
				s.claudeID = frame.SessionID
			}
			s.mu.Unlock()
			if empty {
				select {
				case s.claudeIDCh <- frame.SessionID:
				default:
				}
				if s.onKeyChange != nil {
					s.onKeyChange(frame.SessionID)
				}
			}
		}
		for _, update := range s.translator.handleSystem(frame) {
			s.emit(update)
		}
	case "conversation_reset":
		if frame.NewConversationID != "" {
			s.mu.Lock()
			s.claudeID = frame.NewConversationID
			s.mu.Unlock()
			if s.onKeyChange != nil {
				s.onKeyChange(frame.NewConversationID)
			}
		}
	default:
		for _, update := range s.translator.handleSystem(frame) {
			s.emit(update)
		}
	}
}
