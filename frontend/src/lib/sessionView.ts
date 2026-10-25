import type { app } from '../../wailsjs/go/models'
import { parseUserAttachmentBlock } from './mentions'
import type { ToolResultView } from '../types/agent'
import type { AssistantBlock, ChatItem, UserMention } from '../types/chat'

// viewToItems 把 LoadSession 的历史消息转成消息流模型。
// tool_result 消息在历史里是独立的 user 消息，这里按 toolUseId 回挂到工具卡片上，
// 不再渲染成用户气泡；用户消息里的 image 块与 tool_result 附带图片转为可渲染数据；
// system-reminder 附件块（@ 引用、/ 命令展开）转成摘要 chip，不渲染全文。
export function viewToItems(view: app.SessionViewDTO): ChatItem[] {
  const results = new Map<string, ToolResultView>()
  for (const message of view.messages) {
    for (const block of message.blocks) {
      if (block.type !== 'tool_result' || !block.toolUseId) continue
      const text = block.text ?? ''
      const images = (block.images ?? []).map((img) => ({
        mediaType: img.mediaType,
        data: img.data,
      }))
      results.set(block.toolUseId, {
        preview: text,
        isError: Boolean(block.isError),
        truncated: false,
        bytes: text.length,
        images: images.length > 0 ? images : undefined,
      })
    }
  }

  const items: ChatItem[] = []
  for (const message of view.messages) {
    if (message.role === 'user') {
      const attachments: UserMention[] = []
      const textParts: string[] = []
      for (const block of message.blocks) {
        if (block.type !== 'text') continue
        const mention = parseUserAttachmentBlock(block.text ?? '')
        if (mention) attachments.push(mention)
        else textParts.push(block.text ?? '')
      }
      const text = textParts.join('\n')
      const images = message.blocks
        .filter((block) => block.type === 'image' && block.data)
        .map((block) => ({ mediaType: block.mediaType ?? 'image/png', data: block.data ?? '' }))
      if (text.trim() === '' && images.length === 0) continue
      items.push({
        kind: 'user',
        id: message.id,
        text,
        images: images.length > 0 ? images : undefined,
        attachments: attachments.length > 0 ? attachments : undefined,
        timeMs: message.timeMs,
      })
      continue
    }
    if (message.role !== 'assistant') continue

    const blocks: AssistantBlock[] = []
    message.blocks.forEach((block, index) => {
      if (block.type === 'text') {
        blocks.push({ kind: 'text', index, text: block.text ?? '' })
      } else if (block.type === 'thinking') {
        blocks.push({ kind: 'thinking', index, text: block.thinking ?? '' })
      } else if (block.type === 'tool_use') {
        const toolUseId = block.toolUseId ?? `${message.id}:${index}`
        const result = results.get(toolUseId)
        blocks.push({
          kind: 'tool',
          index,
          toolUseId,
          toolName: block.toolName ?? 'tool',
          inputText: '',
          input: block.input,
          status: result ? (result.isError ? 'error' : 'success') : 'running',
          result,
        })
      }
    })
    if (blocks.length === 0) continue
    items.push({
      kind: 'assistant',
      id: message.id,
      blocks,
      timeMs: message.timeMs,
      streaming: false,
    })
  }
  return items
}
