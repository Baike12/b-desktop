import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { createPortal } from 'react-dom'
import {
  CreateProjectWorktree,
  ListProjectWorktrees,
  RemoveProjectWorktree,
} from '../../wailsjs/go/app/App'
import type { app } from '../../wailsjs/go/models'
import { errorMessage } from '../stores/workbench'

// MENU_WIDTH 与侧边栏项目行右侧对齐；视图宽度不足时向左收。
const MENU_WIDTH = 320

export interface WorktreeMenuAnchor {
  top: number
  bottom: number
  right: number
}

interface WorktreeMenuProps {
  project: app.ProjectDTO
  // anchor 是项目行 worktree 按钮的视口矩形，菜单据此定位。
  anchor: WorktreeMenuAnchor
  sessions: app.SessionInfoDTO[]
  // onStartSession 在指定工作树路径下新建会话。
  onStartSession: (workDir: string) => void
  onClose: () => void
}

const NAME_ADJECTIVES = [
  'swift',
  'quiet',
  'bright',
  'lunar',
  'silent',
  'amber',
  'crimson',
  'gentle',
  'rapid',
  'vivid',
  'silver',
  'north',
] as const

const NAME_NOUNS = [
  'otter',
  'harbor',
  'falcon',
  'cedar',
  'meadow',
  'comet',
  'summit',
  'river',
  'willow',
  'beacon',
  'canyon',
  'ember',
] as const

// defaultBranch 生成可读的默认分支名，打开菜单后无需输入即可创建。
function defaultBranch(): string {
  const pick = (list: readonly string[]) => list[Math.floor(Math.random() * list.length)]
  return `worktree/${pick(NAME_ADJECTIVES)}-${pick(NAME_NOUNS)}`
}

function menuLayout(anchor: WorktreeMenuAnchor): {
  left: number
  top?: number
  bottom?: number
  maxHeight: number
} {
  const margin = 8
  const left = Math.min(
    Math.max(margin, anchor.right - MENU_WIDTH),
    Math.max(margin, window.innerWidth - MENU_WIDTH - margin),
  )
  const spaceBelow = window.innerHeight - anchor.bottom - margin
  const spaceAbove = anchor.top - margin
  const maxHeight = Math.max(200, Math.min(520, Math.max(spaceBelow, spaceAbove)))
  if (spaceBelow >= 260 || spaceBelow >= spaceAbove) {
    return { left, top: anchor.bottom + 6, maxHeight }
  }
  return { left, bottom: window.innerHeight - anchor.top + 6, maxHeight }
}

