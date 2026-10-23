import { useCallback, useEffect, useState } from 'react'
import {
  CancelSubagentTask,
  ListSubagentTasks,
  ListSubagentTypes,
} from '../../wailsjs/go/app/App'
import type { app } from '../../wailsjs/go/models'
import { formatRelative, formatTokens } from '../lib/format'
import { errorMessage } from '../stores/workbench'

// SubagentSettings 在设置页展示子 Agent 类型与后台任务，并支持取消运行中的任务。

// subagent.TaskStatus 取值：running / completed / failed / canceled。
const TASK_STATUS: Record<string, { label: string; dot: string; chip: string }> = {
  running: {
    label: '运行中',
    dot: 'bg-sky-400',
    chip: 'border-line bg-raised text-muted',
  },
  completed: {
    label: '已完成',
    dot: 'bg-emerald-500',
    chip: 'border-success-line bg-success-soft text-success-text',
  },
  failed: {
    label: '失败',
    dot: 'bg-rose-500',
    chip: 'border-danger-line bg-danger-soft text-danger-text',
  },
  canceled: {
    label: '已取消',
    dot: 'bg-slate-400',
    chip: 'border-line bg-raised text-subtle',
  },
}

const TASK_STATUS_UNKNOWN = {
  label: '未知',
  dot: 'bg-slate-400',
  chip: 'border-line bg-raised text-subtle',
}

// parseTime 把后端 RFC3339 字符串转成毫秒；非法值返回 0（交给 formatRelative 忽略）。
function parseTime(value: string): number {
  if (!value) return 0
  const ms = Date.parse(value)
  return Number.isNaN(ms) ? 0 : ms
}

function RefreshIcon({ spinning = false }: { spinning?: boolean }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className={`size-3 shrink-0 ${spinning ? 'animate-spin' : ''}`}
      aria-hidden="true"
    >
      <path d="M21 12a9 9 0 1 1-2.6-6.4" />
      <path d="M21 3v6h-6" />
    </svg>
  )
}

function ErrorNote({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <div className="mb-3 flex items-start gap-2 rounded-lg border border-danger-line bg-danger-soft px-3 py-2 text-[11px] leading-relaxed text-danger-text">
      <span className="min-w-0 flex-1 whitespace-pre-wrap break-words">{message}</span>
      {onRetry && (
        <button
          type="button"
          onClick={onRetry}
          className="shrink-0 cursor-pointer rounded border border-danger-line px-1.5 py-0.5 transition hover:bg-hover"
        >
          重试
        </button>
      )}
    </div>
  )
}

function LoadingRow({ text }: { text: string }) {
  return <p className="py-10 text-center text-xs text-subtle">{text}</p>
}

function EmptyHint({ title, hint }: { title: string; hint: string }) {
  return (
    <div className="px-4 py-10 text-center">
      <p className="text-xs text-muted">{title}</p>
      <p className="mt-1 text-[11px] leading-relaxed text-subtle">{hint}</p>
    </div>
  )
}

interface SubagentTaskRowProps {
  task: app.SubagentTaskDTO
  cancelling: boolean
  cancelError?: string
  onCancel: (id: string) => void
}

function SubagentTaskRow({ task, cancelling, cancelError, onCancel }: SubagentTaskRowProps) {
  const status = TASK_STATUS[task.status] ?? TASK_STATUS_UNKNOWN
  const startedAt = parseTime(task.startedAt)
  return (
    <li className="rounded-lg border border-line bg-surface px-3 py-2">
      <div className="flex items-start gap-2">
        <span
          aria-hidden="true"
          className={`mt-1.5 size-1.5 shrink-0 rounded-full ${status.dot} ${
            task.status === 'running' ? 'animate-pulse' : ''
          }`}
        />
        <div className="min-w-0 flex-1">
          <div className="flex min-w-0 flex-wrap items-center gap-1.5">
            <span className="min-w-0 flex-1 truncate text-xs text-fg">
              {task.description || '(无描述)'}
            </span>
            <span
              className={`shrink-0 rounded-full border px-1.5 py-0.5 text-[10px] ${status.chip}`}
            >
              {status.label}
            </span>
          </div>
          <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[10px] text-subtle">
            {task.agentType && <span className="font-mono">{task.agentType}</span>}
            {task.background && <span className="rounded bg-raised px-1">后台</span>}
            <span>{task.turns} 轮</span>
            <span className="tabular-nums">
              {formatTokens(task.inputTokens + task.outputTokens)} tokens
            </span>
            {startedAt > 0 && <span>{formatRelative(startedAt)}</span>}
          </div>
          {task.error && (
            <p className="mt-1 whitespace-pre-wrap break-words text-[10px] text-danger-text">
              {task.error}
            </p>
          )}
          {cancelError && (
            <p className="mt-1 whitespace-pre-wrap break-words text-[10px] text-danger-text">
              取消失败：{cancelError}
            </p>
          )}
        </div>
        {task.status === 'running' && (
          <button
            type="button"
            disabled={cancelling}
            onClick={() => onCancel(task.id)}
            className="shrink-0 cursor-pointer rounded-md border border-line bg-raised px-2 py-1 text-[10px] text-muted transition hover:border-danger-line hover:bg-danger-soft hover:text-danger-text disabled:cursor-not-allowed disabled:opacity-50"
          >
            {cancelling ? '取消中…' : '取消'}
          </button>
        )}
      </div>
    </li>
  )
}

