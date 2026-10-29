import { memo, useState } from 'react'
import { useWorkbenchStore } from '../stores/workbench'
import type { AssistantBlock, ChatItem } from '../types/chat'
import { MarkdownText } from './MarkdownText'
import { ToolCallCard } from './ToolCallCard'

function blockKey(block: AssistantBlock): string {
  return `${block.kind}-${block.index}`
}

function ChevronIcon({ className = '' }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className={`size-3 shrink-0 text-subtle transition-transform ${className}`}
      aria-hidden="true"
    >
      <path d="m6 9 6 6 6-6" />
    </svg>
  )
}

// ThoughtIcon 对齐 Zed thinking 头部的小灯泡图标。
function ThoughtIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-3.5 shrink-0"
      aria-hidden="true"
    >
      <path d="M9 18h6" />
      <path d="M10 22h4" />
      <path d="M12 2a7 7 0 0 0-4 12.7c.6.5 1 1.4 1 2.3h6c0-.9.4-1.8 1-2.3A7 7 0 0 0 12 2z" />
    </svg>
  )
}

// ThinkingBlock 对齐 Zed 的思考块：一行 muted 小标题可点折叠，
// 内容用普通 Markdown 排版缩进在左侧竖线后面（非斜体、不缩灰）。
//
// 展开策略与 Zed 的默认行为一致：流式期间展开让人实时看到模型的思考，
// 思考结束后自动收起不占空间；用户一旦手动切换，之后不再自动干预。
function ThinkingBlock({ text, streaming }: { text: string; streaming: boolean }) {
  // userOverride 为 null 表示用户还没手动操作过，展开状态由流式状态决定。
  const [userOverride, setUserOverride] = useState<boolean | null>(null)
  const open = userOverride ?? streaming

  return (
    <div data-testid="thinking-block" className="group min-w-0">
      <button
        type="button"
        aria-expanded={open}
        data-testid="thinking-toggle"
        onClick={() => setUserOverride(!open)}
        className="flex cursor-pointer items-center gap-1.5 rounded-xs py-0.5 text-[13px] text-muted transition hover:text-fg"
      >
        <ThoughtIcon />
        <span>{streaming ? '思考中' : '思考过程'}</span>
        <ChevronIcon className={`opacity-0 transition-opacity group-hover:opacity-100 ${open ? '' : 'rotate-[-90deg]'}`} />
      </button>
      {open && (
        <div className="ml-1.5 max-h-64 overflow-auto border-l border-line-strong pl-3.5 pt-1">
          <MarkdownText text={text} streaming={streaming} />
        </div>
      )}
    </div>
  )
}

// MessageRow 用 memo 包裹：store 只替换发生变化的消息对象，
// 其余消息保持引用不变，因此长消息流里只有正在流式的那条会重渲染。
export const MessageRow = memo(function MessageRow({ item }: { item: ChatItem }) {
  // 双击用户气泡 = 回溯到这条消息（cc 的双击 Esc 等价入口）。
  const openRewind = useWorkbenchStore((state) => state.openRewind)
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
      // Zed 的用户消息是一张全宽卡片：白底 + 1px 描边 + 6px 圆角，
      // 文字用等宽 12px（agent_buffer_font_size），hover 时描边转强调色。
      <div className="flex">
        <div
          data-testid={item.queued ? 'queued-message' : 'user-message'}
          title="双击回溯到这条消息"
          onDoubleClick={() => void openRewind()}
          className={`w-full rounded-sm border px-3 py-2.5 shadow-card transition ${
            item.queued
              ? 'border-dashed border-line bg-panel/60 opacity-80'
              : 'border-line bg-surface hover:border-accent/50'
          }`}
        >
          {item.queued && (
            <p className="mb-1 font-mono text-[10px] tracking-[0.12em] text-subtle">
              排队中 · 本轮结束后自动发送
            </p>
          )}
          {item.attachments && item.attachments.length > 0 && (
            <div
              data-testid="user-attachments"
              className={`flex flex-wrap gap-1 ${item.text || item.images?.length ? 'mb-2' : ''}`}
            >
              {item.attachments.map((mention, index) => (
                <span
                  key={`${mention.kind}-${mention.label}-${index}`}
                  title={mention.label}
                  className="max-w-56 truncate rounded-xs border border-line bg-panel px-1.5 py-0.5 font-mono text-[10px] text-muted"
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
            <div className={`flex flex-wrap gap-2 ${item.text ? 'mb-2' : ''}`}>
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
            <p className="whitespace-pre-wrap break-words font-mono text-[12px] leading-[1.75] text-fg">
              {item.text}
            </p>
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
        if (block.kind === 'tool') {
          const hasThread =
            (block.toolName === 'Task' || block.toolName === 'Agent') &&
            block.toolUseId !== ''
          return (
            <div key={blockKey(block)} className="min-w-0">
              <ToolCallCard block={block} />
              {hasThread && <SubagentThread agentId={block.toolUseId} />}
            </div>
          )
        }
        return null
      })}

      {item.streaming && item.blocks.length === 0 && (
        <span className="animate-pulse text-sm text-subtle">▍</span>
      )}
    </div>
  )
})

// SubagentThread 渲染某个 Task/Agent 工具调用的子 Agent 线程：
// agentId 等于该工具 toolUseId 的消息在这里嵌套展示，不再出现在主消息流。
// 流式期间默认展开，用户可手动收起（与 ThinkingBlock 的交互约定一致）。
function SubagentThread({ agentId }: { agentId: string }) {
  const items = useWorkbenchStore((state) => state.items)
  const [userOverride, setUserOverride] = useState<boolean | null>(null)

  const children = items.filter(
    (item) => (item.kind === 'assistant' || item.kind === 'user') && item.agentId === agentId,
  )
  if (children.length === 0) return null

  const streaming = children.some((item) => {
    if (item.kind !== 'assistant') return false
    return (
      item.streaming ||
      item.blocks.some(
        (block) => (block.kind === 'text' || block.kind === 'thinking') && block.active === true,
      ) ||
      item.blocks.some((block) => block.kind === 'tool' && (block.status === 'running' || block.status === 'pending' || block.status === 'waiting'))
    )
  })
  const open = userOverride ?? streaming

  return (
    <details
      open={open}
      data-testid={`subagent-thread-${agentId}`}
      className={`group mt-1 overflow-hidden rounded-xl border bg-panel/60 transition ${
        streaming ? 'border-accent/40' : 'border-line/80'
      }`}
    >
      <summary
        onClick={(event) => {
          event.preventDefault()
          setUserOverride(!open)
        }}
        className="flex w-full cursor-pointer select-none list-none items-center gap-2 px-3 py-1.5 font-mono text-[10px] uppercase tracking-[0.14em] text-subtle transition hover:bg-hover hover:text-fg [&::-webkit-details-marker]:hidden"
      >
        <span
          className={`size-1.5 rounded-full ${streaming ? 'animate-pulse bg-accent' : 'bg-success'}`}
        />
        子 Agent 线程 · {children.length} 条
        <ChevronIcon className="group-open:rotate-180" />
      </summary>
      <div className="flex flex-col gap-3 border-t border-line/70 px-3 py-3">
        {children.map((item) => (
          <MessageRow key={item.id} item={item} />
        ))}
      </div>
    </details>
  )
}
