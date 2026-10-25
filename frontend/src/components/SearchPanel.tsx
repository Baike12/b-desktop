import {
  useDeferredValue,
  useEffect,
  useMemo,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
} from 'react'
import { SearchSessions } from '../../wailsjs/go/app/App'
import type { app } from '../../wailsjs/go/models'
import { formatRelative } from '../lib/format'

const ESCAPE_RE = /[.*+?^${}()|[\]\\]/g

// toMs 兼容 Go time 的 JSON 形态（RFC3339 字符串）与毫秒数。
export function toMs(value: unknown): number {
  if (typeof value === 'number') return Number.isFinite(value) ? value : 0
  if (value instanceof Date) return value.getTime()
  if (typeof value === 'string') {
    const parsed = Date.parse(value)
    return Number.isNaN(parsed) ? 0 : parsed
  }
  return 0
}

function formatWhen(ms: number): string {
  if (!ms) return ''
  const date = new Date(ms)
  const pad = (value: number) => String(value).padStart(2, '0')
  return `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`
}

function errorText(err: unknown): string {
  if (err instanceof Error) return err.message
  if (typeof err === 'string') return err
  try {
    return JSON.stringify(err)
  } catch {
    return String(err)
  }
}

// highlight 在片段里标出关键词，正则元字符已转义。
function highlight(text: string, needle: string): ReactNode[] {
  const trimmed = needle.trim()
  if (!trimmed) return [text]
  const pattern = new RegExp(`(${trimmed.replace(ESCAPE_RE, '\\$&')})`, 'gi')
  const lower = trimmed.toLowerCase()
  return text.split(pattern).map((part, index) =>
    part.toLowerCase() === lower ? (
      <mark key={index} className="rounded-sm bg-accent-soft px-0.5 text-accent-text">
        {part}
      </mark>
    ) : (
      <span key={index}>{part}</span>
    ),
  )
}

const ROLE_LABEL: Record<string, string> = {
  user: '用户',
  assistant: '助手',
  tool: '工具',
  system: '系统',
}

export interface SearchPanelProps {
  className?: string
  // limit 为每次搜索的最大会话数；后端 <=0 时按 50 处理，上限 200。
  limit?: number
  initialQuery?: string
  onOpenSession?: (sessionId: string) => void
}

