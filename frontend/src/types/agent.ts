// "agent:event" 事件与权限审批的前端契约。
// 字段与 internal/agent/event.go、internal/permission/types.go 一一对应，
// 后端通过 internal/app.Emitter 序列化 agent.Event 后推送。

export type PermissionMode =
  | 'default'
  | 'acceptEdits'
  | 'plan'
  | 'bypassPermissions'
  | 'dontAsk'

export interface ApprovalOption {
  id: string
  label: string
}

// ApprovalView 与 permission.ApprovalView 对应；Input 是工具参数的 JSON 对象。
export interface ApprovalView {
  requestId: string
  sessionId: string
  toolName: string
  input: unknown
  reason: string
  options: ApprovalOption[]
}

// ToolResultView 只带预览（前 2KB），完整内容在会话历史里。
export interface ToolResultView {
  preview: string
  isError: boolean
  truncated: boolean
  bytes: number
}

export interface Usage {
  inputTokens: number
  outputTokens: number
  cacheReadInputTokens: number
  cacheCreationInputTokens: number
}

export interface CompactView {
  trigger: string
  preTokens: number
  postTokens: number
  failed?: boolean
  error?: string
}

export interface RetryInfo {
  attempt: number
  max: number
  delayMs: number
  reason: string
}

export interface AgentErrorView {
  kind: string
  message: string
  status?: number
}

export type AgentEventType =
  | 'turn_queued'
  | 'turn_start'
  | 'step_start'
  | 'message_start'
  | 'block_start'
  | 'text_delta'
  | 'thinking_delta'
  | 'tool_input_delta'
  | 'tool_call_start'
  | 'tool_call_ready'
  | 'tool_progress'
  | 'permission_request'
  | 'permission_resolved'
  | 'tool_result'
  | 'usage'
  | 'compact_start'
  | 'compact_end'
  | 'retry'
  | 'message_stop'
  | 'message_discard'
  | 'error'
  | 'turn_end'

export type StopReason =
  | 'completed'
  | 'max_turns'
  | 'canceled'
  | 'error'
  | 'blocking_limit'

// AgentEvent 是 "agent:event" 的载荷，字段按 type 解释。
export interface AgentEvent {
  type: AgentEventType
  sessionId: string
  turnId: string
  step?: number
  position?: number
  seq: number
  timeMs: number
  messageId?: string
  blockIndex?: number
  text?: string
  toolUseId?: string
  toolName?: string
  input?: unknown
  result?: ToolResultView
  approval?: ApprovalView
  usage?: Usage
  // contextTokens 是最近一次模型调用占用的上下文规模（usage 与 turn_end 携带）。
  contextTokens?: number
  compact?: CompactView
  retry?: RetryInfo
  error?: AgentErrorView
  stopReason?: StopReason | string
}
