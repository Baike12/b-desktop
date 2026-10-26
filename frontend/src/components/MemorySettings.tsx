import { useCallback, useEffect, useState } from 'react'
import {
  DeleteMemory,
  GetProviderConfig,
  ListMemories,
  ReadMemory,
  SaveMemory,
  SetProviderConfig,
  UpdateMemory,
} from '../../wailsjs/go/app/App'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import type { app } from '../../wailsjs/go/models'
import { errorMessage, useWorkbenchStore } from '../stores/workbench'

// memory:changed 事件载荷与后端 MemoryChangedDTO 一致。
// 它只作为事件负载存在、不出现在绑定方法签名里，所以 wailsjs 不会生成它。
interface MemoryChangedPayload {
  sessionId?: string
  saved?: number
  updated?: number
  deleted?: number
  error?: string
}

// MemorySettings 在设置页管理长期记忆：用户级与项目级两个作用域、
// 正文查看编辑、软删，以及轮末自动抽取开关。
// 变更来自两条路：本页操作，或 Agent 在轮末自动抽取后推送的
// "memory:changed" 事件（两种都会触发刷新）。

const MEMORY_CHANGED_EVENT = 'memory:changed'

const MEMORY_TYPES = ['user', 'feedback', 'project', 'reference'] as const

const TYPE_LABEL: Record<string, string> = {
  user: '用户',
  feedback: '反馈',
  project: '项目',
  reference: '参考',
}

interface FormState {
  fileName: string | null
  name: string
  description: string
  type: string
  keywords: string
  importance: number
  content: string
}

const EMPTY_FORM: FormState = {
  fileName: null,
  name: '',
  description: '',
  type: 'project',
  keywords: '',
  importance: 5,
  content: '',
}

const fieldClass =
  'w-full rounded-lg border border-line bg-surface px-3 py-2 text-[13px] text-fg outline-none transition placeholder:text-subtle focus:border-accent disabled:opacity-60'

const smallButton =
  'cursor-pointer rounded-md border border-line px-2 py-1 text-[11px] text-muted transition hover:border-line-strong hover:bg-hover hover:text-fg'

const scopeTab = (active: boolean) =>
  `cursor-pointer rounded-full px-3 py-1 text-[11px] font-medium transition ${
    active
      ? 'bg-accent text-on-accent shadow-card'
      : 'border border-line text-muted hover:border-line-strong hover:bg-hover hover:text-fg'
  }`

// formatTime 把 Unix 毫秒格式化为本地日期；0 返回空串。
function formatTime(ms: number): string {
  if (!ms) return ''
  return new Date(ms).toLocaleString()
}

