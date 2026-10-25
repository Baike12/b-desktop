import { memo, useState } from 'react'
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

// ThinkingBlock 是一张低对比软卡片：标题行可折叠，正文缩进在卡片内部。
//
// 展开策略与 Zed 的默认行为一致：流式期间展开让人实时看到模型的思考，
// 思考结束后自动收起不占空间；用户一旦手动切换，之后不再自动干预。
function ThinkingBlock({ text, streaming }: { text: string; streaming: boolean }) {
  // userOverride 为 null 表示用户还没手动操作过，展开状态由流式状态决定。
  const [userOverride, setUserOverride] = useState<boolean | null>(null)
  const open = userOverride ?? streaming

  return (
    <details
      open={open}
      className={`group min-w-0 overflow-hidden rounded-xl border bg-panel transition ${
        streaming ? 'border-accent/40' : 'border-line/80'
      }`}
    >
      <summary
        // 阻止 details 的原生 toggle，改由 state 驱动，
        // 这样才能区分「用户手动切换」与「流式状态变化」。
        onClick={(event) => {
          event.preventDefault()
          setUserOverride(!open)
        }}
        className="flex w-full cursor-pointer select-none list-none items-center gap-1.5 px-3 py-2 font-mono text-[10px] tracking-[0.12em] text-subtle transition hover:bg-hover hover:text-fg [&::-webkit-details-marker]:hidden"
      >
        <SparkIcon active={streaming} />
        <span className="uppercase">{streaming ? '思考中' : '思考过程'}</span>
        <span className="normal-case tracking-normal">{text.length} 字</span>
        <ChevronIcon />
      </summary>
      <div className="max-h-64 overflow-auto whitespace-pre-wrap break-words border-t border-line/70 px-3 py-2 text-[12.5px] leading-6 text-subtle">
        {text}
      </div>
    </details>
  )
}

// MessageRow 用 memo 包裹：store 只替换发生变化的消息对象，
// 其余消息保持引用不变，因此长消息流里只有正在流式的那条会重渲染。
export const MessageRow = memo(function MessageRow({ item }: { item: ChatItem }) {
  if (item.kind === 'user') {
    // 后台任务唤醒轮的 prompt 是 <task-notification>，不是用户输入：
    // 渲染成居中的系统提示，不占用户气泡。
    if (item.text.startsWith('<task-notification>')) {
      const summary = /<summary>([\s\S]*?)<\/summary>/.exec(item.text)?.[1]
      return (
        <div className="flex justify-center">
          <p className="max-w-[85%] rounded-full border border-line bg-raised px-3 py-1 font-mono text-[11px] text-subtle">
            {summary ? summary : item.text}
          </p>
        </div>
      )
    }
    return (
      <div className="flex justify-end">
        <div className="max-w-[85%] rounded-2xl rounded-br-md border border-accent/25 bg-accent-soft px-4 py-2.5 shadow-card">
          {item.attachments && item.attachments.length > 0 && (
            <div
              data-testid="user-attachments"
              className={`flex flex-wrap justify-end gap-1 ${item.text || item.images?.length ? 'mb-2' : ''}`}
            >
              {item.attachments.map((mention, index) => (
                <span
                  key={`${mention.kind}-${mention.label}-${index}`}
                  title={mention.label}
                  className="max-w-56 truncate rounded-xs border border-accent/25 bg-panel/70 px-1.5 py-0.5 font-mono text-[10px] text-muted"
                >
                  <span className="text-accent-text">
                    {mention.kind === 'command' ? '/' : '@'}
                  </span>
                  {mention.kind === 'command' ? mention.label.slice(1) : mention.label}
                </span>
              ))}
            </div>
          )}
          {item.images && item.images.length > 0 && (
            <div className={`flex flex-wrap justify-end gap-2 ${item.text ? 'mb-2' : ''}`}>
              {item.images.map((img, index) => (
                <img
                  key={`${index}-${img.data.length}`}
                  src={`data:${img.mediaType || 'image/png'};base64,${img.data}`}
                  alt={`随消息发送的图片 ${index + 1}`}
                  className="max-h-48 rounded-md border border-line/80 object-contain"
                />
              ))}
            </div>
          )}
          {item.text !== '' && (
            <p className="whitespace-pre-wrap break-words text-[13px] leading-6 text-fg">{item.text}</p>
          )}
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
        <p className={`rounded-full border px-3 py-1 font-mono text-[11px] ${tone}`}>{item.text}</p>
      </div>
    )
  }

  return (
    <div className="flex min-w-0 flex-col gap-2">
      {item.discarded && (
        <span className="w-fit rounded-full border border-danger-line bg-danger-soft px-2 py-0.5 font-mono text-[10px] uppercase tracking-[0.14em] text-danger-text">
          已丢弃
        </span>
      )}

      {item.blocks.map((block) => {
        if (block.kind === 'text') {
          // active 表示该块正在接收增量：光标只跟随当前增长的块，
          // 而不是消息里的最后一个块（思考块之后往往还有正文）。
          const active = block.active === true
          return (
            <div key={blockKey(block)} className="min-w-0">
              <MarkdownText text={block.text} streaming={active} />
              {active && (
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
              streaming={block.active === true}
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
