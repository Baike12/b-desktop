import { useEffect, useState } from 'react'
import { GetUsageSummary, ListRequestTraces } from '../../wailsjs/go/app/App'
import type { app } from '../../wailsjs/go/models'
import { formatTokens } from '../lib/format'
import { errorMessage } from '../stores/workbench'

const RANGE_OPTIONS = [7, 14, 30, 90] as const

interface UsagePanelProps {
  // defaultDays 是首次展示的统计区间，必须是 RANGE_OPTIONS 之一。
  defaultDays?: number
  // traceLimit 是「最近请求」返回的最大条数。
  traceLimit?: number
  // showHeader 控制是否渲染面板标题；嵌入设置页时由外层标题代替。
  showHeader?: boolean
  className?: string
}

// formatDuration 把毫秒转成可读耗时。
function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '—'
  if (ms < 1000) return `${Math.round(ms)}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`
  const minutes = Math.floor(ms / 60_000)
  const seconds = Math.round((ms % 60_000) / 1000)
  return `${minutes}m${seconds}s`
}

// formatTimestamp 返回 MM-DD HH:MM:SS。
function formatTimestamp(ms: number): string {
  if (!ms) return '—'
  const date = new Date(ms)
  const pad = (value: number) => String(value).padStart(2, '0')
  return `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(
    date.getMinutes(),
  )}:${pad(date.getSeconds())}`
}

function StatCard({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="border border-line bg-panel px-3 py-2">
      <div className="font-mono text-[10px] uppercase tracking-[0.14em] text-subtle">{label}</div>
      <div className="mt-1 font-mono text-[15px] font-semibold tabular-nums text-fg">{value}</div>
      {hint && <div className="mt-0.5 text-[10px] text-subtle">{hint}</div>}
    </div>
  )
}