export default function SubagentSettings() {
  const [types, setTypes] = useState<app.SubagentTypeDTO[]>([])
  const [tasks, setTasks] = useState<app.SubagentTaskDTO[]>([])
  const [loaded, setLoaded] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [cancelling, setCancelling] = useState<ReadonlySet<string>>(new Set())
  const [cancelErrors, setCancelErrors] = useState<Record<string, string>>({})

  const load = useCallback(async (isAlive: () => boolean = () => true) => {
    setBusy(true)
    const [typeResult, taskResult] = await Promise.allSettled([
      ListSubagentTypes(),
      ListSubagentTasks(),
    ])
    if (!isAlive()) return

    const problems: string[] = []
    if (typeResult.status === 'fulfilled') {
      setTypes(typeResult.value)
    } else {
      problems.push(`类型列表：${errorMessage(typeResult.reason)}`)
    }
    if (taskResult.status === 'fulfilled') {
      setTasks(taskResult.value)
    } else {
      problems.push(`任务列表：${errorMessage(taskResult.reason)}`)
    }
    setError(problems.length > 0 ? problems.join('\n') : null)
    setLoaded(true)
    setBusy(false)
  }, [])

  useEffect(() => {
    let alive = true
    void load(() => alive)
    return () => {
      alive = false
    }
  }, [load])

  const cancel = useCallback(async (id: string) => {
    setCancelling((current) => new Set(current).add(id))
    setCancelErrors((current) => {
      if (!(id in current)) return current
      const next = { ...current }
      delete next[id]
      return next
    })
    try {
      await CancelSubagentTask(id)
      // 取消成功后重新拉取任务，让状态与结束时间以服务端为准。
      const list = await ListSubagentTasks()
      setTasks(list)
    } catch (err) {
      setCancelErrors((current) => ({ ...current, [id]: errorMessage(err) }))
    } finally {
      setCancelling((current) => {
        const next = new Set(current)
        next.delete(id)
        return next
      })
    }
  }, [])

  return (
    <section role="tabpanel" aria-label="子 Agent" data-testid="settings-subagents">
      <div className="flex items-center justify-between gap-3">
        <p className="text-[11px] text-subtle">
          {types.length} 个类型 · {tasks.length} 个任务
        </p>
        <button
          type="button"
          disabled={busy}
          onClick={() => void load()}
          className="flex shrink-0 cursor-pointer items-center gap-1 rounded-md border border-line bg-raised px-2 py-1 text-[11px] text-muted transition hover:bg-hover hover:text-fg disabled:cursor-not-allowed disabled:opacity-50"
        >
          <RefreshIcon spinning={busy} />
          {busy ? '刷新中…' : '刷新'}
        </button>
      </div>

      <div className="mt-3 space-y-5">
        {error && <ErrorNote message={error} onRetry={() => void load()} />}
        {!loaded ? (
          <LoadingRow text="正在读取子 Agent…" />
        ) : (
          <>
            <section>
              <h3 className="text-[10px] font-medium uppercase tracking-wider text-subtle">
                可用类型
              </h3>
              {types.length === 0 ? (
                <EmptyHint
                  title="没有子 Agent 类型"
                  hint="在 ~/.b-desktop/agents 下添加 *.md 定义"
                />
              ) : (
                <ul className="mt-2 space-y-1.5">
                  {types.map((type) => (
                    <li
                      key={type.name}
                      className="rounded-lg border border-line bg-surface px-3 py-2"
                    >
                      <p className="font-mono text-xs font-medium text-fg">{type.name}</p>
                      <p className="mt-0.5 text-[11px] leading-relaxed text-muted">
                        {type.description || '无描述'}
                      </p>
                      {type.path && (
                        <p className="mt-1 break-all font-mono text-[10px] text-subtle">
                          {type.path}
                        </p>
                      )}
                    </li>
                  ))}
                </ul>
              )}
            </section>

            <section>
              <h3 className="flex items-center gap-1.5 text-[10px] font-medium uppercase tracking-wider text-subtle">
                后台任务
                <span className="tabular-nums normal-case">{tasks.length}</span>
              </h3>
              {tasks.length === 0 ? (
                <EmptyHint
                  title="还没有子 Agent 任务"
                  hint="父 Agent 通过 Agent 工具派发任务后会出现在这里"
                />
              ) : (
                <ul className="mt-2 space-y-1.5">
                  {tasks.map((task) => (
                    <SubagentTaskRow
                      key={task.id}
                      task={task}
                      cancelling={cancelling.has(task.id)}
                      cancelError={cancelErrors[task.id]}
                      onCancel={(id) => void cancel(id)}
                    />
                  ))}
                </ul>
              )}
            </section>
          </>
        )}
      </div>
    </section>
  )
}