// SearchPanel 搜索全部会话存档，结果按会话分组展示标题、片段与时间。
export default function SearchPanel({
  className = '',
  limit = 50,
  initialQuery = '',
  onOpenSession,
}: SearchPanelProps) {
  const [query, setQuery] = useState(initialQuery)
  const [nonce, setNonce] = useState(0)
  const [results, setResults] = useState<app.SessionSearchResultDTO[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const seqRef = useRef(0)
  const deferred = useDeferredValue(query)

  useEffect(() => {
    const trimmed = deferred.trim()
    seqRef.current += 1
    const seq = seqRef.current
    if (!trimmed) {
      setResults([])
      setLoading(false)
      setError(null)
      return
    }
    setLoading(true)
    const timer = window.setTimeout(() => {
      SearchSessions(trimmed, limit)
        .then((value) => {
          if (seq !== seqRef.current) return
          setResults(value ?? [])
          setError(null)
        })
        .catch((err: unknown) => {
          if (seq !== seqRef.current) return
          setResults([])
          setError(errorText(err))
        })
        .finally(() => {
          if (seq === seqRef.current) setLoading(false)
        })
    }, 220)
    return () => window.clearTimeout(timer)
  }, [deferred, limit, nonce])

  const totalMatches = useMemo(
    () => results.reduce((sum, result) => sum + result.matchCount, 0),
    [results],
  )

  const handleSubmit = (event: FormEvent) => {
    event.preventDefault()
    setNonce((value) => value + 1)
  }

  return (
    <div className={`flex h-full min-h-0 flex-col bg-panel ${className}`}>
      <form onSubmit={handleSubmit} className="border-b border-line px-3 py-2">
        <div className="flex items-center gap-2">
          <input
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="搜索会话内容、工具名…"
            aria-label="搜索会话"
            className="min-w-0 flex-1 rounded-lg border border-line bg-surface px-3 py-1.5 text-xs text-fg outline-none transition placeholder:text-subtle focus:border-accent"
          />
          <button
            type="submit"
            disabled={loading}
            className="shrink-0 cursor-pointer rounded-lg border border-line px-3 py-1.5 text-xs text-fg transition hover:bg-hover disabled:cursor-not-allowed disabled:text-subtle"
          >
            {loading ? '搜索中…' : '搜索'}
          </button>
        </div>
        <p className="mt-1.5 text-[10px] text-subtle">
          {results.length > 0
            ? `${results.length} 个会话 · ${totalMatches} 处匹配`
            : '按会话分组显示匹配片段'}
        </p>
      </form>

      <div className="min-h-0 flex-1 overflow-y-auto px-3 py-2">
        {error && (
          <p className="rounded-lg border border-danger-line bg-danger-soft px-2.5 py-1.5 text-[11px] text-danger-text">
            {error}
          </p>
        )}
        {!error && !loading && deferred.trim() === '' && (
          <p className="py-8 text-center text-xs text-subtle">输入关键词搜索全部会话</p>
        )}
        {!error && !loading && deferred.trim() !== '' && results.length === 0 && (
          <p className="py-8 text-center text-xs text-subtle">没有匹配结果</p>
        )}

        <div className="space-y-2">
          {results.map((result) => {
            const updatedMs = toMs(result.updatedAt)
            return (
              <section
                key={result.sessionId}
                className="overflow-hidden rounded-xl border border-line bg-raised shadow-card"
              >
                <button
                  type="button"
                  onClick={() => onOpenSession?.(result.sessionId)}
                  className="flex w-full cursor-pointer items-baseline gap-2 px-2.5 py-2 text-left hover:bg-hover"
                >
                  <span className="min-w-0 flex-1 truncate text-xs font-medium text-fg">
                    {result.title || result.sessionId}
                  </span>
                  <span className="shrink-0 rounded-full border border-accent/40 bg-accent-soft px-1.5 py-0.5 font-mono text-[10px] text-accent-text">
                    {result.matchCount}
                  </span>
                  <time
                    className="shrink-0 text-[10px] text-subtle"
                    dateTime={updatedMs ? new Date(updatedMs).toISOString() : undefined}
                    title={formatWhen(updatedMs)}
                  >
                    {formatRelative(updatedMs)}
                  </time>
                </button>
                {result.workDir && (
                  <p className="truncate border-t border-line px-2.5 py-1 font-mono text-[10px] text-subtle">
                    {result.workDir}
                  </p>
                )}
                <ul className="divide-y divide-line border-t border-line">
                  {result.matches.map((hit) => {
                    const hitMs = toMs(hit.time)
                    return (
                      <li
                        key={`${hit.role}:${hit.line}:${hitMs}`}
                        className="px-2.5 py-1.5 hover:bg-hover"
                      >
                        <div className="mb-0.5 flex items-center gap-1.5 text-[10px] text-subtle">
                          <span className="rounded-sm border border-line bg-raised px-1 font-mono text-[10px] text-muted">
                            {ROLE_LABEL[hit.role] ?? hit.role}
                          </span>
                          {hit.toolName && (
                            <span className="font-mono text-accent-text">{hit.toolName}</span>
                          )}
                          <span className="font-mono">L{hit.line}</span>
                          {hitMs > 0 && (
                            <time className="ml-auto" dateTime={new Date(hitMs).toISOString()}>
                              {formatWhen(hitMs)}
                            </time>
                          )}
                        </div>
                        <p className="whitespace-pre-wrap break-words font-mono text-[11px] leading-4 text-muted">
                          {highlight(hit.snippet, deferred)}
                        </p>
                      </li>
                    )
                  })}
                </ul>
              </section>
            )
          })}
        </div>
      </div>
    </div>
  )
}
