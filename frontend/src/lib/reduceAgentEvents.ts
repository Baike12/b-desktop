import type { AgentEvent, ApprovalView, RetryInfo, Usage } from '../types/agent'
import type { AssistantBlock, AssistantItem, ChatItem, ToolStatus } from '../types/chat'

// StreamState 是 agent 事件影响的状态子集，便于纯函数式归并。
export interface StreamState {
  items: ChatItem[]
  running: boolean
  step: number
  compacting: boolean
  retry: RetryInfo | null
  approvals: ApprovalView[]
  roundUsage: Usage | null
  contextTokens: number
  stopReason: string | null
}

// Draft 在一帧内对 items 做写时复制：数组只复制一次，单条消息只在首次修改时复制，
// 因此高频 text_delta 不会在每一帧里重建整条消息流，未变化的消息引用保持稳定，
// 配合 memo 的消息行可以避免全量重渲染。
interface Draft {
  items: ChatItem[]
  cloned: Set<number>
}

let syntheticSeq = 0

// nextId 为前端合成的系统消息生成稳定唯一 ID（只用于 React key）。
function nextId(prefix: string): string {
  syntheticSeq += 1
  return `${prefix}-${syntheticSeq}`
}

function mutableAssistant(draft: Draft, index: number): AssistantItem | null {
  const current = draft.items[index]
  if (!current || current.kind !== 'assistant') return null
  if (!draft.cloned.has(index)) {
    draft.items[index] = { ...current, blocks: current.blocks.slice() }
    draft.cloned.add(index)
  }
  return draft.items[index] as AssistantItem
}

function mutableBlockAt(item: AssistantItem, pos: number): AssistantBlock {
  const copy = { ...item.blocks[pos] } as AssistantBlock
  item.blocks[pos] = copy
  return copy
}

function assistantFor(
  draft: Draft,
  messageIndex: Map<string, number>,
  messageId: string | undefined,
  timeMs: number,
): { item: AssistantItem; index: number } | null {
  if (!messageId) return null
  const existing = messageIndex.get(messageId)
  if (existing !== undefined) {
    const item = mutableAssistant(draft, existing)
    return item ? { item, index: existing } : null
  }
  const item: AssistantItem = {
    kind: 'assistant',
    id: messageId,
    blocks: [],
    timeMs,
    streaming: true,
  }
  draft.items.push(item)
  const position = draft.items.length - 1
  messageIndex.set(messageId, position)
  draft.cloned.add(position)
  return { item, index: position }
}

function ensureTextBlock(
  item: AssistantItem,
  blockIndex: number,
  kind: 'text' | 'thinking',
): AssistantBlock | null {
  const pos = item.blocks.findIndex((block) => block.index === blockIndex)
  if (pos >= 0) {
    const block = mutableBlockAt(item, pos)
    return block.kind === kind ? block : null
  }
  const created: AssistantBlock =
    kind === 'text'
      ? { kind: 'text', index: blockIndex, text: '' }
      : { kind: 'thinking', index: blockIndex, text: '' }
  item.blocks.push(created)
  return created
}

function ensureToolBlock(
  item: AssistantItem,
  blockIndex: number,
  toolUseId: string,
  toolName: string,
): AssistantBlock | null {
  const pos = item.blocks.findIndex(
    (block) =>
      block.kind === 'tool' &&
      (block.index === blockIndex || (toolUseId !== '' && block.toolUseId === toolUseId)),
  )
  if (pos >= 0) {
    return mutableBlockAt(item, pos)
  }
  const created: AssistantBlock = {
    kind: 'tool',
    index: blockIndex,
    toolUseId,
    toolName,
    inputText: '',
    input: undefined,
    status: 'pending',
  }
  item.blocks.push(created)
  return created
}

// findTool 从后往前按 toolUseId 定位工具块，并返回已写时复制的块对象。
function findTool(
  draft: Draft,
  toolUseId: string | undefined,
): { item: AssistantItem; block: Extract<AssistantBlock, { kind: 'tool' }> } | null {
  if (!toolUseId) return null
  for (let i = draft.items.length - 1; i >= 0; i -= 1) {
    const item = draft.items[i]
    if (item.kind !== 'assistant') continue
    for (let pos = item.blocks.length - 1; pos >= 0; pos -= 1) {
      const block = item.blocks[pos]
      if (block.kind !== 'tool' || block.toolUseId !== toolUseId) continue
      const mutable = mutableAssistant(draft, i)
      if (!mutable) return null
      const copied = mutableBlockAt(mutable, pos)
      return copied.kind === 'tool' ? { item: mutable, block: copied } : null
    }
  }
  return null
}

