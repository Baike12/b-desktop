import { memo, useMemo } from 'react'

// DiffView 把 unified diff 或 old/new 文本渲染成红绿行。
// 数据源优先级：diff（真正的 unified diff）> oldText/newText（前端做行级比较）。

export type DiffLineKind = 'add' | 'del' | 'context' | 'hunk' | 'meta'

export interface DiffLine {
  // key 在解析时按行生成，供 React 列表稳定复用。
  key: string
  kind: DiffLineKind
  text: string
  oldNo?: number
  newNo?: number
}

const HUNK_RE = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/

const LINE_CLASS: Record<DiffLineKind, string> = {
  add: 'bg-success-soft text-success-text',
  del: 'bg-danger-soft text-danger-text',
  hunk: 'bg-accent-soft text-accent-text',
  context: 'text-muted',
  meta: 'text-subtle',
}

// MAX_RENDER_LINES 限制单次渲染的行数，避免超大 diff 拖慢展开操作。
const MAX_RENDER_LINES = 4000

// MAX_LCS_LINES 限制参与 LCS 的中间段规模；超过后退化为整段删除 + 整段新增。
const MAX_LCS_LINES = 600

function splitLines(text: string): string[] {
  if (!text) return []
  const lines = text.split('\n')
  if (lines.length > 0 && lines[lines.length - 1] === '') lines.pop()
  return lines
}

function contextLine(key: string, text: string, oldNo: number, newNo: number): DiffLine {
  return { key, kind: 'context', text, oldNo, newNo }
}

// parseUnifiedDiff 解析标准 unified diff（---/+++/@@ ），未知行按 meta 展示。
export function parseUnifiedDiff(diff: string): DiffLine[] {
  const out: DiffLine[] = []
  let oldNo = 0
  let newNo = 0
  let seq = 0
  for (const text of splitLines(diff)) {
    const key = `u${seq++}`
    const hunk = HUNK_RE.exec(text)
    if (hunk) {
      oldNo = Number(hunk[1])
      newNo = Number(hunk[2])
      out.push({ key, kind: 'hunk', text })
      continue
    }
    if (
      text.startsWith('diff ') ||
      text.startsWith('index ') ||
      text.startsWith('--- ') ||
      text.startsWith('+++ ') ||
      text.startsWith('new file') ||
      text.startsWith('deleted file') ||
      text.startsWith('rename ') ||
      text.startsWith('similarity ') ||
      text.startsWith('\\ No newline')
    ) {
      out.push({ key, kind: 'meta', text })
      continue
    }
    if (text.startsWith('+')) {
      out.push({ key, kind: 'add', text, newNo })
      newNo += 1
      continue
    }
    if (text.startsWith('-')) {
      out.push({ key, kind: 'del', text, oldNo })
      oldNo += 1
      continue
    }
    if (text.startsWith(' ')) {
      out.push(contextLine(key, text, oldNo, newNo))
      oldNo += 1
      newNo += 1
      continue
    }
    out.push({ key, kind: 'meta', text })
  }
  return out
}