// WorktreeMenu 是项目行 worktree 按钮弹出的菜单：
// 列出该项目仓库的工作树，并在底部直接创建新的工作树。
// 创建成功后立刻在对应工作树里新建会话，与 Zed 的 “Create New Worktree” 行为一致。
export default function WorktreeMenu({
  project,
  anchor,
  sessions,
  onStartSession,
  onClose,
}: WorktreeMenuProps) {
  const [data, setData] = useState<app.ProjectWorktreesDTO | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [branch, setBranch] = useState(defaultBranch)
  const [base, setBase] = useState('')
  const [creating, setCreating] = useState(false)
  const [busyPath, setBusyPath] = useState<string | null>(null)
  const [confirmPath, setConfirmPath] = useState<string | null>(null)

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const value = await ListProjectWorktrees(project.path)
      setData(value)
      setError(null)
    } catch (err) {
      setData(null)
      setError(errorMessage(err))
    } finally {
      setLoading(false)
    }
  }, [project.path])

  useEffect(() => {
    void load()
  }, [load])

  const handleCreate = async (event: FormEvent) => {
    event.preventDefault()
    const trimmed = branch.trim()
    if (!trimmed || creating) return
    setCreating(true)
    setError(null)
    try {
      const created = await CreateProjectWorktree(project.path, trimmed, base.trim())
      onStartSession(created.path)
      onClose()
    } catch (err) {
      setError(errorMessage(err))
      setCreating(false)
    }
  }

  const handleRemove = async (worktree: app.WorktreeDTO) => {
    if (busyPath) return
    if (confirmPath !== worktree.path) {
      setConfirmPath(worktree.path)
      return
    }
    setConfirmPath(null)
    setBusyPath(worktree.path)
    try {
      await RemoveProjectWorktree(project.path, worktree.path)
      await load()
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusyPath(null)
    }
  }

  const layout = menuLayout(anchor)
  const worktrees = data?.worktrees ?? []
  const previewPath =
    data && branch.trim() ? `${data.worktreesRoot}/${branch.trim()}` : data?.worktreesRoot ?? ''

  const startSession = (workDir: string) => {
    onStartSession(workDir)
    onClose()
  }

  return createPortal(
    <>
      <div className="fixed inset-0 z-40" onClick={onClose} aria-hidden="true" />
      <section
        role="dialog"
        aria-label={`${project.name} 的工作树`}
        data-testid="worktree-menu"
        style={{ left: layout.left, top: layout.top, bottom: layout.bottom, maxHeight: layout.maxHeight, width: MENU_WIDTH }}
        className="fixed z-50 flex flex-col overflow-hidden border border-line-strong bg-raised"
      >
        <header className="flex shrink-0 items-start gap-2 border-b border-line px-3 py-2">
          <div className="min-w-0 flex-1">
            <p className="text-xs font-medium text-fg">工作树</p>
            <p className="mt-0.5 truncate text-[10px] text-subtle" title={project.path}>
              {project.name} · {project.path}
            </p>
          </div>
          <button
            type="button"
            aria-label="关闭工作树菜单"
            title="关闭"
            onClick={onClose}
            className="-mr-1 cursor-pointer px-1.5 text-sm text-subtle transition hover:bg-hover hover:text-fg"
          >
            ×
          </button>
        </header>

        <div className="min-h-0 flex-1 overflow-y-auto px-2 py-2">
          <p className="px-1 pb-1 font-mono text-[10px] font-medium uppercase tracking-[0.16em] text-subtle">
            已有工作树
          </p>

          {error && (
            <p
              role="alert"
              className="mb-1 whitespace-pre-wrap break-words border border-danger-line bg-danger-soft px-2 py-1.5 text-[11px] text-danger-text"
            >
              {error}
            </p>
          )}

          {loading && worktrees.length === 0 && (
            <p className="px-1 py-3 text-[11px] text-subtle">正在读取工作树…</p>
          )}

          <ul data-testid="worktree-list" className="space-y-1">
            {worktrees.map((worktree) => {
              const busy = busyPath === worktree.path
              const worktreeSessions = sessions.filter(
                (session) => session.workDir === worktree.path,
              )
              const running = worktreeSessions.some((session) => session.running)
              const removable = !worktree.main && worktree.managed
              return (
                <li
                  key={worktree.path}
                  data-testid="worktree-item"
                  data-managed={worktree.managed ? 'true' : 'false'}
                  className="border border-line bg-panel px-2 py-1.5"
                >
                  <div className="flex items-center gap-1.5">
                    <button
                      type="button"
                      onClick={() => startSession(worktree.path)}
                      title={`在 ${worktree.path} 中新建会话`}
                      className="min-w-0 flex-1 cursor-pointer text-left"
                    >
                      <span className="flex items-center gap-1.5">
                        <span className="min-w-0 truncate font-mono text-[11px] text-fg">
                          {worktree.branch || '(detached)'}
                        </span>
                        {worktree.main && (
                          <span className="shrink-0 border border-accent/40 bg-accent-soft px-1.5 py-0.5 font-mono text-[10px] text-accent-text">
                            主
                          </span>
                        )}
                        {worktree.managed && (
                          <span className="shrink-0 border border-success-line bg-success-soft px-1.5 py-0.5 font-mono text-[10px] text-success-text">
                            托管
                          </span>
                        )}
                        {worktree.detached && (
                          <span className="shrink-0 border border-warn-line bg-warn-soft px-1.5 py-0.5 font-mono text-[10px] text-warn-text">
                            detached
                          </span>
                        )}
                        {worktree.locked && (
                          <span className="shrink-0 border border-line bg-raised px-1.5 py-0.5 font-mono text-[10px] text-muted">
                            锁定
                          </span>
                        )}
                      </span>
                      <span
                        className="mt-0.5 block truncate font-mono text-[10px] text-subtle"
                        title={worktree.path}
                      >
                        {worktree.path}
                      </span>
                    </button>

                    {removable && (
                      <button
                        type="button"
                        onClick={() => void handleRemove(worktree)}
                        disabled={busy || running}
                        title={running ? '该工作树有运行中的会话' : '移除工作树，保留分支'}
                        className={`shrink-0 cursor-pointer border px-1.5 py-0.5 text-[10px] disabled:cursor-not-allowed disabled:text-subtle ${ confirmPath === worktree.path ? 'border-danger-text bg-danger-soft text-danger-text' : 'border-line text-muted hover:bg-hover' }`}
                      >
                        {confirmPath === worktree.path ? '确认' : '移除'}
                      </button>
                    )}
                  </div>

                  {confirmPath === worktree.path && (
                    <p className="mt-1 text-[10px] leading-relaxed text-warn-text">
                      移除后目录会被删除，分支保留。
                      {worktreeSessions.length > 0 &&
                        `该工作树有 ${worktreeSessions.length} 个会话。`}
                      {running && '其中存在运行中的会话，请先停止。'}
                    </p>
                  )}
                </li>
              )
            })}
          </ul>
        </div>

        <form
          onSubmit={handleCreate}
          className="shrink-0 space-y-2 border-t border-line bg-panel px-3 py-2.5"
        >
          <p className="font-mono text-[10px] font-medium uppercase tracking-[0.16em] text-subtle">新建工作树</p>
          <label className="flex items-center gap-2">
            <span className="w-8 shrink-0 text-[11px] text-muted">分支</span>
            <input
              autoFocus
              data-testid="worktree-branch"
              value={branch}
              onChange={(event) => setBranch(event.target.value)}
              onFocus={(event) => event.currentTarget.select()}
              placeholder="feature/xxx"
              className="min-w-0 flex-1 border border-line bg-surface px-2 py-1 font-mono text-[11px] text-fg outline-none placeholder:text-subtle focus:border-accent"
            />
          </label>
          <label className="flex items-center gap-2">
            <span className="w-8 shrink-0 text-[11px] text-muted">基线</span>
            <input
              data-testid="worktree-base"
              value={base}
              onChange={(event) => setBase(event.target.value)}
              placeholder="留空使用当前 HEAD"
              className="min-w-0 flex-1 border border-line bg-surface px-2 py-1 font-mono text-[11px] text-fg outline-none placeholder:text-subtle focus:border-accent"
            />
          </label>
          <div className="flex items-center justify-between gap-2">
            <p className="min-w-0 flex-1 truncate text-[10px] text-subtle" title={previewPath}>
              {previewPath ? `位置 ${previewPath}` : '读取仓库后显示创建位置'}
            </p>
            <button
              type="submit"
              data-testid="worktree-create"
              disabled={creating || !branch.trim()}
              className="shrink-0 cursor-pointer bg-accent px-3 py-1 text-[11px] font-medium text-on-accent hover:bg-accent-hover disabled:cursor-not-allowed disabled:bg-hover disabled:text-subtle"
            >
              {creating ? '创建中…' : '创建并打开'}
            </button>
          </div>
        </form>
      </section>
    </>,
    document.body,
  )
}
