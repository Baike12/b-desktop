# ACP 支持：外部 Agent 桥接（Claude Code 等）

b-desktop 通过 ACP（Agent Client Protocol）驱动外部 agent。Claude Code 是第一个内置
后端：Go 直接 spawn `claude` CLI 并说它的 stream-json 双向协议，行为对齐 zed 的
`claude-agent-acp` 适配器（Node 实现），**不需要** Node 插件进程。后续接入 codex 等
外部 agent 时，走通用外部 ACP 进程客户端（`internal/acp/client.go`）即可。

## 架构

```text
前端 Composer（AgentSelect）
  │  SendMessage / ListAgents / SetSessionAgent
  ▼
internal/app（会话路由：benben → agent.Engine；外部 → acp.Bridge）
  │                              │
  │                       internal/acp.Bridge
  │                        │ 更新→agent.Event、transcript 落盘、权限审批
  │                              │
  │              ┌───────────────┴───────────────┐
  │       internal/acp/claude              internal/acp.ExternalBackend
  │       （内置 claude 后端：                 （通用 ACP v1 客户端：
  │        spawn claude CLI，                  NDJSON JSON-RPC over stdio，
  │        stream-json 双向协议）               后续 codex 走这条路径）
  ▼                              ▼
本地 agent 引擎                claude / 外部 agent 进程
```

分层职责：

| 包 | 职责 |
|---|---|
| `internal/acp` | ACP v1 类型（`types.go`）、`Backend`/`Session`/`Client` 抽象与注册表（`session.go`）、外部进程客户端（`client.go`）、`Bridge`（`bridge.go`：ACP 更新 → `agent.Event` + transcript 落盘 + 审批对接） |
| `internal/acp/claude` | 内置 Claude Code 后端：CLI 发现（`cli.go`）、stream-json 线类型（`protocol.go`）、进程与控制通道（`process.go`）、SDK 帧 → ACP 更新翻译（`translate.go`）、工具标题映射（`tools.go`）、权限选项（`permission.go`）、会话实现（`session.go`） |

## 会话生命周期

1. **首次发送**：`app.SendMessage` 按 `rt.agent` 路由；外部 agent 时构建
   `acp.Bridge`（工作目录、MCP 配置、审批通道、resume 键来自 transcript）。
2. **握手**：spawn `claude --output-format stream-json --verbose
   --include-partial-messages --input-format stream-json --setting-sources=user,project,local
   --permission-prompt-tool stdio [--permission-mode X] [--session-id=<uuid>|--resume=<uuid>]
   [--mcp-config <json>]`，随后发 `control_request(initialize, forwardSubagentText=true)`。
   响应携带命令清单 / 模型目录 / 当前权限模式，经 `status_update` 与
   `available_commands_update` 推给前端。
3. **会话键**：UUID 由本侧生成并经 `--session-id` 固定，握手后即写入 transcript 的
   `agent_state` 记录（`<agentId>.key`）；重启后用 `--resume=<key>` 续接 claude 上下文。
   resume 失败（会话已不存在）自动清键重开。
4. **一轮**：写 user 消息帧 → 消费 stream_event / assistant / user / result 帧 →
   result 映射为 `agent.Result`（stopReason、usage、turns）。进程跨轮次保活。
5. **取消**：`Cancel` 发 `control_request(interrupt)`；等待中的 can_use_tool 以
   deny+interrupt 应答；result 到达后按 `cancelled` 结算。
6. **释放**：`Shutdown` 关闭桥接层与 CLI 进程。

## 翻译规则（对齐 claude-agent-acp）