export default function MemorySettings() {
  const workDir = useWorkbenchStore((state) => state.workDir)
  const [scope, setScope] = useState<'user' | 'project'>('user')
  const [memories, setMemories] = useState<app.MemoryDTO[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [form, setForm] = useState<FormState | null>(null)
  const [saving, setSaving] = useState(false)
  const [busyName, setBusyName] = useState<string | null>(null)
  const [autoExtract, setAutoExtract] = useState<boolean | null>(null)

  const scopeWorkDir = scope === 'project' ? workDir : ''

  const refresh = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setMemories(await ListMemories(scope, scopeWorkDir))
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setLoading(false)
    }
  }, [scope, scopeWorkDir])

  useEffect(() => {
    void refresh()
  }, [refresh])

  // 订阅轮末自动抽取的变更事件：刷新列表并提示结果。
  useEffect(() => {
    const unsubscribe = EventsOn(MEMORY_CHANGED_EVENT, (...data: unknown[]) => {
      const payload = (data[0] ?? {}) as MemoryChangedPayload
      const parts: string[] = []
      if (payload.saved) parts.push(`保存 ${payload.saved} 条`)
      if (payload.updated) parts.push(`更新 ${payload.updated} 条`)
      if (payload.deleted) parts.push(`删除 ${payload.deleted} 条`)
      if (payload.error) {
        setNotice(`自动抽取失败：${payload.error}`)
      } else if (parts.length > 0) {
        setNotice(`Agent 自动${parts.join('、')}记忆`)
      }
      void refresh()
    })
    return () => {
      unsubscribe()
    }
  }, [refresh])

  // 自动抽取开关来自模型配置；本页做 Get→Set 往返，只改这一个字段。
  useEffect(() => {
    let cancelled = false
    void (async () => {
      try {
        const config = await GetProviderConfig()
        if (!cancelled) setAutoExtract(config.memoryAutoExtract)
      } catch {
        if (!cancelled) setAutoExtract(null)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [])

  const toggleAutoExtract = async () => {
    if (autoExtract === null) return
    const next = !autoExtract
    setError(null)
    try {
      const config = await GetProviderConfig()
      await SetProviderConfig({
        provider: config.provider,
        baseUrl: config.baseUrl,
        model: config.model,
        contextWindow: config.contextWindow,
        maxOutputTokens: config.maxOutputTokens,
        thinkingEnabled: config.thinkingEnabled,
        thinkingBudget: config.thinkingBudget,
        memoryAutoExtract: next,
      })
      setAutoExtract(next)
    } catch (err) {
      setError(errorMessage(err))
    }
  }

  const startCreate = () => {
    setForm({ ...EMPTY_FORM, type: scope === 'user' ? 'user' : 'project' })
    setError(null)
  }

  const startEdit = async (mem: app.MemoryDTO) => {
    setError(null)
    setBusyName(mem.fileName)
    try {
      const detail = await ReadMemory(scope, scopeWorkDir, mem.fileName)
      setForm({
        fileName: detail.fileName,
        name: detail.name,
        description: detail.description,
        type: detail.type,
        keywords: (detail.keywords ?? []).join(', '),
        importance: detail.importance || 5,
        content: detail.body,
      })
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusyName(null)
    }
  }

  const save = async () => {
    if (!form || saving) return
    const name = form.name.trim()
    if (name === '') {
      setError('记忆名不能为空')
      return
    }
    if (form.content.trim() === '') {
      setError('记忆正文不能为空')
      return
    }
    setSaving(true)
    setError(null)
    const keywords = form.keywords
      .split(/[,，]/)
      .map((item) => item.trim())
      .filter(Boolean)
    try {
      if (form.fileName === null) {
        await SaveMemory({
          scope,
          workDir: scopeWorkDir,
          name,
          description: form.description.trim(),
          type: form.type,
          keywords,
          importance: form.importance,
          content: form.content,
        })
      } else {
        await UpdateMemory({
          scope,
          workDir: scopeWorkDir,
          fileName: form.fileName,
          name,
          description: form.description.trim(),
          type: form.type,
          keywords,
          importance: form.importance,
          content: form.content,
        })
      }
      setForm(null)
      await refresh()
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  const remove = async (mem: app.MemoryDTO) => {
    if (busyName) return
    if (!window.confirm(`删除记忆「${mem.name}」？删除后会归档保留，可从文件恢复。`)) return
    setBusyName(mem.fileName)
    setError(null)
    try {
      await DeleteMemory(scope, scopeWorkDir, mem.fileName)
      if (form?.fileName === mem.fileName) setForm(null)
      await refresh()
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusyName(null)
    }
  }

  return (
    <section role="tabpanel" aria-label="记忆" data-testid="settings-memory">
      <div className="flex items-center justify-between">
        <div>
          <h3 className="font-mono text-[10px] font-medium uppercase tracking-[0.16em] text-subtle">记忆</h3>
          <p className="mt-1 text-[11px] text-subtle">
            存放于 ~/.b-desktop/memory；每轮对话开始时注入记忆索引与相关记忆，Agent 也会在轮末自动沉淀重要事实。
          </p>
        </div>
        <button
          type="button"
          data-testid="memory-add"
          onClick={startCreate}
          className="shrink-0 cursor-pointer rounded-full bg-accent px-3 py-1 text-[11px] font-medium text-on-accent shadow-card transition hover:bg-accent-hover active:translate-y-px"
        >
          新建记忆
        </button>
      </div>

      <div className="mt-3 flex items-center justify-between gap-3 rounded-xl border border-line/80 bg-panel px-3.5 py-2.5">
        <div>
          <p className="text-xs font-medium text-fg">轮末自动抽取</p>
          <p className="mt-0.5 text-[11px] text-subtle">对话结束后由模型判断是否保存、合并或删除记忆（限频，不阻塞对话）。</p>
        </div>
        <button
          type="button"
          role="switch"
          aria-checked={autoExtract === true}
          data-testid="memory-auto-extract"
          disabled={autoExtract === null}
          onClick={() => void toggleAutoExtract()}
          className={`relative h-5 w-9 shrink-0 cursor-pointer rounded-full transition disabled:opacity-50 ${
            autoExtract ? 'bg-accent' : 'bg-line-strong'
          }`}
        >
          <span
            className={`absolute top-0.5 h-4 w-4 rounded-full bg-surface shadow-card transition-all ${
              autoExtract ? 'left-[18px]' : 'left-0.5'
            }`}
          />
        </button>
      </div>

      <div className="mt-3 flex items-center gap-1.5">
        <button type="button" data-testid="memory-scope-user" onClick={() => setScope('user')} className={scopeTab(scope === 'user')}>
          用户级
        </button>
        <button
          type="button"
          data-testid="memory-scope-project"
          onClick={() => setScope('project')}
          disabled={!workDir}
          className={`${scopeTab(scope === 'project')} disabled:cursor-not-allowed disabled:opacity-50`}
          title={workDir ? workDir : '当前没有打开的工作目录'}
        >
          项目级{scope === 'project' && workDir ? ` · ${workDir.split('/').pop()}` : ''}
        </button>
      </div>

      {notice && (
        <p data-testid="memory-notice" className="mt-3 rounded-lg border border-line bg-surface px-3 py-2 text-xs text-muted">
          {notice}
        </p>
      )}
      {error && (
        <p data-testid="memory-error" className="mt-3 rounded-lg border border-danger-line bg-danger-soft px-3 py-2 text-xs text-danger-text">
          {error}
        </p>
      )}

      {form && (
        <div data-testid="memory-form" className="mt-3 space-y-2 rounded-xl border border-line/80 bg-panel p-3.5">
          <div className="grid gap-2 sm:grid-cols-2">
            <label className="block">
              <span className="text-[11px] text-muted">名称</span>
              <input
                data-testid="memory-name"
                value={form.name}
                onChange={(event) => setForm({ ...form, name: event.target.value })}
                placeholder="提交前确认"
                className={`mt-1 ${fieldClass}`}
              />
            </label>
            <label className="block">
              <span className="text-[11px] text-muted">类型</span>
              <select
                data-testid="memory-type"
                value={form.type}
                onChange={(event) => setForm({ ...form, type: event.target.value })}
                className={`mt-1 ${fieldClass}`}
              >
                {MEMORY_TYPES.map((type) => (
                  <option key={type} value={type}>
                    {TYPE_LABEL[type]}（{type}）
                  </option>
                ))}
              </select>
            </label>
          </div>
          <label className="block">
            <span className="text-[11px] text-muted">描述（用于索引与召回）</span>
            <input
              data-testid="memory-description"
              value={form.description}
              onChange={(event) => setForm({ ...form, description: event.target.value })}
              placeholder="一句话说明这条记忆的内容"
              className={`mt-1 ${fieldClass}`}
            />
          </label>
          <div className="grid gap-2 sm:grid-cols-2">
            <label className="block">
              <span className="text-[11px] text-muted">关键词（逗号分隔）</span>
              <input
                data-testid="memory-keywords"
                value={form.keywords}
                onChange={(event) => setForm({ ...form, keywords: event.target.value })}
                placeholder="git, 提交"
                className={`mt-1 ${fieldClass}`}
              />
            </label>
            <label className="block">
              <span className="text-[11px] text-muted">重要性（1-10）</span>
              <input
                data-testid="memory-importance"
                type="number"
                min={1}
                max={10}
                value={form.importance}
                onChange={(event) => setForm({ ...form, importance: Number(event.target.value) || 0 })}
                className={`mt-1 ${fieldClass}`}
              />
            </label>
          </div>
          <label className="block">
            <span className="text-[11px] text-muted">正文</span>
            <textarea
              data-testid="memory-content"
              value={form.content}
              onChange={(event) => setForm({ ...form, content: event.target.value })}
              rows={6}
              placeholder="自包含的一条事实，脱离对话也能读懂。"
              className={`mt-1 resize-y font-mono text-xs ${fieldClass}`}
            />
          </label>
          <div className="flex justify-end gap-2 pt-1">
            <button
              type="button"
              onClick={() => {
                setForm(null)
                setError(null)
              }}
              className={smallButton}
            >
              取消
            </button>
            <button
              type="button"
              data-testid="memory-save"
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
        {loading && memories.length === 0 && <p className="text-xs text-subtle">正在读取记忆…</p>}
        {!loading && memories.length === 0 && !form && (
          <div className="rounded-xl border border-dashed border-line-strong/70 px-4 py-8 text-center">
            <p className="text-sm text-muted">还没有{scope === 'user' ? '用户级' : '项目级'}记忆</p>
            <p className="mt-1 text-[11px] text-subtle">直接新建，或在对话里让 Agent 记住某件事</p>
          </div>
        )}
        {memories.map((mem) => (
          <div
            key={`${mem.scope}:${mem.fileName}`}
            data-testid={`memory-item-${mem.fileName}`}
            className="flex items-start gap-3 rounded-xl border border-line bg-panel p-3"
          >
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-2">
                <span className="truncate text-sm font-medium text-fg">{mem.name}</span>
                <span className="rounded-full border border-line px-1.5 py-0.5 text-[10px] text-subtle">
                  {TYPE_LABEL[mem.type] ?? mem.type}
                </span>
                {mem.importance > 0 && (
                  <span className="rounded-full border border-line px-1.5 py-0.5 font-mono text-[10px] text-subtle">
                    P{mem.importance}
                  </span>
                )}
              </div>
              <p className="mt-1 text-xs text-muted">{mem.description || '（无描述）'}</p>
              <p className="mt-1 font-mono text-[10px] text-subtle">
                {mem.fileName} · {formatTime(mem.updatedAtMs)}
              </p>
            </div>
            <div className="flex shrink-0 gap-1.5">
              <button
                type="button"
                disabled={busyName === mem.fileName}
                onClick={() => void startEdit(mem)}
                className={`${smallButton} disabled:opacity-50`}
              >
                编辑
              </button>
              <button
                type="button"
                disabled={busyName === mem.fileName}
                onClick={() => void remove(mem)}
                className={`${smallButton} text-danger-text disabled:opacity-50`}
              >
                删除
              </button>
            </div>
          </div>
        ))}
      </div>
    </section>
  )
}
