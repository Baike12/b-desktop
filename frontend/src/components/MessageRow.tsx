import { memo } from 'react'
import { formatClock } from '../lib/format'
import type { AssistantBlock, ChatItem } from '../types/chat'
import { MarkdownText } from './MarkdownText'
import { ToolCallCard } from './ToolCallCard'

function blockKey(block: AssistantBlock): string {
  return `${block.kind}-${block.index}`
}

function ThinkingBlock({ text, streaming }: { text: string; streaming: boolean }) {
  return (
    <details className="rounded-lg border border-line bg-raised px-3 py-2">
      <summary className="cursor-pointer select-none text-[11px] text-subtle">
        思考过程 · {text.length} 字{streaming ? '（进行中）' : ''}
      </summary>
      <pre className="mt-2 max-h-64 overflow-auto whitespace-pre-wrap break-words font-mono text-[11px] leading-5 text-muted">
        {text}
      </pre>
    </details>
  )
}

// MessageRow 用 memo 包裹：store 只替换发生变化的消息对象，
// 其余消息保持引用不变，因此长消息流里只有正在流式的那条会重渲染。
export const MessageRow = memo(function MessageRow({ item }: { item: ChatItem }) {
  if (item.kind === 'user') {
    return (
      <div className="flex justify-end">
        <div className="max-w-[85%] rounded-2xl rounded-br-sm bg-accent-soft px-4 py-2.5">
          <p className="whitespace-pre-wrap break-words text-sm leading-6 text-fg">{item.text}</p>
          <p className="mt-1 text-right text-[10px] text-subtle">{formatClock(item.timeMs)}</p>
        </div>
      </div>
    )
  }

  if (item.kind === 'system') {
    const tone =
      item.tone === 'error'
        ? 'border-danger-line bg-danger-soft text-danger-text'
        : 'border-line bg-raised text-subtle'
    return (
      <div className="flex justify-center">
        <p className={`rounded-md border px-3 py-1.5 text-xs ${tone}`}>{item.text}</p>
      </div>
    )
  }

  const lastIndex = item.blocks.length - 1

  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center gap-2 text-[11px] text-subtle">
        <span>助手</span>
        <span>·</span>
        <span>{formatClock(item.timeMs)}</span>
        {item.discarded && <span className="text-danger-text">已丢弃</span>}
      </div>

      {item.blocks.map((block, index) => {
        if (block.kind === 'text') {
          return (
            <div key={blockKey(block)} className="min-w-0">
              <MarkdownText
                text={block.text}
                streaming={item.streaming && index === lastIndex}
              />
              {item.streaming && index === lastIndex && (
                <span className="inline-block animate-pulse text-sm text-accent-text">▍</span>
              )}
            </div>
          )
        }
        if (block.kind === 'thinking') {
          return (
            <ThinkingBlock
              key={blockKey(block)}
              text={block.text}
              streaming={item.streaming && index === lastIndex}
            />
          )
        }
        return <ToolCallCard key={blockKey(block)} block={block} />
      })}

      {item.streaming && item.blocks.length === 0 && (
        <span className="animate-pulse text-sm text-subtle">▍</span>
      )}
    </div>
  )
})
