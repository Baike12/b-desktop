import { memo } from 'react'
import type { AssistantBlock, ChatItem } from '../types/chat'
import { MarkdownText } from './MarkdownText'
import { ToolCallCard } from './ToolCallCard'

function blockKey(block: AssistantBlock): string {
  return `${block.kind}-${block.index}`
}

function SparkIcon({ active }: { active: boolean }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      className={`size-3 shrink-0 ${active ? 'animate-pulse text-accent-text' : 'text-subtle'}`}
      aria-hidden="true"
    >
      <path d="M12 3v4M12 17v4M3 12h4M17 12h4M5.6 5.6l2.8 2.8M15.6 15.6l2.8 2.8M18.4 5.6l-2.8 2.8M8.4 15.6l-2.8 2.8" />
    </svg>
  )
}

function ChevronIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-3 shrink-0 text-subtle transition-transform group-open:rotate-90"
      aria-hidden="true"
    >
      <path d="m9 18 6-6-6-6" />
    </svg>
  )
}

// ThinkingBlock 不套卡片：一行低对比标题 + 左侧细线缩进的浅色正文。
function ThinkingBlock({ text, streaming }: { text: string; streaming: boolean }) {
  return (
    <details className="group min-w-0">
      <summary className="list-none flex w-full cursor-pointer select-none items-center gap-1.5 py-0.5 font-mono text-[10px] tracking-[0.12em] text-subtle transition hover:text-fg [&::-webkit-details-marker]:hidden">
        <SparkIcon active={streaming} />
        <span className="uppercase">{streaming ? '思考中' : '思考过程'}</span>
        <span className="normal-case tracking-normal">{text.length} 字</span>
        <ChevronIcon />
      </summary>
      <div className="mt-1 max-h-64 overflow-auto whitespace-pre-wrap break-words border-l border-line pl-3 text-[12.5px] leading-6 text-subtle">
        {text}
      </div>
    </details>
  )
}

// MessageRow 用 memo 包裹：store 只替换发生变化的消息对象，
// 其余消息保持引用不变，因此长消息流里只有正在流式的那条会重渲染。
export const MessageRow = memo(function MessageRow({ item }: { item: ChatItem }) {
  if (item.kind === 'user') {
    return (
      <div className="flex justify-end">
        <div className="max-w-[85%] border border-line bg-panel px-3.5 py-2.5">
          <p className="whitespace-pre-wrap break-words text-[13px] leading-6 text-fg">{item.text}</p>
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
        <p className={`border px-3 py-1 font-mono text-[11px] ${tone}`}>{item.text}</p>
      </div>
    )
  }

  const lastIndex = item.blocks.length - 1

  return (
    <div className="flex min-w-0 flex-col gap-2">
      {item.discarded && (
        <span className="font-mono text-[10px] uppercase tracking-[0.14em] text-danger-text">
          已丢弃
        </span>
      )}

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
