import { useCallback, useEffect, useMemo, useState } from 'react'

import type { app } from '../../wailsjs/go/models'
import { readFileDiff, usePreviewStore } from '../stores/preview'
import { useWorkbenchStore } from '../stores/workbench'
import ReviewDiff from './ReviewDiff'

// ChangesPanel 是「变更审阅」面板：列出工作区的 git 变更并展示单文件差异。
//
// 交互模型与编辑器一致：点文件展开差异，暂存/取消暂存在行尾就地完成，
// 放弃改动需要二次确认（不可撤销）。

// STATUS_BADGE 把变更类型映射成字母徽标。
const STATUS_BADGE: Record<string, string> = {
  added: 'A',
  modified: 'M',
  deleted: 'D',
  renamed: 'R',
  copied: 'C',
  untracked: 'U',
  conflicted: '!',
  typechange: 'T',
}

// STATUS_BADGE_CLASS 给徽标上色。
const STATUS_BADGE_CLASS: Record<string, string> = {
  added: 'bg-success-soft text-success-text',
  modified: 'bg-warn-soft text-warn-text',
  deleted: 'bg-danger-soft text-danger-text',
  renamed: 'bg-accent-soft text-accent-text',
  copied: 'bg-accent-soft text-accent-text',
  untracked: 'bg-raised text-muted',
  conflicted: 'bg-danger-soft text-danger-text',
  typechange: 'bg-warn-soft text-warn-text',
}

// STATUS_LABEL 是变更类型的中文名。
const STATUS_LABEL: Record<string, string> = {
  added: '新增',
  modified: '修改',
  deleted: '删除',
  renamed: '重命名',
  copied: '复制',
  untracked: '未跟踪',
  conflicted: '冲突',
  typechange: '类型变更',
}

interface ChangeRowProps {
  change: app.FileChangeDTO
  expanded: boolean
  onToggle: () => void
  onStage: () => void
  onUnstage: () => void
  onDiscard: () => void
  onOpenFile: () => void
  confirming: boolean
  onConfirmDiscard: () => void
}

function ChangeRow({
  change,
  expanded,
  onToggle,
  onStage,
  onUnstage,
  onDiscard,
  onOpenFile,
  confirming,
  onConfirmDiscard,
}: ChangeRowProps) {
  const badge = STATUS_BADGE[change.status] ?? 'M'
  const badgeClass = STATUS_BADGE_CLASS[change.status] ?? 'bg-raised text-muted'
  return (
    <div className="border-b border-line/60 last:border-b-0">
      <div className="flex items-center gap-1.5 px-2 py-1 hover:bg-hover">
        <button
          type="button"
          data-testid="change-row"
          data-path={change.path}
          onClick={onToggle}
          title={STATUS_LABEL[change.status] ?? change.status}
          className="flex min-w-0 flex-1 cursor-pointer items-center gap-1.5 text-left"
        >
          <span
            className={`flex size-4 shrink-0 items-center justify-center rounded text-[10px] font-medium ${badgeClass}`}
          >
            {badge}
          </span>
          <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-muted" title={change.path}>
            {change.path}
          </span>
          {!change.binary && (
            <>
              <span className="shrink-0 text-[10px] text-success-text">+{change.additions}</span>
              <span className="shrink-0 text-[10px] text-danger-text">-{change.deletions}</span>
            </>
          )}
          <svg
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="2"
            strokeLinecap="round"
            className={`size-3 shrink-0 text-subtle transition-transform ${expanded ? 'rotate-90' : ''}`}
            aria-hidden="true"
          >
            <path d="m9 18 6-6-6-6" />
          </svg>
        </button>

        <div className="flex shrink-0 items-center gap-0.5">
          <button
            type="button"
            data-testid="change-open-file"
            onClick={onOpenFile}
            title="打开文件"
            className="cursor-pointer rounded px-1.5 py-0.5 text-[10px] text-subtle transition hover:bg-hover hover:text-fg"
          >
            打开
          </button>
          {change.staged ? (
            <button
              type="button"
              data-testid="change-unstage"
              onClick={onUnstage}
              title="取消暂存"
              className="cursor-pointer rounded px-1.5 py-0.5 text-[10px] text-subtle transition hover:bg-hover hover:text-fg"
            >
              取消暂存
            </button>
          ) : (
            <button
              type="button"
              data-testid="change-stage"
              onClick={onStage}
              title="暂存这个文件的改动"
              className="cursor-pointer rounded px-1.5 py-0.5 text-[10px] text-subtle transition hover:bg-hover hover:text-fg"
            >
              暂存
            </button>
          )}
          <button
            type="button"
            data-testid="change-discard"
            onClick={confirming ? onConfirmDiscard : onDiscard}
            title={change.staged ? '放弃暂存区与工作区的改动，文件回到 HEAD' : '放弃工作区里对这个文件的改动'}
            className={
              confirming
                ? 'cursor-pointer rounded bg-danger-soft px-1.5 py-0.5 text-[10px] text-danger-text'
                : 'cursor-pointer rounded px-1.5 py-0.5 text-[10px] text-subtle transition hover:bg-hover hover:text-fg'
            }
          >
            {confirming ? '确认放弃' : '放弃'}
          </button>
        </div>
      </div>
      {expanded && (
        <div className="border-t border-line/60">
          <DiffBody path={change.path} staged={change.staged} />
        </div>
      )}
    </div>
  )
}