// buildLineDiff 用「公共前后缀 + LCS」生成行级差异，行号从 1 开始。
export function buildLineDiff(oldText: string, newText: string): DiffLine[] {
  const oldLines = splitLines(oldText)
  const newLines = splitLines(newText)
  const out: DiffLine[] = []
  let seq = 0
  const push = (kind: DiffLineKind, text: string, oldNo?: number, newNo?: number) => {
    out.push({ key: `d${seq++}`, kind, text, oldNo, newNo })
  }

  let prefix = 0
  while (
    prefix < oldLines.length &&
    prefix < newLines.length &&
    oldLines[prefix] === newLines[prefix]
  ) {
    prefix += 1
  }
  let oldEnd = oldLines.length - 1
  let newEnd = newLines.length - 1
  while (oldEnd >= prefix && newEnd >= prefix && oldLines[oldEnd] === newLines[newEnd]) {
    oldEnd -= 1
    newEnd -= 1
  }

  for (let i = 0; i < prefix; i += 1) push('context', oldLines[i], i + 1, i + 1)

  const midOld = oldLines.slice(prefix, oldEnd + 1)
  const midNew = newLines.slice(prefix, newEnd + 1)
  const oldBase = prefix + 1
  const newBase = prefix + 1

  if (
    midOld.length > MAX_LCS_LINES ||
    midNew.length > MAX_LCS_LINES ||
    midOld.length * midNew.length > MAX_LCS_LINES * MAX_LCS_LINES
  ) {
    for (let i = 0; i < midOld.length; i += 1) {
      push('del', midOld[i], oldBase + i)
    }
    for (let i = 0; i < midNew.length; i += 1) {
      push('add', midNew[i], undefined, newBase + i)
    }
  } else {
    const rows = midOld.length + 1
    const cols = midNew.length + 1
    const table = new Uint32Array(rows * cols)
    for (let i = midOld.length - 1; i >= 0; i -= 1) {
      for (let j = midNew.length - 1; j >= 0; j -= 1) {
        const index = i * cols + j
        if (midOld[i] === midNew[j]) {
          table[index] = table[(i + 1) * cols + (j + 1)] + 1
        } else {
          const down = table[(i + 1) * cols + j]
          const right = table[i * cols + (j + 1)]
          table[index] = down >= right ? down : right
        }
      }
    }
    let i = 0
    let j = 0
    while (i < midOld.length && j < midNew.length) {
      if (midOld[i] === midNew[j]) {
        push('context', midOld[i], oldBase + i, newBase + j)
        i += 1
        j += 1
      } else if (table[(i + 1) * cols + j] >= table[i * cols + (j + 1)]) {
        push('del', midOld[i], oldBase + i)
        i += 1
      } else {
        push('add', midNew[j], undefined, newBase + j)
        j += 1
      }
    }
    while (i < midOld.length) {
      push('del', midOld[i], oldBase + i)
      i += 1
    }
    while (j < midNew.length) {
      push('add', midNew[j], undefined, newBase + j)
      j += 1
    }
  }

  // 公共后缀在两边的长度相同，但行号偏移可能不同，分别计算。
  for (let k = oldEnd + 1; k < oldLines.length; k += 1) {
    const oldNo = k + 1
    const newNo = newEnd + 1 + (k - (oldEnd + 1)) + 1
    push('context', oldLines[k], oldNo, newNo)
  }
  return out
}

// diffStats 统计新增/删除行数，用于标题上的 +n / -n。
export function diffStats(lines: DiffLine[]): { add: number; del: number } {
  let add = 0
  let del = 0
  for (const line of lines) {
    if (line.kind === 'add') add += 1
    else if (line.kind === 'del') del += 1
  }
  return { add, del }
}

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
      <div className={`rounded border border-line bg-code px-2 py-1.5 text-[11px] text-subtle ${className}`}>
        {emptyLabel}
      </div>
    )
  }

  return (
    <div className={`overflow-hidden rounded border border-line bg-code ${className}`}>
      <div className="flex items-center gap-2 border-b border-line px-2 py-1 text-[10px] text-subtle">
        {title && <span className="min-w-0 flex-1 truncate font-mono text-muted">{title}</span>}
        <span className="shrink-0 text-success-text">+{stats.add}</span>
        <span className="shrink-0 text-danger-text">-{stats.del}</span>
      </div>
      <div className={`overflow-auto font-mono text-[11px] leading-5 ${maxHeightClass}`}>
        {visible.map((line) => (
          <div key={line.key} className={`flex ${LINE_CLASS[line.kind]}`}>
            <span className="w-10 shrink-0 select-none pr-2 text-right text-[10px] opacity-60">
              {line.oldNo ?? ''}
            </span>
            <span className="w-10 shrink-0 select-none pr-2 text-right text-[10px] opacity-60">
              {line.newNo ?? ''}
            </span>
            <span className="whitespace-pre pr-3">{line.text || ' '}</span>
          </div>
        ))}
        {lines.length > MAX_RENDER_LINES && (
          <p className="px-2 py-1 text-[10px] text-warn-text">
            已截断 · 仅显示前 {MAX_RENDER_LINES} 行（共 {lines.length} 行）
          </p>
        )}
      </div>
    </div>
  )
})

export default DiffView
