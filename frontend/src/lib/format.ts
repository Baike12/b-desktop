// 纯展示格式化函数：无副作用，可在渲染期直接调用。

const TOOL_SUMMARY_KEYS = [
  'command',
  'cmd',
  'path',
  'file_path',
  'filePath',
  'pattern',
  'query',
  'url',
  'description',
  'prompt',
  'skill',
] as const

// truncate 按字符截断并补省略号。
export function truncate(value: string, max: number): string {
  if (value.length <= max) return value
  return `${value.slice(0, max)}…`
}

// formatTokens 把 token 数压缩成 1.2k / 3.4M 形式，整数结果不带小数位。
export function formatTokens(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return '0'
  const scaled = (unit: number, suffix: string) => {
    const text = (value / unit).toFixed(1).replace(/\.0$/, '')
    return `${text}${suffix}`
  }
  if (value >= 1_000_000) return scaled(1_000_000, 'M')
  if (value >= 1_000) return scaled(1_000, 'k')
  return String(Math.round(value))
}

// formatClock 返回 HH:MM。
export function formatClock(timeMs: number): string {
  if (!timeMs) return ''
  const date = new Date(timeMs)
  return `${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`
}

// formatRelative 返回「刚刚 / n 分钟前 / 月-日」。
export function formatRelative(timeMs: number): string {
  if (!timeMs) return ''
  const diff = Date.now() - timeMs
  if (diff < 60_000) return '刚刚'
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)} 分钟前`
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)} 小时前`
  const date = new Date(timeMs)
  return `${date.getMonth() + 1}-${date.getDate()}`
}

// stringifyJSON 把工具参数格式化；无法序列化时退回 String。
export function stringifyJSON(value: unknown): string {
  if (value === undefined || value === null) return ''
  try {
    return JSON.stringify(value, null, 2)
  } catch {
    return String(value)
  }
}

// summarizeInput 从工具参数里挑一个最可读的字段作为摘要。
export function summarizeInput(value: unknown, max = 120): string {
  if (value === undefined || value === null) return ''
  if (typeof value !== 'object') return truncate(String(value), max)
  const record = value as Record<string, unknown>
  for (const key of TOOL_SUMMARY_KEYS) {
    const candidate = record[key]
    if (typeof candidate === 'string' && candidate.trim() !== '') {
      return `${key}: ${truncate(candidate, max)}`
    }
  }
  const keys = Object.keys(record)
  if (keys.length === 0) return '{}'
  return truncate(JSON.stringify(record), max)
}

// mergeUsage 累加两次用量（用于会话累计）。
export function addUsage(
  base: {
    inputTokens: number
    outputTokens: number
    cacheReadInputTokens: number
    cacheCreationInputTokens: number
  },
  delta: {
    inputTokens: number
    outputTokens: number
    cacheReadInputTokens: number
    cacheCreationInputTokens: number
  },
) {
  return {
    inputTokens: base.inputTokens + delta.inputTokens,
    outputTokens: base.outputTokens + delta.outputTokens,
    cacheReadInputTokens: base.cacheReadInputTokens + (delta.cacheReadInputTokens ?? 0),
    cacheCreationInputTokens:
      base.cacheCreationInputTokens + (delta.cacheCreationInputTokens ?? 0),
  }
}

// emptyUsage 返回零值用量。
export function emptyUsage() {
  return {
    inputTokens: 0,
    outputTokens: 0,
    cacheReadInputTokens: 0,
    cacheCreationInputTokens: 0,
  }
}
