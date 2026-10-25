import { useCallback, useEffect, useState } from 'react'
import { DeleteHook, ListHooks, ReloadHooks, SaveHook } from '../../wailsjs/go/app/App'
import type { app } from '../../wailsjs/go/models'
import { errorMessage, useWorkbenchStore } from '../stores/workbench'

// HookSettings 在设置页管理事件钩子：用户级写入 ~/.b-desktop/hooks.json，
// 项目级写入 <工作目录>/.b-desktop/hooks.json。命令从 stdin 收到事件 JSON。

interface FormState {
  event: string
  matcher: string
  command: string
  scope: string
}

const EMPTY_FORM: FormState = {
  event: 'PreToolUse',
  matcher: '',
  command: '',
  scope: 'user',
}

const EVENTS: readonly { id: string; label: string; hint: string }[] = [
  { id: 'PreToolUse', label: 'PreToolUse', hint: '工具执行前；退出码 2 或输出 deny 会阻止调用' },
  { id: 'PostToolUse', label: 'PostToolUse', hint: '工具执行后；可做格式化、通知或审计' },
  { id: 'UserPromptSubmit', label: 'UserPromptSubmit', hint: '用户提交输入后；stdout 会追加到上下文' },
  { id: 'SessionStart', label: 'SessionStart', hint: '会话开始或恢复时执行一次' },
]

const SOURCE_LABEL: Record<string, string> = {
  user: '用户级',
  project: '项目级',
}

const fieldClass =
  'w-full rounded-lg border border-line bg-surface px-3 py-2 text-[13px] text-fg outline-none transition placeholder:text-subtle focus:border-accent'

const smallButton =
  'cursor-pointer rounded-md border border-line px-2 py-1 text-[11px] text-muted transition hover:border-line-strong hover:bg-hover hover:text-fg'

function eventHint(event: string): string {
  return EVENTS.find((item) => item.id === event)?.hint ?? ''
}