// markToolByName 把最近一个匹配工具名的块从指定状态迁移到新状态。
// 审批事件不带 toolUseId，只能用工具名近似匹配当前等待中的调用。
function markToolByName(
  draft: Draft,
  toolName: string,
  from: ToolStatus[],
  to: ToolStatus,
): void {
  for (let i = draft.items.length - 1; i >= 0; i -= 1) {
    const item = draft.items[i]
    if (item.kind !== 'assistant') continue
    for (let pos = item.blocks.length - 1; pos >= 0; pos -= 1) {
      const block = item.blocks[pos]
      if (block.kind !== 'tool' || block.toolName !== toolName) continue
      if (!from.includes(block.status)) continue
      const mutable = mutableAssistant(draft, i)
      if (!mutable) return
      const copied = mutableBlockAt(mutable, pos)
      if (copied.kind === 'tool') copied.status = to
      return
    }
  }
}

function markMessageStreaming(draft: Draft, messageId: string | undefined, streaming: boolean): void {
  if (!messageId) return
  for (let i = 0; i < draft.items.length; i += 1) {
    const item = draft.items[i]
    if (item.kind !== 'assistant' || item.id !== messageId) continue
    const mutable = mutableAssistant(draft, i)
    if (mutable) mutable.streaming = streaming
    return
  }
}

function markAllStreaming(draft: Draft): void {
  for (let i = 0; i < draft.items.length; i += 1) {
    const item = draft.items[i]
    if (item.kind !== 'assistant' || !item.streaming) continue
    const mutable = mutableAssistant(draft, i)
    if (mutable) mutable.streaming = false
  }
}

function pushSystem(draft: Draft, tone: 'info' | 'error', text: string, timeMs: number): void {
  draft.items.push({ kind: 'system', id: nextId('sys'), tone, text, timeMs })
}

function rejectionLabel(option: string): string {
  switch (option) {
    case 'allow_once':
    case 'allow_always':
      return 'allow'
    case 'deny_once':
    case 'deny_always':
      return 'deny'
    default:
      return 'canceled'
  }
}