// DiffBody 按需加载并渲染单个文件的差异。
function DiffBody({ path, staged }: { path: string; staged: boolean }) {
  const [diff, setDiff] = useState<app.FileDiffDTO | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    const state = useWorkbenchStore.getState()
    const sessionId = state.activeId
    const workDir = state.workDir
    if (!sessionId || !workDir) {
      setLoading(false)
      return
    }
    readFileDiff(sessionId, workDir, path, staged)
      .then((result) => {
        if (cancelled) return
        setDiff(result)
        setLoading(false)
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setError(err instanceof Error ? err.message : String(err))
        setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [path, staged])

  if (loading) {
    return <p className="px-3 py-2 text-[11px] text-subtle">正在读取差异…</p>
  }
  if (error) {
    return <p className="px-3 py-2 text-[11px] text-danger-text">{error}</p>
  }
  if (!diff) {
    return <p className="px-3 py-2 text-[11px] text-subtle">没有差异</p>
  }
  if (diff.binary) {
    return <p className="px-3 py-2 text-[11px] text-subtle">二进制文件，没有行级差异</p>
  }
  if (!diff.diff.trim()) {
    return <p className="px-3 py-2 text-[11px] text-subtle">没有差异</p>
  }
  return <ReviewDiff diff={diff.diff} path={path} />
}

// ChangesPanel 是「变更审阅」面板的主体。
export default function ChangesPanel() {
  const changes = usePreviewStore((state) => state.changes)
  const refreshChanges = usePreviewStore((state) => state.refreshChanges)
  const stageChange = usePreviewStore((state) => state.stageChange)
  const unstageChange = usePreviewStore((state) => state.unstageChange)
  const discardChange = usePreviewStore((state) => state.discardChange)
  const openFile = usePreviewStore((state) => state.openFile)

  const [expanded, setExpanded] = useState<string | null>(null)
  const [confirming, setConfirming] = useState<string | null>(null)

  const staged = useMemo(
    () => changes.list.filter((change) => change.staged),
    [changes.list],
  )
  const unstaged = useMemo(
    () => changes.list.filter((change) => !change.staged),
    [changes.list],
  )

  // 同一个文件可能同时有「已暂存」和「未暂存」两行，
  // 所以展开态与确认态都要用「区段 + 路径」做键，否则两行会互相串。
  const rowKey = (change: app.FileChangeDTO): string =>
    `${change.staged ? 's' : 'u'}:${change.path}`

  const toggle = useCallback((key: string) => {
    setExpanded((prev) => (prev === key ? null : key))
    setConfirming(null)
  }, [])

  const renderSection = (
    title: string,
    items: app.FileChangeDTO[],
    emptyLabel: string,
  ) => (
    <section>
      <h3 className="flex items-center gap-2 px-3 py-1.5 text-[10px] font-medium uppercase tracking-wide text-subtle">
        {title}
        <span className="rounded bg-raised px-1 text-[10px] text-muted">{items.length}</span>
      </h3>
      {items.length === 0 ? (
        <p className="px-3 pb-2 text-[11px] text-subtle">{emptyLabel}</p>
      ) : (
        items.map((change) => (
          <ChangeRow
            key={rowKey(change)}
            change={change}
            expanded={expanded === rowKey(change)}
            onToggle={() => toggle(rowKey(change))}
            onStage={() => void stageChange(change.path)}
            onUnstage={() => void unstageChange(change.path)}
            onDiscard={() => setConfirming(rowKey(change))}
            onConfirmDiscard={() => {
              setConfirming(null)
              void discardChange(change.path, change.oldPath ?? '', change.staged)
            }}
            confirming={confirming === rowKey(change)}
            onOpenFile={() => void openFile(change.path)}
          />
        ))
      )}
    </section>
  )

  if (changes.loading && changes.list.length === 0) {
    return (
      <div className="flex min-h-0 flex-1 items-center justify-center text-xs text-subtle">
        正在读取变更…
      </div>
    )
  }

  if (!changes.isRepo) {
    return (
      <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-2 px-6 text-center">
        <p className="text-xs text-fg">当前目录不在 git 仓库中</p>
        <p className="text-[11px] text-subtle">在仓库目录里打开会话后即可审阅改动</p>
      </div>
    )
  }

  return (
    <div data-testid="changes-panel" className="flex min-h-0 flex-1 flex-col overflow-hidden">
      <div className="flex shrink-0 items-center gap-2 border-b border-line bg-panel px-3 py-1.5">
        <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-muted">
          {changes.branch || 'HEAD'}
        </span>
        <button
          type="button"
          data-testid="changes-refresh"
          onClick={() => void refreshChanges()}
          title="刷新变更"
          className="cursor-pointer rounded px-1.5 py-0.5 text-[10px] text-subtle transition hover:bg-hover hover:text-fg"
        >
          刷新
        </button>
      </div>
      {changes.error && (
        <p className="shrink-0 border-b border-line bg-danger-soft px-3 py-1.5 text-[11px] text-danger-text">
          {changes.error}
        </p>
      )}
      <div className="min-h-0 flex-1 overflow-auto">
        {renderSection('已暂存', staged, '没有已暂存的改动')}
        {renderSection('未暂存', unstaged, '工作区没有未暂存的改动')}
      </div>
    </div>
  )
}
