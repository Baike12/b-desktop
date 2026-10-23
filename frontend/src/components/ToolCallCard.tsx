import { memo, useState } from 'react'
import { stringifyJSON, summarizeInput, truncate } from '../lib/format'
import type { AssistantBlock, ToolStatus } from '../types/chat'

type ToolBlock = Extract<AssistantBlock, { kind: 'tool' }>

const STATUS_META: Record<ToolStatus, { label: string; className: string; dot: string }> = {
  pending: { label: '参数流式', className: 'bg-hover text-muted', dot: 'bg-slate-400' },
  running: { label: '执行中', className: 'bg-accent-soft text-accent-text', dot: 'bg-sky-400' },
  waiting: { label: '待审批', className: 'bg-warn-soft text-warn-text', dot: 'bg-amber-400' },
  success: { label: '完成', className: 'bg-success-soft text-success-text', dot: 'bg-emerald-500' },
  error: { label: '失败', className: 'bg-danger-soft text-danger-text', dot: 'bg-rose-500' },
  denied: { label: '已拒绝', className: 'bg-danger-soft text-danger-text', dot: 'bg-rose-500' },
}

// ToolCallCard 展示一次工具调用：工具名、状态、参数摘要，展开后看完整参数与结果预览。
export const ToolCallCard = memo(function ToolCallCard({ block }: { block: ToolBlock }) {
  const [open, setOpen] = useState(false)
  const meta = STATUS_META[block.status]
  const summary =
    block.input !== undefined
      ? summarizeInput(block.input)
      : block.inputText
        ? `${block.inputText.length} 字符参数正在流式接收…`
        : ''

  const inputDetail = block.input !== undefined ? stringifyJSON(block.input) : block.inputText
  const resultPreview = block.result?.preview ?? ''

  return (
    <div className="overflow-hidden rounded-lg border border-line bg-raised">
      <button
        type="button"
        onClick={() => setOpen((value) => !value)}
        className="flex w-full cursor-pointer items-center gap-2 px-3 py-2 text-left transition hover:bg-hover"
        aria-expanded={open}
      >
        <span className={`size-1.5 shrink-0 rounded-full ${meta.dot}`} />
        <span className="shrink-0 font-mono text-xs text-fg">{block.toolName}</span>
        <span className={`shrink-0 rounded-full px-2 py-0.5 text-[10px] ${meta.className}`}>
          {meta.label}
        </span>
        <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-subtle">
          {summary}
        </span>
        <span className="shrink-0 text-[10px] text-subtle">{open ? '收起' : '详情'}</span>
      </button>

      {block.status === 'running' && block.progress && (
        <p className="border-t border-line px-3 py-1.5 font-mono text-[11px] text-accent-text">
          {truncate(block.progress, 200)}
        </p>
      )}

      {open && (
        <div className="space-y-2 border-t border-line px-3 py-2">
          {inputDetail && (
            <div>
              <p className="mb-1 text-[10px] uppercase tracking-wide text-subtle">参数</p>
              <pre className="max-h-56 overflow-auto whitespace-pre-wrap break-words rounded bg-code p-2 font-mono text-[11px] text-muted">
                {truncate(inputDetail, 4000)}
              </pre>
            </div>
          )}
          {block.result && (
            <div>
              <p className="mb-1 flex items-center gap-2 text-[10px] uppercase tracking-wide text-subtle">
                结果
                {block.result.truncated && (
                  <span className="normal-case text-warn-text">
                    已截断 · {block.result.bytes} 字节
                  </span>
                )}
              </p>
              <pre
                className={
                  block.result.isError
                    ? 'max-h-56 overflow-auto whitespace-pre-wrap break-words rounded bg-danger-soft p-2 font-mono text-[11px] text-danger-text'
                    : 'max-h-56 overflow-auto whitespace-pre-wrap break-words rounded bg-code p-2 font-mono text-[11px] text-muted'
                }
              >
                {truncate(resultPreview, 4000) || '（空结果）'}
              </pre>
            </div>
          )}
        </div>
      )}
    </div>
  )
})
