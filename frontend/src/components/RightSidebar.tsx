import {
  Fragment,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
} from 'react'
import { ListSessionFiles } from '../../wailsjs/go/app/App'
import type { app } from '../../wailsjs/go/models'
import { errorMessage, useWorkbenchStore } from '../stores/workbench'
import {
  RIGHT_SIDEBAR_DEFAULT_WIDTH,
  RIGHT_SIDEBAR_MAX_WIDTH,
  RIGHT_SIDEBAR_MIN_WIDTH,
  useSettingsStore,
} from '../stores/settings'
import { DRAG_REGION, NO_DRAG } from '../lib/platform'

// DirState 是一个目录的加载状态。entries 为 null 表示尚未加载。
interface DirState {
  entries: app.FileEntryDTO[] | null
  loading: boolean
  error: string | null
  truncated: boolean
}

const EMPTY_DIR: DirState = { entries: null, loading: false, error: null, truncated: false }

function ChevronIcon({ open }: { open: boolean }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className={`size-3 shrink-0 text-subtle transition-transform ${open ? 'rotate-90' : ''}`}
      aria-hidden="true"
    >
      <path d="m9 18 6-6-6-6" />
    </svg>
  )
}

function FolderIcon({ open }: { open: boolean }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      className={`size-3.5 shrink-0 ${open ? 'text-accent-text' : 'text-muted'}`}
      aria-hidden="true"
    >
      {open ? (
        <path d="M3 8a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v1H5.6a2 2 0 0 0-1.9 1.4L3 16Z" />
      ) : (
        <path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2Z" />
      )}
      {open && <path d="M3.2 16 5 11h16l-1.8 6.2a2 2 0 0 1-1.9 1.4H5a2 2 0 0 1-1.8-2.6Z" />}
    </svg>
  )
}

function FileIcon({ name }: { name: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
      className={`size-3.5 shrink-0 ${fileTone(name)}`}
      aria-hidden="true"
    >
      <path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8Z" />
      <path d="M14 3v5h5" />
    </svg>
  )
}

function RefreshIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-3.5 shrink-0"
      aria-hidden="true"
    >
      <path d="M20 11a8 8 0 1 0-2.3 5.7" />
      <path d="M20 4v7h-7" />
    </svg>
  )
}

function CloseIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      className="size-3.5 shrink-0"
      aria-hidden="true"
    >
      <path d="M18 6 6 18M6 6l12 12" />
    </svg>
  )
}

function FolderOpenIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-4 shrink-0 text-subtle"
      aria-hidden="true"
    >
      <path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v1H5.6a2 2 0 0 0-1.9 1.4L3 16Z" />
      <path d="M3.2 16 5 11h16l-1.8 6.2a2 2 0 0 1-1.9 1.4H5a2 2 0 0 1-1.8-2.6Z" />
    </svg>
  )
}

// fileTone 按扩展名给文件图标上色，语气与编辑器文件图标一致。
function fileTone(name: string): string {
  const ext = name.includes('.') ? name.split('.').pop()?.toLowerCase() ?? '' : ''
  if (ext === 'ts' || ext === 'tsx') return 'text-sky-500'
  if (ext === 'js' || ext === 'jsx' || ext === 'mjs' || ext === 'cjs') return 'text-amber-500'
  if (ext === 'go') return 'text-cyan-500'
  if (ext === 'json' || ext === 'yaml' || ext === 'yml' || ext === 'toml') return 'text-amber-400'
  if (ext === 'md' || ext === 'mdx') return 'text-violet-400'
  if (ext === 'css' || ext === 'scss' || ext === 'html') return 'text-rose-400'
  if (ext === 'png' || ext === 'jpg' || ext === 'jpeg' || ext === 'svg' || ext === 'webp') {
    return 'text-emerald-500'
  }
  return 'text-subtle'
}

function baseName(path: string): string {
  const parts = path.split(/[\\/]/).filter(Boolean)
  return parts[parts.length - 1] ?? path
}

interface FileRowProps {
  entry: app.FileEntryDTO
  depth: number
  open: boolean
  selected: boolean
  onActivate: (entry: app.FileEntryDTO) => void
  onToggle: (entry: app.FileEntryDTO) => void
}

function FileRow({ entry, depth, open, selected, onActivate, onToggle }: FileRowProps) {
  const activate = () => {
    if (entry.isDir) onToggle(entry)
    else onActivate(entry)
  }
  return (
    <button
      type="button"
      role="treeitem"
      aria-level={depth + 1}
      aria-expanded={entry.isDir ? open : undefined}
      aria-selected={selected}
      title={entry.isSymlink ? `${entry.path}（符号链接）` : entry.path}
      data-testid="file-row"
      data-path={entry.path}
      onClick={activate}
      onKeyDown={(event) => {
        if (event.key === 'Enter' || event.key === ' ') {
          event.preventDefault()
          activate()
        }
      }}
      style={{ paddingLeft: 8 + depth * 12 }}
      className={`mx-1 flex h-7 w-[calc(100%-0.5rem)] min-w-0 cursor-pointer items-center gap-1.5 pr-2 text-left text-[12px] transition ${ selected ? 'accent-edge bg-hover text-fg' : 'text-muted hover:bg-hover hover:text-fg' }`}
    >
      {entry.isDir ? <ChevronIcon open={open} /> : <span className="size-3 shrink-0" />}
      {entry.isDir ? <FolderIcon open={open} /> : <FileIcon name={entry.name} />}
      <span className="min-w-0 flex-1 truncate">{entry.name}</span>
      {entry.isSymlink && (
        <span className="shrink-0 text-[10px] text-subtle" aria-label="符号链接">
          ↗
        </span>
      )}
    </button>
  )
}

