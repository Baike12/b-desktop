import type { ToolResultView } from './agent'

// ToolStatus 是工具卡片的状态机：
// pending（参数流式中）→ running（执行中）→ waiting（等待审批）
// → success / error / denied。
export type ToolStatus =
  | 'pending'
  | 'running'
  | 'waiting'
  | 'success'
  | 'error'
  | 'denied'

// AssistantBlock 是 assistant 消息内的一个块。index 是后端 blockIndex，
// 在同一条消息内唯一，用来做 React key 与增量定位。
export type AssistantBlock =
  | { kind: 'text'; index: number; text: string; active?: boolean }
  | { kind: 'thinking'; index: number; text: string; active?: boolean }
  | {
      kind: 'tool'
      index: number
      toolUseId: string
      toolName: string
      // 参数流式阶段的原始 JSON 片段，参数完整后由 input 取代。
      inputText: string
      input: unknown
      status: ToolStatus
      result?: ToolResultView
      progress?: string
      note?: string
    }

export type AssistantItem = {
  kind: 'assistant'
  id: string
  blocks: AssistantBlock[]
  timeMs: number
  streaming: boolean
  discarded?: boolean
}

// UserImage 是随用户消息提交的一张 base64 图片。
export type UserImage = {
  mediaType: string
  data: string
}

// UserMention 是用户消息里一个 @ 引用或 / 命令的展示摘要：
// 后端把引用内容展开成 system-reminder 附件块，前端只渲染文件名/命令名。
export type UserMention = {
  kind: 'file' | 'directory' | 'command'
  label: string
}

export type UserItem = {
  kind: 'user'
  id: string
  text: string
  images?: UserImage[]
  attachments?: UserMention[]
  timeMs: number
}

export type SystemItem = {
  kind: 'system'
  id: string
  tone: 'info' | 'error'
  text: string
  timeMs: number
}

export type ChatItem = UserItem | AssistantItem | SystemItem