| CLI 帧 | ACP 更新 | agent.Event |
|---|---|---|
| `stream_event` text/thinking delta | `agent_message_chunk` / `agent_thought_chunk`（带 MessageID 分组） | `text_delta` / `thinking_delta` |
| `stream_event` input_json_delta | `tool_call_update.inputDelta`（b-desktop 扩展） | `tool_input_delta` |
| `assistant` 帧 text/thinking | 只补发未被流式覆盖的剩余部分（去重） | 同上 |
| tool_use（首发） | `tool_call`（kind/title/locations 见 `tools.go` 映射表） | `tool_call_start`（+`tool_call_ready`） |
| tool_use（已见） | `tool_call_update`（完整 rawInput） | `tool_call_ready` |
| `user` 帧 tool_result | `tool_call_update` status=completed/failed | `tool_result` |
| TodoWrite 的 tool_use | 追加 `plan` 快照（保留工具卡片，与本地 agent UI 一致） | `todo_update` |
| initialize 响应 / `system:commands_changed` | `available_commands_update` / `status_update` | `commands_update` / `agent_status` |
| `result` 帧 | 结算（success→end_turn、error_max_*→max_turn_requests、refusal、is_error→Error） | `usage` + `turn_end` |

**子 Agent 路由（对比 zed 的增强）**：CLI 帧上的 `parent_tool_use_id` 被翻译为
`SessionUpdate.AgentID`，桥接层再落到 `agent.Event.AgentID`。前端把
`agentId == 某 Task 工具 toolUseId` 的消息渲染进该工具卡片的「子 Agent 线程」内嵌
视图，而不是像 zed 一样混进主面板。子线程内容只产生事件、**不落 transcript**
（保证 tool_result 紧跟 assistant 消息的重放配对不变）。

**transcript 落盘**：桥接层把 ACP 更新流重建成与本地引擎相同的记录集
（message / usage / turn_end / agent_state），`LoadSession` 重放无需感知 agent 差异。

## 权限

- CLI 以 `--permission-prompt-tool stdio` 发 `control_request(can_use_tool)`，
  claude 后端转成 ACP `PermissionRequest`（含 `permission_suggestions` 解析），
  桥接层登记到会话级 `Approvals`（与 benben 共用），前端弹现有审批窗。
- 选项固定三档：`allow-once` / `allow-with-updates`（采纳 CLI 的 allow 建议；
  无建议时合成工具级 allow 规则）/ `reject`；同时兼容四档标准选项 id。
- 权限模式（default/acceptEdits/plan/bypassPermissions/dontAsk）经
  `--permission-mode` 传入、`control_request(set_permission_mode)` 实时切换；
  CLI 侧模式变化经 `status_update.Mode` 反向前端同步。

## 前端

- `AgentSelect`：输入框底部左侧（附件按钮之后、权限模式选择之前），列出
  `ListAgents()` 结果（Benben Agent 恒在首位 + 注册表后端，附可用性探测）。
  agent 是会话级属性，切换写 `agent_state` 记录并推系统提示；运行中禁切。
- `agent_status` 事件更新输入框的模型名与上下文窗口（claude 会话显示 CLI 报告的
  模型，如 `MiniMax-M3.1-Flash[1m]`）与权限模式显示。
- `commands_update` 的命令并入 `/` 补全（本应用内置命令同名优先）。
- claude 会话下 `/compact`、`/clear` 原文转发给 CLI（CLI 自己处理压缩与重置，
  `conversation_reset` 帧会更新会话键）；`/model`、`/export` 仍是本应用命令。

## 验证

```bash
go test ./internal/acp/...          # 翻译层、假 CLI 端到端（握手/工具/权限/取消）、桥接落盘
# 真实 CLI 冒烟（本机需安装并登录 claude）：
BDESKTOP_ACP_E2E=1      go test ./internal/acp/claude/ -run TestRealCLIHandshake
BDESKTOP_ACP_E2E_FULL=1 go test ./internal/acp/claude/ -run TestRealCLIPrompt      # 消耗一次极小 API 调用
BDESKTOP_ACP_E2E_TOOL=1 go test ./internal/acp/claude/ -run TestRealCLIToolUse    # 真实 Bash 工具往返
```

## 扩展新 agent（如 codex）

1. 在 `internal/acp` 增加后端：实现 `Backend`（`NewSession` 返回 `Session`），或直接
   复用 `ExternalBackend`（传入外部 ACP 进程的启动命令）。
2. 在 `app.agentRegistry()` 注册（`Registry.Register`），前端选择器自动出现新条目。
3. 如外部 agent 需要文件回调，桥接层的 `ReadTextFile`/`WriteTextFile` 已实现并限定
   在会话工作目录内。