function TreeNotice({
  text,
  depth = 0,
  tone = 'muted',
}: {
  text: string
  depth?: number
  tone?: 'muted' | 'danger'
}) {
  return (
    <p
      style={{ paddingLeft: 28 + depth * 12 }}
      className={`py-1 pr-2 text-[11px] ${tone === 'danger' ? 'text-danger-text' : 'text-subtle'}`}
    >
      {text}
    </p>
  )
}

// RightSidebar 是右侧文件栏：懒加载目录树，点击文件在中间区域只读预览。
// 结构参考 Zed 的 Project Panel：目录优先、目录前折叠箭头、始终隐藏 VCS 目录。
export default function RightSidebar() {
  const sessionId = useWorkbenchStore((state) => state.activeId)
  const workDir = useWorkbenchStore((state) => state.workDir)
  const openFile = useWorkbenchStore((state) => state.openFile)
  const filesRevision = useWorkbenchStore((state) => state.filesRevision)
  const previewPath = useWorkbenchStore((state) => state.preview?.path ?? null)

  const width = useSettingsStore((state) => state.rightSidebarWidth)
  const setWidth = useSettingsStore((state) => state.setRightSidebarWidth)
  const persistWidth = useSettingsStore((state) => state.persistRightSidebarWidth)
  const closeSidebar = useSettingsStore((state) => state.setRightSidebarOpen)
  const leftSidebarCollapsed = useSettingsStore((state) => state.sidebarCollapsed)

  const [dirs, setDirs] = useState<Record<string, DirState>>({})
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set())
  const [selected, setSelected] = useState<string | null>(null)
  const generation = useRef(0)

  const loadDir = useCallback(
    async (path: string) => {
      if (!sessionId || !workDir) return
      const run = generation.current
      setDirs((prev) => ({
        ...prev,
        [path]: { ...(prev[path] ?? EMPTY_DIR), loading: true, error: null },
      }))
      try {
        const result = await ListSessionFiles(sessionId, workDir, path)
        if (run !== generation.current) return
        setDirs((prev) => ({
          ...prev,
          [path]: {
            entries: result.entries ?? [],
            loading: false,
            error: null,
            truncated: Boolean(result.truncated),
          },
        }))
      } catch (err) {
        if (run !== generation.current) return
        setDirs((prev) => ({
          ...prev,
          [path]: { entries: null, loading: false, error: errorMessage(err), truncated: false },
        }))
      }
    },
    [sessionId, workDir],
  )

  // 会话或工作目录变化时重建整棵树，避免把上一个项目的展开状态带过来。
  useEffect(() => {
    generation.current += 1
    setDirs({})
    setExpanded(new Set())
    setSelected(null)
    if (sessionId && workDir) void loadDir('')
  }, [loadDir, sessionId, workDir])

  const refreshAll = useCallback(() => {
    if (!sessionId) return
    generation.current += 1
    const paths = ['', ...expanded]
    for (const path of paths) void loadDir(path)
  }, [expanded, loadDir, sessionId])

  // 一轮对话结束后可能产生新文件：重载根目录与已展开目录。
  const revisionRef = useRef(filesRevision)
  useEffect(() => {
    if (revisionRef.current === filesRevision) return
    revisionRef.current = filesRevision
    refreshAll()
  }, [filesRevision, refreshAll])

  const toggleDir = (entry: app.FileEntryDTO) => {
    setSelected(entry.path)
    const isOpen = expanded.has(entry.path)
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(entry.path)) next.delete(entry.path)
      else next.add(entry.path)
      return next
    })
    if (!isOpen && !dirs[entry.path]?.entries && !dirs[entry.path]?.loading) {
      void loadDir(entry.path)
    }
  }

  const activateFile = (entry: app.FileEntryDTO) => {
    setSelected(entry.path)
    void openFile(entry.path)
  }

  const startResize = (event: ReactPointerEvent<HTMLDivElement>) => {
    event.preventDefault()
    const startX = event.clientX
    const startWidth = width
    // 给中间对话区至少留 360px；左侧栏折叠时只占 72px。
    const leftRail = leftSidebarCollapsed ? 72 : 256
    const maximum = Math.min(
      RIGHT_SIDEBAR_MAX_WIDTH,
      Math.max(RIGHT_SIDEBAR_MIN_WIDTH, window.innerWidth - leftRail - 360),
    )
    const previousSelect = document.body.style.userSelect
    document.body.style.userSelect = 'none'
    const onMove = (moveEvent: PointerEvent) => {
      const next = startWidth + (startX - moveEvent.clientX)
      setWidth(Math.min(maximum, Math.max(RIGHT_SIDEBAR_MIN_WIDTH, next)))
    }
    const onUp = () => {
      document.body.style.userSelect = previousSelect
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
      persistWidth()
    }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
  }

  const rootState = dirs[''] ?? { ...EMPTY_DIR, loading: Boolean(sessionId) }
  const folderName = useMemo(() => (workDir ? baseName(workDir) : ''), [workDir])

  const renderDir = (path: string, depth: number): ReactNode => {
    const state = dirs[path]
    if (!state || (state.loading && state.entries === null)) {
      return <TreeNotice key={`${path}:loading`} depth={depth} text="正在加载…" />
    }
    if (state.error) {
      return <TreeNotice key={`${path}:error`} depth={depth} tone="danger" text={state.error} />
    }
    const entries = state.entries ?? []
    if (entries.length === 0) {
      return (
        <TreeNotice
          key={`${path}:empty`}
          depth={depth}
          text={path === '' ? '工作目录为空' : '空目录'}
        />
      )
    }
    return (
      <Fragment key={`${path}:entries`}>
        {entries.map((entry) => {
          const open = entry.isDir && expanded.has(entry.path)
          return (
            <Fragment key={entry.path}>
              <FileRow
                entry={entry}
                depth={depth}
                open={open}
                selected={selected === entry.path || previewPath === entry.path}
                onActivate={activateFile}
                onToggle={toggleDir}
              />
              {open && renderDir(entry.path, depth + 1)}
            </Fragment>
          )
        })}
        {state.truncated && (
          <TreeNotice key={`${path}:truncated`} depth={depth} text="目录项过多，仅显示前 1000 项" />
        )}
      </Fragment>
    )
  }

  let body: ReactNode
  if (!sessionId) {
    body = <TreeNotice text="先选择一个会话" />
  } else if (!workDir) {
    body = <TreeNotice text="未选择工作目录" />
  } else if (rootState.error) {
    body = (
      <div className="px-3 py-4 text-[11px]">
        <p className="text-danger-text">读取目录失败</p>
        <p className="mt-1 break-words text-subtle">{rootState.error}</p>
        <button
          type="button"
          onClick={refreshAll}
          className="mt-2 cursor-pointer border border-line px-2 py-1 text-[11px] text-muted transition hover:border-line-strong hover:bg-hover hover:text-fg"
        >
          重试
        </button>
      </div>
    )
  } else {
    body = renderDir('', 0)
  }

  return (
    <aside
      data-testid="right-sidebar"
      style={{
        width,
        maxWidth: `max(${RIGHT_SIDEBAR_MIN_WIDTH}px, calc(100vw - ${
          (leftSidebarCollapsed ? 72 : 256) + 360
        }px))`,
      }}
      className="relative flex shrink-0 flex-col border-l border-line bg-panel"
    >
      <div
        role="separator"
        aria-orientation="vertical"
        aria-label="调整文件栏宽度"
        data-testid="right-sidebar-resize"
        onPointerDown={startResize}
        onDoubleClick={() => {
          setWidth(RIGHT_SIDEBAR_DEFAULT_WIDTH)
          persistWidth()
        }}
        title="拖动调整宽度，双击恢复默认"
        className="absolute inset-y-0 -left-1 z-10 w-2 cursor-col-resize transition-colors hover:bg-accent/30"
      />

      <header
        style={DRAG_REGION}
        className="flex h-11 shrink-0 items-center gap-2 border-b border-line px-3"
      >
        <FolderOpenIcon />
        <span className="shrink-0 font-mono text-[10px] uppercase tracking-[0.16em] text-subtle">
          文件
        </span>
        <span className="min-w-0 flex-1 truncate font-mono text-[10px] text-subtle" title={workDir}>
          {folderName}
        </span>
        <button
          type="button"
          data-testid="right-sidebar-refresh"
          style={NO_DRAG}
          aria-label="刷新文件"
          title="刷新文件"
          onClick={refreshAll}
          disabled={!sessionId}
          className="cursor-pointer p-1 text-subtle transition hover:bg-hover hover:text-fg disabled:cursor-not-allowed disabled:opacity-40"
        >
          <RefreshIcon />
        </button>
        <button
          type="button"
          data-testid="right-sidebar-close"
          style={NO_DRAG}
          aria-label="隐藏文件栏"
          title="隐藏文件栏"
          onClick={() => closeSidebar(false)}
          className="cursor-pointer p-1 text-subtle transition hover:bg-hover hover:text-fg"
        >
          <CloseIcon />
        </button>
      </header>

      <div
        data-testid="file-tree"
        role="tree"
        aria-label="文件树"
        className="min-h-0 flex-1 overflow-auto py-1.5"
      >
        {body}
      </div>

      <div className="shrink-0 border-t border-line px-3 py-1.5 font-mono text-[10px] text-subtle">
        点击文件在中间区域预览
      </div>
    </aside>
  )
}
