// diff 的解析与比较：unified diff 解析、行级 LCS 比较、增删统计。
// 纯函数模块，供 DiffView 与变更审阅面板共用。

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

// MAX_LCS_LINES 限制参与 LCS 的中间段规模；超过后退化为整段删除 + 整段新增。
const MAX_LCS_LINES = 600

export function splitLines(text: string): string[] {
  if (!text) return []
  const lines = text.split('\n')
  if (lines.length > 0 && lines[lines.length - 1] === '') lines.pop()
  return lines
}

function contextLine(key: string, text: string, oldNo: number, newNo: number): DiffLine {
  return { key, kind: 'context', text, oldNo, newNo }
}

// parseUnifiedDiff 解析标准 unified diff（---/+++/@@ ），未知行按 meta 展示。
//
// 文件头只在第一个 hunk 之前识别：hunk 内部的内容行可能以 "--- " 或 "+++ " 开头
// （SQL 的 "-- 注释"、Markdown 分隔线等），当成文件头会把删除行吃掉并让行号错位。
export function parseUnifiedDiff(diff: string): DiffLine[] {
  const out: DiffLine[] = []
  let oldNo = 0
  let newNo = 0
  let seq = 0
  let inHunk = false
  for (const text of splitLines(diff)) {
    const key = `u${seq++}`
    const hunk = HUNK_RE.exec(text)
    if (hunk) {
      inHunk = true
      oldNo = Number(hunk[1])
      newNo = Number(hunk[2])
      out.push({ key, kind: 'hunk', text })
      continue
    }
    if (!inHunk && isDiffHeader(text)) {
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
    if (inHunk && text === '') {
      // 空行：部分工具输出的上下文行没有前缀，按上下文行处理以免行号错位。
      out.push(contextLine(key, text, oldNo, newNo))
      oldNo += 1
      newNo += 1
      continue
    }
    out.push({ key, kind: 'meta', text })
  }
  return out
}

// isDiffHeader 判断一行是否是 unified diff 的文件头。
function isDiffHeader(text: string): boolean {
  return (
    text.startsWith('diff ') ||
    text.startsWith('index ') ||
    text.startsWith('--- ') ||
    text.startsWith('+++ ') ||
    text.startsWith('new file') ||
    text.startsWith('deleted file') ||
    text.startsWith('rename ') ||
    text.startsWith('similarity ') ||
    text.startsWith('\\ No newline')
  )
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