// UsagePanel 展示用量统计：按天与按模型聚合，外加最近模型请求。
export default function UsagePanel({
  defaultDays = 7,
  traceLimit = 20,
  showHeader = true,
  className = '',
}: UsagePanelProps) {
  const [days, setDays] = useState(defaultDays)
  const [summary, setSummary] = useState<app.UsageSummaryDTO | null>(null)
  const [traces, setTraces] = useState<app.RequestTraceDTO[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [reloadToken, setReloadToken] = useState(0)

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    void (async () => {
      try {
        const [nextSummary, nextTraces] = await Promise.all([
          GetUsageSummary(days),
          ListRequestTraces(traceLimit),
        ])
        if (cancelled) return
        setSummary(nextSummary)
        setTraces(nextTraces)
      } catch (err) {
        if (!cancelled) setError(errorMessage(err))
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [days, traceLimit, reloadToken])

  // 派生数据全部在渲染期计算：每日柱宽按区间内最大 token 归一。
  const dailyRows = summary?.days ?? []
  const modelRows = summary?.models ?? []
  const maxDailyTokens = dailyRows.reduce((max, day) => Math.max(max, day.totalTokens), 0)

  return (
    <section
      data-testid="usage-panel"
      className={`flex flex-col gap-4 border border-line bg-surface p-4 ${className}`}
    >
      <header className="flex flex-wrap items-center justify-between gap-2">
        {showHeader ? (
          <div>
            <h2 className="text-[15px] font-semibold tracking-[-0.015em] text-fg">用量统计</h2>
            <p className="mt-0.5 font-mono text-[10px] text-subtle">
              {summary
                ? `${summary.from} ~ ${summary.to} · 数据来自 ~/.b-desktop/usage.jsonl`
                : '读取本地模型调用记录'}
            </p>
          </div>
        ) : (
          <p className="font-mono text-[10px] text-subtle">
            {summary
              ? `${summary.from} ~ ${summary.to} · 数据来自 ~/.b-desktop/usage.jsonl`
              : '读取本地模型调用记录'}
          </p>
        )}
        <div className="ml-auto flex items-center gap-1">
          {RANGE_OPTIONS.map((option) => (
            <button
              key={option}
              type="button"
              data-testid={`usage-range-${option}`}
              aria-pressed={option === days}
              onClick={() => setDays(option)}
              className={`cursor-pointer border px-2 py-1 font-mono text-[11px] transition ${ option === days ? 'border-accent bg-accent-soft text-accent-text' : 'border-line text-muted hover:border-line-strong hover:bg-hover' }`}
            >
              {option} 天
            </button>
          ))}
          <button
            type="button"
            data-testid="usage-refresh"
            onClick={() => setReloadToken((value) => value + 1)}
            disabled={loading}
            className="ml-1 cursor-pointer border border-line px-2 py-1 text-[11px] text-muted transition hover:border-line-strong hover:bg-hover disabled:cursor-not-allowed disabled:opacity-50"
          >
            {loading ? '刷新中…' : '刷新'}
          </button>
        </div>
      </header>

      {error && (
        <p
          role="alert"
          className="border border-danger-line bg-danger-soft px-3 py-2 text-[11px] text-danger-text"
        >
          {error}
        </p>
      )}

      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        <StatCard label="请求" value={String(summary?.requestCount ?? 0)} />
        <StatCard label="总 tokens" value={formatTokens(summary?.totalTokens ?? 0)} />
        <StatCard
          label="错误"
          value={String(summary?.errorCount ?? 0)}
          hint={summary && summary.errorCount > 0 ? '含上游失败与中止' : undefined}
        />
        <StatCard label="总耗时" value={formatDuration(summary?.durationMs ?? 0)} />
      </div>

      <div className="grid gap-2 sm:grid-cols-3">
        <StatCard label="输入" value={formatTokens(summary?.inputTokens ?? 0)} />
        <StatCard label="输出" value={formatTokens(summary?.outputTokens ?? 0)} />
        <StatCard
          label="缓存读 / 写"
          value={`${formatTokens(summary?.cacheReadInputTokens ?? 0)} / ${formatTokens(
            summary?.cacheCreationInputTokens ?? 0,
          )}`}
        />
      </div>

      <div>
        <div className="flex items-center justify-between">
          <h3 className="font-mono text-[10px] font-medium uppercase tracking-[0.16em] text-fg">
            按天
          </h3>
          <span className="font-mono text-[10px] text-subtle">{dailyRows.length} 天有记录</span>
        </div>
        {dailyRows.length === 0 ? (
          <p data-testid="usage-days-empty" className="mt-2 text-[11px] text-subtle">
            所选区间内没有模型请求。
          </p>
        ) : (
          <ul data-testid="usage-days" className="mt-2 space-y-1">
            {dailyRows.map((day) => {
              const ratio = maxDailyTokens > 0 ? day.totalTokens / maxDailyTokens : 0
              return (
                <li key={day.date} className="flex items-center gap-2 font-mono text-[11px]">
                  <span className="w-14 shrink-0 tabular-nums text-muted">{day.date.slice(5)}</span>
                  <span className="h-2 min-w-0 flex-1 overflow-hidden border border-line bg-raised">
                    <span
                      className="block h-full bg-accent"
                      style={{ width: `${Math.max(ratio * 100, day.totalTokens > 0 ? 2 : 0)}%` }}
                    />
                  </span>
                  <span className="w-16 shrink-0 text-right tabular-nums text-muted">
                    {formatTokens(day.totalTokens)}
                  </span>
                  <span className="w-14 shrink-0 text-right tabular-nums text-subtle">
                    {day.requestCount} 次
                  </span>
                  <span
                    className={`w-12 shrink-0 text-right tabular-nums ${ day.errorCount > 0 ? 'text-danger-text' : 'text-subtle' }`}
                  >
                    {day.errorCount > 0 ? `${day.errorCount} 错` : '—'}
                  </span>
                </li>
              )
            })}
          </ul>
        )}
      </div>

      <div>
        <h3 className="font-mono text-[10px] font-medium uppercase tracking-[0.16em] text-fg">
          按模型
        </h3>
        {modelRows.length === 0 ? (
          <p data-testid="usage-models-empty" className="mt-2 text-[11px] text-subtle">
            暂无模型统计。
          </p>
        ) : (
          <div className="mt-2 overflow-x-auto">
            <table data-testid="usage-models" className="w-full border-collapse font-mono text-[11px]">
              <thead>
                <tr>
                  <th className="border-b border-line px-2 py-1.5 text-left font-medium text-subtle">
                    模型
                  </th>
                  <th className="border-b border-line px-2 py-1.5 text-right font-medium text-subtle">
                    请求
                  </th>
                  <th className="border-b border-line px-2 py-1.5 text-right font-medium text-subtle">
                    错误
                  </th>
                  <th className="border-b border-line px-2 py-1.5 text-right font-medium text-subtle">
                    输入
                  </th>
                  <th className="border-b border-line px-2 py-1.5 text-right font-medium text-subtle">
                    输出
                  </th>
                  <th className="border-b border-line px-2 py-1.5 text-right font-medium text-subtle">
                    缓存读
                  </th>
                  <th className="border-b border-line px-2 py-1.5 text-right font-medium text-subtle">
                    总计
                  </th>
                </tr>
              </thead>
              <tbody>
                {modelRows.map((model) => (
                  <tr key={model.model}>
                    <td className="max-w-52 truncate border-b border-line px-2 py-1.5 font-mono text-fg">
                      {model.model || '未知模型'}
                    </td>
                    <td className="border-b border-line px-2 py-1.5 text-right tabular-nums text-muted">
                      {model.requestCount}
                    </td>
                    <td
                      className={`border-b border-line px-2 py-1.5 text-right tabular-nums ${ model.errorCount > 0 ? 'text-danger-text' : 'text-muted' }`}
                    >
                      {model.errorCount}
                    </td>
                    <td className="border-b border-line px-2 py-1.5 text-right tabular-nums text-muted">
                      {formatTokens(model.inputTokens)}
                    </td>
                    <td className="border-b border-line px-2 py-1.5 text-right tabular-nums text-muted">
                      {formatTokens(model.outputTokens)}
                    </td>
                    <td className="border-b border-line px-2 py-1.5 text-right tabular-nums text-muted">
                      {formatTokens(model.cacheReadInputTokens)}
                    </td>
                    <td className="border-b border-line px-2 py-1.5 text-right tabular-nums text-fg">
                      {formatTokens(model.totalTokens)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      <div>
        <div className="flex items-center justify-between">
          <h3 className="font-mono text-[10px] font-medium uppercase tracking-[0.16em] text-fg">
            最近请求
          </h3>
          <span className="font-mono text-[10px] text-subtle">最多 {traceLimit} 条</span>
        </div>
        {traces.length === 0 ? (
          <p data-testid="usage-traces-empty" className="mt-2 text-[11px] text-subtle">
            暂无请求记录。
          </p>
        ) : (
          <div className="mt-2 overflow-x-auto">
            <table data-testid="usage-traces" className="w-full border-collapse font-mono text-[11px]">
              <thead>
                <tr>
                  <th className="border-b border-line px-2 py-1.5 text-left font-medium text-subtle">
                    时间
                  </th>
                  <th className="border-b border-line px-2 py-1.5 text-left font-medium text-subtle">
                    模型
                  </th>
                  <th className="border-b border-line px-2 py-1.5 text-right font-medium text-subtle">
                    输入
                  </th>
                  <th className="border-b border-line px-2 py-1.5 text-right font-medium text-subtle">
                    输出
                  </th>
                  <th className="border-b border-line px-2 py-1.5 text-right font-medium text-subtle">
                    总计
                  </th>
                  <th className="border-b border-line px-2 py-1.5 text-right font-medium text-subtle">
                    耗时
                  </th>
                  <th className="border-b border-line px-2 py-1.5 text-left font-medium text-subtle">
                    结果
                  </th>
                </tr>
              </thead>
              <tbody>
                {traces.map((trace, index) => (
                  <tr key={`${trace.atMs}-${trace.sessionId}-${index}`}>
                    <td className="whitespace-nowrap border-b border-line px-2 py-1.5 tabular-nums text-muted">
                      {formatTimestamp(trace.atMs)}
                    </td>
                    <td className="max-w-48 truncate border-b border-line px-2 py-1.5 font-mono text-fg">
                      {trace.model || '—'}
                    </td>
                    <td className="border-b border-line px-2 py-1.5 text-right tabular-nums text-muted">
                      {formatTokens(trace.inputTokens)}
                    </td>
                    <td className="border-b border-line px-2 py-1.5 text-right tabular-nums text-muted">
                      {formatTokens(trace.outputTokens)}
                    </td>
                    <td className="border-b border-line px-2 py-1.5 text-right tabular-nums text-fg">
                      {formatTokens(trace.totalTokens)}
                    </td>
                    <td className="border-b border-line px-2 py-1.5 text-right tabular-nums text-muted">
                      {formatDuration(trace.durationMs)}
                    </td>
                    <td
                      className={`max-w-56 truncate border-b border-line px-2 py-1.5 ${ trace.err ? 'text-danger-text' : 'text-subtle' }`}
                      title={trace.err || trace.stopReason}
                    >
                      {trace.err || trace.stopReason || '—'}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </section>
  )
}
