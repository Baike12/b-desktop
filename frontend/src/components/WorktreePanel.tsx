import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import {
  CreateWorktree,
  ListWorktrees,
  RemoveWorktree,
} from '../../wailsjs/go/app/App'
import type { app } from '../../wailsjs/go/models'

function errorText(err: unknown): string {
  if (err instanceof Error) return err.message
  if (typeof err === 'string') return err
  try {
    return JSON.stringify(err)
  } catch {
    return String(err)
  }
}

export interface WorktreePanelProps {
  className?: string
  // defaultRepo 是仓库路径（通常为当前会话的 workDir）。
  defaultRepo?: string
  // onOpenWorktree 供页面把某个工作树切换为当前工作目录。
  onOpenWorktree?: (worktree: app.WorktreeDTO) => void
}

// WorktreePanel 列出仓库的 git 工作树，并支持按分支创建 / 移除。
export default function WorktreePanel({
  className = '',
  defaultRepo = '',
  onOpenWorktree,
}: WorktreePanelProps) {
  const [repo, setRepo] = useState(defaultRepo)
  const [worktrees, setWorktrees] = useState<app.WorktreeDTO[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [busyId, setBusyId] = useState<string | null>(null)
  const [confirmPath, setConfirmPath] = useState<string | null>(null)
  const [branch, setBranch] = useState('')
  const [base, setBase] = useState('')
  const [creating, setCreating] = useState(false)
  const seqRef = useRef(0)

  const load = useCallback(async (target: string) => {
    const trimmed = target.trim()
    seqRef.current += 1
    const seq = seqRef.current
    if (!trimmed) {
      setWorktrees([])
      setError(null)
      setLoading(false)
      return
    }
    setLoading(true)
    try {
      const value = await ListWorktrees(trimmed)
      if (seq !== seqRef.current) return
      setWorktrees(value ?? [])
      setError(null)
    } catch (err) {
      if (seq !== seqRef.current) return
      setWorktrees([])
      setError(errorText(err))
    } finally {
      if (seq === seqRef.current) setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load(defaultRepo)
  }, [defaultRepo, load])

  const handleLoad = (event: FormEvent) => {
    event.preventDefault()
    setConfirmPath(null)
    void load(repo)
  }

  const handleCreate = async (event: FormEvent) => {
    event.preventDefault()
    const trimmedRepo = repo.trim()
    const trimmedBranch = branch.trim()
    if (!trimmedRepo || !trimmedBranch || creating) return
    setCreating(true)
    try {
      await CreateWorktree(trimmedRepo, trimmedBranch, base.trim())
      setBranch('')
      setBase('')
      setError(null)
      await load(trimmedRepo)
    } catch (err) {
      setError(errorText(err))
    } finally {
      setCreating(false)
    }
  }

  const handleRemove = async (worktree: app.WorktreeDTO) => {
    const trimmedRepo = repo.trim()
    if (!trimmedRepo || busyId) return
    if (confirmPath !== worktree.path) {
      setConfirmPath(worktree.path)
      return
    }
    setConfirmPath(null)
    setBusyId(worktree.path)
    try {
      await RemoveWorktree(trimmedRepo, worktree.path)
      setWorktrees((current) => current.filter((item) => item.path !== worktree.path))
      setError(null)
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusyId(null)
    }
  }

  return (
    <div className={`flex h-full min-h-0 flex-col bg-panel ${className}`}>
      <form onSubmit={handleLoad} className="border-b border-line px-3 py-2">
        <div className="flex items-center gap-2">
          <input
            value={repo}
            onChange={(event) => setRepo(event.target.value)}
            placeholder="仓库路径"
            aria-label="仓库路径"
            className="min-w-0 flex-1 rounded-md border border-line bg-surface px-2 py-1 font-mono text-[11px] text-fg outline-none placeholder:text-subtle focus:border-accent"
          />
          <button
            type="submit"
            disabled={loading || !repo.trim()}
            className="shrink-0 cursor-pointer rounded-md border border-line px-2 py-1 text-[11px] text-muted hover:bg-hover disabled:cursor-not-allowed disabled:text-subtle"
          >
            {loading ? '加载中…' : '加载'}
          </button>
        </div>
      </form>

      <div className="min-h-0 flex-1 overflow-y-auto px-3 py-2">
        {error && (
          <p className="mb-2 rounded-md bg-danger-soft px-2 py-1.5 text-[11px] text-danger-text">
            {error}
          </p>
        )}

        {!loading && worktrees.length === 0 && !error && (
          <p className="py-6 text-center text-xs text-subtle">
            {repo.trim() ? '没有工作树或路径不是 git 仓库' : '填写仓库路径后加载工作树'}
          </p>
        )}

        <ul className="space-y-2">
          {worktrees.map((worktree) => {
            const busy = busyId === worktree.path
            return (
              <li
                key={worktree.path}
                className="rounded-lg border border-line bg-raised px-2.5 py-2"
              >
                <div className="flex items-center gap-2">
                  <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-fg">
                    {worktree.branch || '(detached)'}
                  </span>
                  {worktree.main && (
                    <span className="shrink-0 rounded-full bg-accent-soft px-1.5 py-0.5 text-[10px] text-accent-text">
                      主
                    </span>
                  )}
                  {worktree.detached && (
                    <span className="shrink-0 rounded-full bg-warn-soft px-1.5 py-0.5 text-[10px] text-warn-text">
                      detached
                    </span>
                  )}
                  {worktree.locked && (
                    <span className="shrink-0 rounded-full bg-hover px-1.5 py-0.5 text-[10px] text-muted">
                      锁定
                    </span>
                  )}
                  {worktree.prunable && (
                    <span className="shrink-0 rounded-full bg-warn-soft px-1.5 py-0.5 text-[10px] text-warn-text">
                      可清理
                    </span>
                  )}
                </div>
                <p className="mt-1 truncate font-mono text-[10px] text-subtle" title={worktree.path}>
                  {worktree.path}
                </p>
                <p className="mt-0.5 font-mono text-[10px] text-subtle">
                  HEAD {worktree.head.slice(0, 10) || '—'}
                </p>
                <div className="mt-2 flex items-center justify-end gap-2">
                  <button
                    type="button"
                    onClick={() => onOpenWorktree?.(worktree)}
                    className="cursor-pointer rounded border border-line px-2 py-0.5 text-[10px] text-muted hover:bg-hover"
                  >
                    打开
                  </button>
                  <button
                    type="button"
                    onClick={() => void handleRemove(worktree)}
                    disabled={busy || worktree.main}
                    title={worktree.main ? '主工作树不可删除' : undefined}
                    className={`cursor-pointer rounded border px-2 py-0.5 text-[10px] disabled:cursor-not-allowed disabled:text-subtle ${
                      confirmPath === worktree.path
                        ? 'border-danger-text bg-danger-soft text-danger-text'
                        : 'border-line text-muted hover:bg-hover'
                    }`}
                  >
                    {confirmPath === worktree.path ? '确认移除' : '移除'}
                  </button>
                </div>
              </li>
            )
          })}
        </ul>
      </div>

      <form onSubmit={handleCreate} className="space-y-2 border-t border-line px-3 py-2">
        <p className="text-[10px] uppercase tracking-wide text-subtle">新建工作树</p>
        <div className="flex items-center gap-2">
          <label className="w-14 shrink-0 text-[11px] text-muted" htmlFor="worktree-branch">
            分支
          </label>
          <input
            id="worktree-branch"
            value={branch}
            onChange={(event) => setBranch(event.target.value)}
            placeholder="feature/xxx"
            className="min-w-0 flex-1 rounded-md border border-line bg-surface px-2 py-1 font-mono text-[11px] text-fg outline-none placeholder:text-subtle focus:border-accent"
          />
        </div>
        <div className="flex items-center gap-2">
          <label className="w-14 shrink-0 text-[11px] text-muted" htmlFor="worktree-base">
            基线
          </label>
          <input
            id="worktree-base"
            value={base}
            onChange={(event) => setBase(event.target.value)}
            placeholder="留空使用 HEAD"
            className="min-w-0 flex-1 rounded-md border border-line bg-surface px-2 py-1 font-mono text-[11px] text-fg outline-none placeholder:text-subtle focus:border-accent"
          />
        </div>
        <div className="flex items-center justify-between gap-2">
          <p className="text-[10px] text-subtle">创建位置：&lt;repo&gt;/.b-desktop/worktrees/&lt;branch&gt;</p>
          <button
            type="submit"
            disabled={creating || !repo.trim() || !branch.trim()}
            className="shrink-0 cursor-pointer rounded-md bg-accent px-3 py-1 text-[11px] font-medium text-on-accent hover:bg-accent-hover disabled:cursor-not-allowed disabled:bg-hover disabled:text-subtle"
          >
            {creating ? '创建中…' : '创建'}
          </button>
        </div>
      </form>
    </div>
  )
}
