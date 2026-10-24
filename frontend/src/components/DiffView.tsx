import { memo, useMemo } from 'react'

import {
  buildLineDiff,
  diffStats,
  parseUnifiedDiff,
  type DiffLine,
  type DiffLineKind,
} from '../lib/diff'

// DiffView 把 unified diff 或 old/new 文本渲染成红绿行。
// 数据源优先级：diff（真正的 unified diff）> oldText/newText（前端做行级比较）。
// 解析与比较逻辑在 lib/diff 中，供变更审阅面板共用。

export type { DiffLine, DiffLineKind }
export { buildLineDiff, diffStats, parseUnifiedDiff }

const LINE_CLASS: Record<DiffLineKind, string> = {
  add: 'bg-success-soft text-success-text',
  del: 'bg-danger-soft text-danger-text',
  hunk: 'bg-accent-soft text-accent-text',
  context: 'text-muted',
  meta: 'text-subtle',
}

// MAX_RENDER_LINES 限制单次渲染的行数，避免超大 diff 拖慢展开操作。
const MAX_RENDER_LINES = 4000

export interface DiffViewProps {
  // diff 是 unified diff 文本；给出时优先使用。
  diff?: string
  // oldText/newText 在前端做行级比较，用于拿不到 unified diff 的工具调用。
  oldText?: string
  newText?: string
  title?: string
  className?: string
  maxHeightClass?: string
  emptyLabel?: string
}

// DiffView 是纯展示组件：所有派生数据（行、统计）都在渲染期计算。
export const DiffView = memo(function DiffView({
  diff,
  oldText,
  newText,
  title,
  className = '',
  maxHeightClass = 'max-h-72',
  emptyLabel = '没有差异',
}: DiffViewProps) {
  const lines = useMemo(() => {
    if (diff && diff.trim() !== '') return parseUnifiedDiff(diff)
    if (oldText !== undefined || newText !== undefined) {
      return buildLineDiff(oldText ?? '', newText ?? '')
    }
    return []
  }, [diff, oldText, newText])

  const stats = useMemo(() => diffStats(lines), [lines])
  const visible = lines.length > MAX_RENDER_LINES ? lines.slice(0, MAX_RENDER_LINES) : lines

  if (lines.length === 0) {
    return (
      <div className={`border border-line bg-code px-2 py-1.5 text-[11px] text-subtle ${className}`}>
        {emptyLabel}
      </div>
    )
  }

  return (
    <div className={`overflow-hidden border border-line bg-code ${className}`}>
      <div className="flex items-center gap-2 border-b border-line bg-raised px-2 py-1 font-mono text-[10px] text-subtle">
        {title && <span className="min-w-0 flex-1 truncate text-muted">{title}</span>}
        <span className="shrink-0 text-success-text">+{stats.add}</span>
        <span className="shrink-0 text-danger-text">-{stats.del}</span>
      </div>
      <div className={`overflow-auto font-mono text-[11px] leading-5 ${maxHeightClass}`}>
        {visible.map((line) => (
          <div key={line.key} className={`flex ${LINE_CLASS[line.kind]}`}>
            <span className="w-10 shrink-0 select-none border-r border-line pr-2 text-right text-[10px] opacity-50">
              {line.oldNo ?? ''}
            </span>
            <span className="w-10 shrink-0 select-none border-r border-line pr-2 text-right text-[10px] opacity-50">
              {line.newNo ?? ''}
            </span>
            <span className="whitespace-pre pr-3">{line.text || ' '}</span>
          </div>
        ))}
        {lines.length > MAX_RENDER_LINES && (
          <p className="border-t border-line px-2 py-1 font-mono text-[10px] text-warn-text">
            已截断 · 仅显示前 {MAX_RENDER_LINES} 行（共 {lines.length} 行）
          </p>
        )}
      </div>
    </div>
  )
})

export default DiffView
