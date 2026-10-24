import { useMemo } from 'react'

import { parseUnifiedDiff, type DiffLineKind } from '../lib/diff'
import { detectLanguage, highlightDiff } from '../lib/highlight'

// ReviewDiff 渲染带语法高亮的 unified diff。
//
// 高亮不是逐行匹配：先把 diff 还原成改动前后的完整文本分别高亮，再按增删关系
// 把 token 映射回每一行，跨行的块注释与模板字符串才不会断裂。

const LINE_CLASS: Record<DiffLineKind, string> = {
  add: 'bg-success-soft text-success-text',
  del: 'bg-danger-soft text-danger-text',
  context: 'text-muted',
  hunk: 'bg-accent-soft text-accent-text',
  meta: 'text-subtle',
}

// PREFIX_CLASS 给行首的 +/- 标记单独上色。
const PREFIX_CLASS: Record<DiffLineKind, string> = {
  add: 'text-success-text',
  del: 'text-danger-text',
  context: 'text-subtle',
  hunk: 'text-accent-text',
  meta: 'text-subtle',
}

// MAX_RENDER_LINES 限制单次渲染的行数，避免超大 diff 拖慢页面。
const MAX_RENDER_LINES = 4000

export interface ReviewDiffProps {
  // diff 是 unified diff 文本。
  diff: string
  // path 用于推断语言；为空时按纯文本渲染。
  path: string
}

export default function ReviewDiff({ diff, path }: ReviewDiffProps) {
  const lines = useMemo(() => parseUnifiedDiff(diff), [diff])
  const tokens = useMemo(
    () => highlightDiff(lines, detectLanguage(path)),
    [lines, path],
  )

  if (lines.length === 0) {
    return (
      <p className="px-3 py-2 text-[11px] text-subtle">没有差异</p>
    )
  }

  const visible = lines.length > MAX_RENDER_LINES ? lines.slice(0, MAX_RENDER_LINES) : lines

  return (
    <div data-testid="review-diff" className="overflow-auto bg-code font-mono text-[11px] leading-5">
      {visible.map((line, index) => {
        const lineTokens = tokens[index]
        const isContent = line.kind === 'add' || line.kind === 'del' || line.kind === 'context'
        const prefix = line.kind === 'add' || line.kind === 'del' ? line.text.slice(0, 1) : ''
        // 内容行去掉前缀标记：token 路径里的文本也不含这个字符，两条路径必须一致，
        // 否则同一行的缩进在有无高亮时差一格。
        const body = isContent ? line.text.slice(1) : line.text
        return (
          <div key={line.key} className={`flex ${LINE_CLASS[line.kind]}`}>
            <span className="w-9 shrink-0 select-none pr-1 text-right text-[10px] opacity-60">
              {line.oldNo ?? ''}
            </span>
            <span className="w-9 shrink-0 select-none pr-1 text-right text-[10px] opacity-60">
              {line.newNo ?? ''}
            </span>
            <span className={`w-3 shrink-0 select-none text-center ${PREFIX_CLASS[line.kind]}`}>
              {prefix}
            </span>
            <span className="whitespace-pre pr-3">
              {lineTokens && lineTokens.length > 0 ? (
                lineTokens.map((token, tokenIndex) => (
                  <span key={tokenIndex} className={token.cls ?? undefined}>
                    {token.text}
                  </span>
                ))
              ) : (
                body || ' '
              )}
            </span>
          </div>
        )
      })}
      {lines.length > MAX_RENDER_LINES && (
        <p className="px-2 py-1 text-[10px] text-warn-text">
          已截断 · 仅显示前 {MAX_RENDER_LINES} 行（共 {lines.length} 行）
        </p>
      )}
    </div>
  )
}