export default function HookSettings() {
  const workDir = useWorkbenchStore((state) => state.workDir)
  const [hooks, setHooks] = useState<app.HookDTO[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [form, setForm] = useState<FormState | null>(null)
  const [original, setOriginal] = useState<app.HookDTO | null>(null)
  const [saving, setSaving] = useState(false)
  const [busyKey, setBusyKey] = useState<string | null>(null)

  const refresh = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setHooks(await ListHooks(workDir))
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setLoading(false)
    }
  }, [workDir])

  useEffect(() => {
    void refresh()
  }, [refresh])

  const startCreate = () => {
    setOriginal(null)
    setForm({ ...EMPTY_FORM })
    setError(null)
  }

  const startEdit = (hook: app.HookDTO) => {
    setOriginal(hook)
    setForm({
      event: hook.event,
      matcher: hook.matcher ?? '',
      command: hook.command,
      scope: hook.source,
    })
    setError(null)
  }

  const save = async () => {
    if (!form || saving) return
    const command = form.command.trim()
    if (command === '') {
      setError('命令不能为空')
      return
    }
    setSaving(true)
    setError(null)
    try {
      const next = await SaveHook(workDir, {
        event: form.event,
        matcher: form.matcher.trim(),
        command,
        scope: form.scope,
        originalEvent: original?.event ?? '',
        originalMatcher: original?.matcher ?? '',
        originalCommand: original?.command ?? '',
        originalScope: original?.source ?? '',
      })
      setHooks(next)
      setForm(null)
      setOriginal(null)
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  const remove = async (hook: app.HookDTO) => {
    const key = `${hook.source}:${hook.event}:${hook.matcher}:${hook.command}`
    if (busyKey) return
    if (!window.confirm(`删除 ${SOURCE_LABEL[hook.source] ?? hook.source} 的 ${hook.event} 钩子？`)) return
    setBusyKey(key)
    setError(null)
    try {
      const next = await DeleteHook(workDir, hook.source, hook.event, hook.matcher ?? '', hook.command)
      setHooks(next)
      if (original && original.event === hook.event && original.command === hook.command) {
        setForm(null)
        setOriginal(null)
      }
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusyKey(null)
    }
  }

  const reloadAll = async () => {
    setError(null)
    try {
      setHooks(await ReloadHooks(workDir))
    } catch (err) {
      setError(errorMessage(err))
    }
  }

  return (
    <section role="tabpanel" aria-label="钩子" data-testid="settings-hooks">
      <div className="flex items-center justify-between">
        <div>
          <h3 className="font-mono text-[10px] font-medium uppercase tracking-[0.16em] text-subtle">钩子</h3>
          <p className="mt-1 text-[11px] text-subtle">
            用户级 ~/.b-desktop/hooks.json；项目级 {workDir || '默认工作目录'}/.b-desktop/hooks.json。
          </p>
        </div>
        <div className="flex shrink-0 gap-1.5">
          <button type="button" onClick={() => void reloadAll()} className={smallButton}>
            重新加载
          </button>
          <button
            type="button"
            data-testid="hook-add"
            onClick={startCreate}
            className="cursor-pointer rounded-full bg-accent px-3 py-1 text-[11px] font-medium text-on-accent shadow-card transition hover:bg-accent-hover active:translate-y-px"
          >
            新建钩子
          </button>
        </div>
      </div>

      {error && (
        <p data-testid="hook-error" className="mt-3 rounded-lg border border-danger-line bg-danger-soft px-3 py-2 text-xs text-danger-text">
          {error}
        </p>
      )}

      {form && (
        <div data-testid="hook-form" className="mt-3 space-y-2 rounded-xl border border-line/80 bg-panel p-3.5">
          <div className="grid gap-2 sm:grid-cols-2">
            <label className="block">
              <span className="text-[11px] text-muted">事件</span>
              <select
                data-testid="hook-event"
                value={form.event}
                onChange={(event) => setForm({ ...form, event: event.target.value })}
                className={`mt-1 ${fieldClass}`}
              >
                {EVENTS.map((item) => (
                  <option key={item.id} value={item.id}>
                    {item.label}
                  </option>
                ))}
              </select>
            </label>
            <label className="block">
              <span className="text-[11px] text-muted">作用域</span>
              <select
                data-testid="hook-scope"
                value={form.scope}
                onChange={(event) => setForm({ ...form, scope: event.target.value })}
                className={`mt-1 ${fieldClass}`}
              >
                <option value="user">用户级（所有项目）</option>
                <option value="project">项目级（当前工作目录）</option>
              </select>
            </label>
          </div>
          <p className="text-[11px] text-subtle">{eventHint(form.event)}</p>
          <label className="block">
            <span className="text-[11px] text-muted">工具匹配（空或 * 表示全部，支持 | 分隔与正则）</span>
            <input
              data-testid="hook-matcher"
              value={form.matcher}
              onChange={(event) => setForm({ ...form, matcher: event.target.value })}
              placeholder="Bash"
              className={`mt-1 ${fieldClass}`}
            />
          </label>
          <label className="block">
            <span className="text-[11px] text-muted">命令（交给系统 shell，事件 JSON 从 stdin 传入）</span>
            <textarea
              data-testid="hook-command"
              value={form.command}
              onChange={(event) => setForm({ ...form, command: event.target.value })}
              rows={3}
              placeholder="./scripts/check.sh"
              className={`mt-1 resize-y font-mono text-xs ${fieldClass}`}
            />
          </label>
          <div className="flex justify-end gap-2 pt-1">
            <button
              type="button"
              onClick={() => {
                setForm(null)
                setOriginal(null)
              }}
              className={smallButton}
            >
              取消
            </button>
            <button
              type="button"
              data-testid="hook-save"
              disabled={saving}
              onClick={() => void save()}
              className="cursor-pointer rounded-full bg-accent px-3.5 py-1 text-[11px] font-medium text-on-accent shadow-card transition hover:bg-accent-hover active:translate-y-px disabled:opacity-60 disabled:shadow-none"
            >
              {saving ? '保存中…' : '保存'}
            </button>
          </div>
        </div>
      )}

      <div className="mt-3 space-y-2">
        {loading && hooks.length === 0 && <p className="text-xs text-subtle">正在读取钩子…</p>}
        {!loading && hooks.length === 0 && !form && (
          <div className="rounded-xl border border-dashed border-line-strong/70 px-4 py-8 text-center">
            <p className="text-sm text-muted">尚未安装钩子</p>
            <p className="mt-1 text-[11px] text-subtle">新建钩子，以在任务生命周期事件中运行命令</p>
          </div>
        )}
        {hooks.map((hook) => {
          const key = `${hook.source}:${hook.event}:${hook.matcher}:${hook.command}`
          return (
            <div key={key} data-testid={`hook-item-${hook.event}`} className="rounded-xl border border-line bg-panel p-3">
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-sm font-medium text-fg">{hook.event}</span>
                <span className="rounded-full border border-line px-1.5 py-0.5 text-[10px] text-subtle">
                  {SOURCE_LABEL[hook.source] ?? hook.source}
                </span>
                {hook.matcher && (
                  <span className="rounded-full border border-line px-1.5 py-0.5 font-mono text-[10px] text-muted">
                    {hook.matcher}
                  </span>
                )}
                <div className="ml-auto flex gap-1.5">
                  <button type="button" onClick={() => startEdit(hook)} className={smallButton}>
                    编辑
                  </button>
                  <button
                    type="button"
                    disabled={busyKey === key}
                    onClick={() => void remove(hook)}
                    className={`${smallButton} text-danger-text disabled:opacity-50`}
                  >
                    删除
                  </button>
                </div>
              </div>
              <pre className="mt-1.5 overflow-x-auto whitespace-pre-wrap break-all font-mono text-[11px] text-muted">
                {hook.command}
              </pre>
            </div>
          )
        })}
      </div>
    </section>
  )
}
