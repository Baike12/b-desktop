import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { CreateTask, DeleteTask, ListTasks, SetTaskEnabled } from '../../wailsjs/go/app/App'
import type { app } from '../../wailsjs/go/models'

// formatUntil 是面向未来的相对时间（下次运行），与 formatRelative 的“过去”语义相反。
function formatUntil(ms: number): string {
  if (!ms) return ''
  const diff = ms - Date.now()
  if (diff <= 0) return '即将运行'
  if (diff < 60_000) return `${Math.ceil(diff / 1000)} 秒后`
  if (diff < 3_600_000) return `${Math.ceil(diff / 60_000)} 分钟后`
  if (diff < 86_400_000) return `${Math.ceil(diff / 3_600_000)} 小时后`
  return `${Math.ceil(diff / 86_400_000)} 天后`
}

function formatWhen(ms: number): string {
  if (!ms) return '—'
  const date = new Date(ms)
  const pad = (value: number) => String(value).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`
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

export interface TaskPanelProps {
  className?: string
  // defaultWorkDir 作为新建任务的默认工作目录（会话当前项目）。
  defaultWorkDir?: string
  onChanged?: () => void
}

// TaskPanel 管理定时任务：列表、新建、启停与删除，并显示下次运行时间。
export default function TaskPanel({
  className = '',
  defaultWorkDir = '',
  onChanged,
}: TaskPanelProps) {
  const [tasks, setTasks] = useState<app.TaskDTO[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [busyId, setBusyId] = useState<string | null>(null)
  const [confirmId, setConfirmId] = useState<string | null>(null)

  const [cron, setCron] = useState('0 9 * * *')
  const [prompt, setPrompt] = useState('')
  const [workDir, setWorkDir] = useState(defaultWorkDir)
  const [creating, setCreating] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const value = await ListTasks()
      setTasks(value ?? [])
      setError(null)
    } catch (err) {
      setError(errorText(err))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const handleCreate = async (event: FormEvent) => {
    event.preventDefault()
    const trimmedCron = cron.trim()
    const trimmedPrompt = prompt.trim()
    if (!trimmedCron || !trimmedPrompt || creating) return
    setCreating(true)
    try {
      const trimmedDir = workDir.trim()
      const created = await CreateTask({
        cron: trimmedCron,
        prompt: trimmedPrompt,
        workDir: trimmedDir || undefined,
      })
      setTasks((current) => [...current, created])
      setPrompt('')
      setError(null)
      onChanged?.()
    } catch (err) {
      setError(errorText(err))
    } finally {
      setCreating(false)
    }
  }

  const handleToggle = async (task: app.TaskDTO) => {
    if (busyId) return
    setBusyId(task.id)
    setConfirmId(null)
    try {
      await SetTaskEnabled(task.id, !task.enabled)
      setTasks((current) =>
        current.map((item) =>
          item.id === task.id ? { ...item, enabled: !task.enabled } : item,
        ),
      )
      setError(null)
      onChanged?.()
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusyId(null)
    }
  }

  const handleDelete = async (task: app.TaskDTO) => {
    if (busyId) return
    if (confirmId !== task.id) {
      setConfirmId(task.id)
      return
    }
    setBusyId(task.id)
    setConfirmId(null)
    try {
      await DeleteTask(task.id)
      setTasks((current) => current.filter((item) => item.id !== task.id))
      setError(null)
      onChanged?.()
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusyId(null)
    }
  }

  return (
    <div className={`flex h-full min-h-0 flex-col bg-panel ${className}`}>
      <div className="flex items-center gap-2 border-b border-line px-3 py-2">
        <span className="text-xs font-medium text-fg">定时任务</span>
        <span className="text-[10px] text-subtle">{tasks.length} 个</span>
        <button
          type="button"
          onClick={() => void load()}
          disabled={loading}
          className="ml-auto cursor-pointer rounded-md border border-line px-2 py-1 text-[11px] text-muted hover:bg-hover disabled:cursor-not-allowed disabled:text-subtle"
        >
          {loading ? '刷新中…' : '刷新'}
        </button>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto px-3 py-2">
        {error && (
          <p className="mb-2 rounded-md bg-danger-soft px-2 py-1.5 text-[11px] text-danger-text">
            {error}
          </p>
        )}

        {!loading && tasks.length === 0 && (
          <p className="py-6 text-center text-xs text-subtle">还没有定时任务</p>
        )}

        <ul className="space-y-2">
          {tasks.map((task) => {
            const nextMs = task.nextRunAtMs ?? 0
            const lastMs = task.lastRunAtMs ?? 0
            const busy = busyId === task.id
            return (
              <li
                key={task.id}
                className="rounded-lg border border-line bg-raised px-2.5 py-2"
              >
                <div className="flex items-start gap-2">
                  <span
                    className={`mt-1 size-1.5 shrink-0 rounded-full ${
                      task.enabled ? 'bg-emerald-500' : 'bg-slate-400'
                    }`}
                  />
                  <div className="min-w-0 flex-1">
                    <p className="line-clamp-2 whitespace-pre-wrap break-words text-xs text-fg">
                      {task.prompt}
                    </p>
                    <p className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-[10px] text-subtle">
                      <code className="rounded bg-code px-1 py-0.5 font-mono text-muted">
                        {task.cron}
                      </code>
                      {task.workDir && (
                        <span className="max-w-full truncate font-mono">{task.workDir}</span>
                      )}
                      <span className={task.enabled ? 'text-success-text' : 'text-subtle'}>
                        {task.enabled ? '已启用' : '已暂停'}
                      </span>
                    </p>
                    <p className="mt-1 flex flex-wrap items-center gap-x-2 text-[10px] text-subtle">
                      <span>
                        下次运行：
                        <span className={task.enabled ? 'text-muted' : 'text-subtle'}>
                          {task.enabled ? formatWhen(nextMs) : '—'}
                        </span>
                      </span>
                      {task.enabled && nextMs > 0 && (
                        <span className="text-accent-text">{formatUntil(nextMs)}</span>
                      )}
                      {lastMs > 0 && <span>上次：{formatWhen(lastMs)}</span>}
                    </p>
                  </div>
                </div>
                <div className="mt-2 flex items-center justify-end gap-2">
                  <button
                    type="button"
                    onClick={() => void handleToggle(task)}
                    disabled={busy}
                    className="cursor-pointer rounded border border-line px-2 py-0.5 text-[10px] text-muted hover:bg-hover disabled:cursor-not-allowed disabled:text-subtle"
                  >
                    {task.enabled ? '暂停' : '启用'}
                  </button>
                  <button
                    type="button"
                    onClick={() => void handleDelete(task)}
                    disabled={busy}
                    className={`cursor-pointer rounded border px-2 py-0.5 text-[10px] disabled:cursor-not-allowed disabled:text-subtle ${
                      confirmId === task.id
                        ? 'border-danger-text bg-danger-soft text-danger-text'
                        : 'border-line text-muted hover:bg-hover'
                    }`}
                  >
                    {confirmId === task.id ? '确认删除' : '删除'}
                  </button>
                </div>
              </li>
            )
          })}
        </ul>
      </div>

      <form onSubmit={handleCreate} className="space-y-2 border-t border-line px-3 py-2">
        <p className="text-[10px] uppercase tracking-wide text-subtle">新建任务</p>
        <div className="flex items-center gap-2">
          <label className="w-20 shrink-0 text-[11px] text-muted" htmlFor="task-cron">
            Cron
          </label>
          <input
            id="task-cron"
            value={cron}
            onChange={(event) => setCron(event.target.value)}
            placeholder="0 9 * * *"
            className="min-w-0 flex-1 rounded-md border border-line bg-surface px-2 py-1 font-mono text-[11px] text-fg outline-none placeholder:text-subtle focus:border-accent"
          />
        </div>
        <div className="flex items-start gap-2">
          <label className="mt-1 w-20 shrink-0 text-[11px] text-muted" htmlFor="task-prompt">
            提示词
          </label>
          <textarea
            id="task-prompt"
            value={prompt}
            onChange={(event) => setPrompt(event.target.value)}
            rows={2}
            placeholder="每次触发时发送给 Agent 的内容"
            className="min-w-0 flex-1 resize-y rounded-md border border-line bg-surface px-2 py-1 text-[11px] text-fg outline-none placeholder:text-subtle focus:border-accent"
          />
        </div>
        <div className="flex items-center gap-2">
          <label className="w-20 shrink-0 text-[11px] text-muted" htmlFor="task-workdir">
            工作目录
          </label>
          <input
            id="task-workdir"
            value={workDir}
            onChange={(event) => setWorkDir(event.target.value)}
            placeholder="留空使用默认项目"
            className="min-w-0 flex-1 rounded-md border border-line bg-surface px-2 py-1 font-mono text-[11px] text-fg outline-none placeholder:text-subtle focus:border-accent"
          />
        </div>
        <div className="flex items-center justify-between gap-2">
          <p className="text-[10px] text-subtle">Cron 由后端解析，示例：0 9 * * *</p>
          <button
            type="submit"
            disabled={creating || !cron.trim() || !prompt.trim()}
            className="cursor-pointer rounded-md bg-accent px-3 py-1 text-[11px] font-medium text-on-accent hover:bg-accent-hover disabled:cursor-not-allowed disabled:bg-hover disabled:text-subtle"
          >
            {creating ? '创建中…' : '创建'}
          </button>
        </div>
      </form>
    </div>
  )
}