// reduceAgentEvents 纯函数归并一批事件。事件按 seq 升序传入。
export function reduceAgentEvents(state: StreamState, events: AgentEvent[]): StreamState {
  const draft: Draft = { items: state.items.slice(), cloned: new Set<number>() }
  const messageIndex = new Map<string, number>()
  draft.items.forEach((item, index) => {
    if (item.kind === 'assistant') messageIndex.set(item.id, index)
  })

  let running = state.running
  let step = state.step
  let compacting = state.compacting
  let retry = state.retry
  let approvals = state.approvals
  let roundUsage = state.roundUsage
  let contextTokens = state.contextTokens
  let stopReason = state.stopReason

  for (const ev of events) {
    const timeMs = ev.timeMs || Date.now()
    const blockIndex = ev.blockIndex ?? 0

    switch (ev.type) {
      case 'turn_queued': {
        running = true
        break
      }
      case 'turn_start': {
        running = true
        step = 0
        compacting = false
        retry = null
        stopReason = null
        break
      }
      case 'step_start': {
        step = ev.step ?? step + 1
        break
      }
      case 'message_start': {
        assistantFor(draft, messageIndex, ev.messageId, timeMs)
        break
      }
      case 'text_delta': {
        const target = assistantFor(draft, messageIndex, ev.messageId, timeMs)
        if (!target) break
        const block = ensureTextBlock(target.item, blockIndex, 'text')
        if (block && block.kind === 'text') block.text += ev.text ?? ''
        if (target.item.streaming === false) target.item.streaming = true
        break
      }
      case 'thinking_delta': {
        const target = assistantFor(draft, messageIndex, ev.messageId, timeMs)
        if (!target) break
        const block = ensureTextBlock(target.item, blockIndex, 'thinking')
        if (block && block.kind === 'thinking') block.text += ev.text ?? ''
        break
      }
      case 'tool_call_start': {
        const target = assistantFor(draft, messageIndex, ev.messageId, timeMs)
        if (!target) break
        const block = ensureToolBlock(
          target.item,
          blockIndex,
          ev.toolUseId ?? '',
          ev.toolName ?? 'tool',
        )
        if (block && block.kind === 'tool') {
          block.status = 'pending'
          if (ev.toolName) block.toolName = ev.toolName
          if (ev.toolUseId) block.toolUseId = ev.toolUseId
        }
        break
      }
      case 'tool_input_delta': {
        const tool = findTool(draft, ev.toolUseId)
        if (tool) {
          tool.block.inputText += ev.text ?? ''
          break
        }
        const target = assistantFor(draft, messageIndex, ev.messageId, timeMs)
        if (!target) break
        const block = ensureToolBlock(
          target.item,
          blockIndex,
          ev.toolUseId ?? '',
          ev.toolName ?? 'tool',
        )
        if (block && block.kind === 'tool') block.inputText += ev.text ?? ''
        break
      }
      case 'tool_call_ready': {
        const tool = findTool(draft, ev.toolUseId)
        const block =
          tool?.block ??
          (() => {
            const target = assistantFor(draft, messageIndex, ev.messageId, timeMs)
            if (!target) return null
            const created = ensureToolBlock(
              target.item,
              blockIndex,
              ev.toolUseId ?? '',
              ev.toolName ?? 'tool',
            )
            return created && created.kind === 'tool' ? created : null
          })()
        if (!block) break
        if (ev.toolName) block.toolName = ev.toolName
        if (ev.toolUseId) block.toolUseId = ev.toolUseId
        block.input = ev.input
        block.inputText = ev.input === undefined ? block.inputText : JSON.stringify(ev.input)
        block.status = 'running'
        break
      }
      case 'tool_progress': {
        const tool = findTool(draft, ev.toolUseId)
        if (tool) {
          tool.block.progress = ev.text ?? ''
          tool.block.status = 'running'
        }
        break
      }
      case 'permission_request': {
        const approval = ev.approval
        if (!approval) break
        const exists = approvals.some((item) => item.requestId === approval.requestId)
        approvals = exists
          ? approvals.map((item) =>
              item.requestId === approval.requestId ? approval : item,
            )
          : [...approvals, approval]
        markToolByName(draft, approval.toolName, ['pending', 'running'], 'waiting')
        break
      }
      case 'permission_resolved': {
        const requestId = ev.approval?.requestId
        if (!requestId) break
        const approval = approvals.find((item) => item.requestId === requestId)
        approvals = approvals.filter((item) => item.requestId !== requestId)
        const outcome = rejectionLabel(ev.text ?? '')
        if (approval) {
          markToolByName(
            draft,
            approval.toolName,
            ['waiting'],
            outcome === 'allow' ? 'running' : 'denied',
          )
        }
        break
      }
      case 'tool_result': {
        const tool = findTool(draft, ev.toolUseId)
        if (!tool) break
        tool.block.result = ev.result
        tool.block.progress = undefined
        tool.block.status = ev.result?.isError ? 'error' : 'success'
        break
      }
      case 'usage': {
        if (ev.usage) roundUsage = ev.usage
        if (ev.contextTokens) contextTokens = ev.contextTokens
        break
      }
      case 'compact_start': {
        compacting = true
        break
      }
      case 'compact_end': {
        compacting = false
        if (ev.compact) {
          if (ev.compact.failed) {
            pushSystem(
              draft,
              'error',
              `上下文压缩失败：${ev.compact.error || '未知原因'}`,
              timeMs,
            )
          } else if (ev.compact.postTokens > 0) {
            pushSystem(
              draft,
              'info',
              `上下文已压缩：${ev.compact.preTokens} → ${ev.compact.postTokens} tokens`,
              timeMs,
            )
          }
        }
        break
      }
      case 'retry': {
        retry = ev.retry ?? null
        break
      }
      case 'message_stop': {
        markMessageStreaming(draft, ev.messageId, false)
        break
      }
      case 'message_discard': {
        if (ev.messageId) {
          const index = messageIndex.get(ev.messageId)
          if (index !== undefined) {
            draft.items = draft.items.filter((_, position) => position !== index)
            draft.cloned.clear()
            messageIndex.clear()
            draft.items.forEach((item, position) => {
              if (item.kind === 'assistant') messageIndex.set(item.id, position)
            })
          }
        }
        pushSystem(
          draft,
          'error',
          `已丢弃未完成的回复：${ev.error?.message || '流中断'}`,
          timeMs,
        )
        break
      }
      case 'error': {
        pushSystem(draft, 'error', ev.error?.message || ev.error?.kind || '发生未知错误', timeMs)
        break
      }
      case 'turn_end': {
        running = false
        compacting = false
        retry = null
        if (ev.usage) roundUsage = ev.usage
        if (ev.contextTokens) contextTokens = ev.contextTokens
        if (ev.stopReason) stopReason = ev.stopReason
        approvals = []
        markAllStreaming(draft)
        if (ev.stopReason === 'canceled') pushSystem(draft, 'info', '本轮已停止', timeMs)
        else if (ev.stopReason === 'max_turns')
          pushSystem(draft, 'info', '达到最大模型调用次数，本轮结束', timeMs)
        else if (ev.stopReason === 'blocking_limit')
          pushSystem(draft, 'error', '上下文超出模型上限，本轮结束', timeMs)
        break
      }
      case 'block_start':
      default:
        break
    }
  }

  return {
    items: draft.items,
    running,
    step,
    compacting,
    retry,
    approvals,
    roundUsage,
    contextTokens,
    stopReason,
  }
}
