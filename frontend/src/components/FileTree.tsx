import {
  Fragment,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type DragEvent,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
} from 'react'
import {
  CreateSessionFile,
  DeleteSessionFile,
  ListSessionFiles,
  MoveSessionFile,
  RenameSessionFile,
  SearchSessionFiles,
} from '../../wailsjs/go/app/App'
import type { app } from '../../wailsjs/go/models'
import { errorMessage, useWorkbenchStore } from '../stores/workbench'
import { usePreviewStore } from '../stores/preview'
import {
  MIN_CHAT_WIDTH,
  RIGHT_SIDEBAR_DEFAULT_WIDTH,
  RIGHT_SIDEBAR_MAX_WIDTH,
  RIGHT_SIDEBAR_MIN_WIDTH,
  SIDEBAR_COLLAPSED_WIDTH,
  useSettingsStore,
} from '../stores/settings'
import { DRAG_REGION, NO_DRAG } from '../lib/platform'
import { trackResizeShadow } from '../lib/resize'
import ContextMenu, { type MenuItem } from './ContextMenu'

// DirState 是一个目录的加载状态。entries 为 null 表示尚未加载。
interface DirState {
  entries: app.FileEntryDTO[] | null
  loading: boolean
  error: string | null
  truncated: boolean
}

const EMPTY_DIR: DirState = { entries: null, loading: false, error: null, truncated: false }

// DRAG_MIME 是文件树内部拖拽用的 MIME 类型，避免与外部拖拽混淆。
const DRAG_MIME = 'application/x-b-desktop-file-path'

// SEARCH_DEBOUNCE_MS 是搜索框的输入防抖时间。
const SEARCH_DEBOUNCE_MS = 200

// MenuState 是当前打开的右键菜单。
interface MenuState {
  x: number
  y: number
  // target 是菜单作用的条目；null 表示作用在目录根上。
  target: app.FileEntryDTO | null
}

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

function NewFileIcon() {
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
      <path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8Z" />
      <path d="M14 3v5h5" />
      <path d="M12 12v6M9 15h6" />
    </svg>
  )
}

function NewFolderIcon() {
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
      <path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2Z" />
      <path d="M12 11v5M9.5 13.5h5" />
    </svg>
  )
}

// fileTone 按扩展名给文件图标上色，语气与编辑器文件图标一致。
function fileTone(name: string): string {
  const ext = name.includes('.') ? name.split('.').pop()?.toLowerCase() ?? '' : ''
  if (ext === 'ts' || ext === 'tsx') return 'text-sky-500'
  if (ext === 'js' || ext === 'jsx' || ext === 'mjs' || ext === 'cjs') return 'text-amber-500'
  if (ext === 'go') return 'text-cyan-500'
  if (ext === 'rs') return 'text-orange-500'
  if (ext === 'c' || ext === 'h' || ext === 'cpp') return 'text-sky-600'
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

function parentPath(path: string): string {
  const parts = path.split('/').filter(Boolean)
  parts.pop()
  return parts.join('/')
}

// joinPath 拼接父目录与文件名，返回工作目录相对路径。
function joinPath(parent: string, name: string): string {
  return parent === '' ? name : `${parent}/${name}`
}

interface FileRowProps {
  entry: app.FileEntryDTO
  depth: number
  open: boolean
  selected: boolean
  badge: string | null
  renaming: boolean
  dragOver: boolean
  onActivate: (entry: app.FileEntryDTO) => void
  onToggle: (entry: app.FileEntryDTO) => void
  onContextMenu: (entry: app.FileEntryDTO, event: React.MouseEvent) => void
  onRenameStart: (entry: app.FileEntryDTO) => void
  onRenameCommit: (name: string) => void
  onRenameCancel: () => void
  onDragStart: (entry: app.FileEntryDTO, event: DragEvent<HTMLDivElement>) => void
  onDragOver: (entry: app.FileEntryDTO) => void
  onDragLeave: (entry: app.FileEntryDTO) => void
  onDrop: (entry: app.FileEntryDTO) => void
}

function FileRow({
  entry,
  depth,
  open,
  selected,
  badge,
  renaming,
  dragOver,
  onActivate,
  onToggle,
  onContextMenu,
  onRenameStart,
  onRenameCommit,
  onRenameCancel,
  onDragStart,
  onDragOver,
  onDragLeave,
  onDrop,
}: FileRowProps) {
  const activate = () => {
    if (renaming) return
    if (entry.isDir) onToggle(entry)
    else onActivate(entry)
  }
  return (
    <div
      role="treeitem"
      aria-level={depth + 1}
      aria-expanded={entry.isDir ? open : undefined}
      aria-selected={selected}
      title={entry.isSymlink ? `${entry.path}（符号链接）` : entry.path}
      data-testid="file-row"
      data-path={entry.path}
      draggable={!renaming}
      onDragStart={(event) => onDragStart(entry, event)}
      onDragOver={(event) => {
        if (!entry.isDir) return
        event.preventDefault()
        event.dataTransfer.dropEffect = 'move'
        onDragOver(entry)
      }}
      onDragLeave={() => onDragLeave(entry)}
      onDrop={(event) => {
        if (!entry.isDir) return
        event.preventDefault()
        event.stopPropagation()
        onDrop(entry)
      }}
      onClick={activate}
      onDoubleClick={() => {
        if (!entry.isDir) onRenameStart(entry)
      }}
      onContextMenu={(event) => {
        event.preventDefault()
        onContextMenu(entry, event)
      }}
      onKeyDown={(event) => {
        if (renaming) return
        if (event.key === 'Enter' || event.key === ' ') {
          event.preventDefault()
          activate()
        }
        if (event.key === 'F2') {
          event.preventDefault()
          onRenameStart(entry)
        }
      }}
      tabIndex={0}
      style={{ paddingLeft: 8 + depth * 12 }}
      className={`mx-1 flex h-7 w-[calc(100%-0.5rem)] min-w-0 cursor-pointer items-center gap-1.5 pr-2 text-left transition ${
        dragOver ? 'bg-accent-soft ring-1 ring-accent' : ''
      } ${
        selected
          ? 'accent-edge bg-hover text-fg'
          : 'text-muted hover:bg-hover hover:text-fg'
      }`}
    >
      {entry.isDir ? <ChevronIcon open={open} /> : <span className="size-3 shrink-0" />}
      {entry.isDir ? <FolderIcon open={open} /> : <FileIcon name={entry.name} />}
      {renaming ? (
        <input
          autoFocus
          data-testid="file-rename-input"
          defaultValue={entry.name}
          onClick={(event) => event.stopPropagation()}
          onDoubleClick={(event) => event.stopPropagation()}
          onBlur={(event) => onRenameCommit(event.target.value)}
          onKeyDown={(event) => {
            event.stopPropagation()
            if (event.key === 'Enter') {
              event.preventDefault()
              onRenameCommit(event.currentTarget.value)
            }
            if (event.key === 'Escape') {
              event.preventDefault()
              onRenameCancel()
            }
          }}
          className="min-w-0 flex-1 border border-accent bg-surface px-1 py-0.5 text-xs text-fg outline-none"
        />
      ) : (
        <span className="min-w-0 flex-1 truncate font-mono text-[11px]">{entry.name}</span>
      )}
      {!renaming && badge && (
        <span
          className="shrink-0 border border-accent/40 bg-accent-soft px-1 font-mono text-[10px] text-accent-text"
          aria-label="git 状态"
        >
          {badge}
        </span>
      )}
      {entry.isSymlink && !renaming && (
        <span className="shrink-0 text-[10px] text-subtle" aria-label="符号链接">
          ↗
        </span>
      )}
    </div>
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

// NewItemInput 是「新建文件 / 新建文件夹」的内联输入行。
function NewItemInput({
  depth,
  isDir,
  onCommit,
  onCancel,
}: {
  depth: number
  isDir: boolean
  onCommit: (name: string) => void
  onCancel: () => void
}) {
  return (
    <div
      style={{ paddingLeft: 8 + depth * 12 }}
      className="mx-1 flex h-7 w-[calc(100%-0.5rem)] items-center gap-1.5 pr-2 text-xs text-fg"
    >
      {isDir ? <FolderIcon open={false} /> : <FileIcon name="new" />}
      <input
        autoFocus
        data-testid="file-create-input"
        placeholder={isDir ? '文件夹名' : '文件名'}
        onKeyDown={(event) => {
          event.stopPropagation()
          if (event.key === 'Enter') {
            event.preventDefault()
            onCommit(event.currentTarget.value)
          }
          if (event.key === 'Escape') {
            event.preventDefault()
            onCancel()
          }
        }}
        onBlur={(event) => onCommit(event.target.value)}
        className="min-w-0 flex-1 border border-accent bg-surface px-1 py-0.5 text-xs text-fg outline-none"
      />
    </div>
  )
}

// FileTree 是右侧文件栏：懒加载目录树，支持预览、新建、重命名、删除与拖拽移动。
// 结构参考 Zed 的 Project Panel：目录优先、目录前折叠箭头、始终隐藏 VCS 目录。
export default function FileTree() {
  const sessionId = useWorkbenchStore((state) => state.activeId)
  const workDir = useWorkbenchStore((state) => state.workDir)
  const filesRevision = useWorkbenchStore((state) => state.filesRevision)
  const bumpFilesRevision = useWorkbenchStore((state) => state.bumpFilesRevision)
  const openFile = usePreviewStore((state) => state.openFile)
  const closeTab = usePreviewStore((state) => state.closeTab)
  const renameTab = usePreviewStore((state) => state.renameTab)
  const activePath = usePreviewStore((state) => state.active)
  const changes = usePreviewStore((state) => state.changes)

  const width = useSettingsStore((state) => state.rightSidebarWidth)
  const setWidth = useSettingsStore((state) => state.setRightSidebarWidth)
  const persistWidth = useSettingsStore((state) => state.persistRightSidebarWidth)
  const closeSidebar = useSettingsStore((state) => state.setRightSidebarOpen)
  const leftSidebarCollapsed = useSettingsStore((state) => state.sidebarCollapsed)
  const leftSidebarWidth = useSettingsStore((state) => state.sidebarWidth)

  const [dirs, setDirs] = useState<Record<string, DirState>>({})
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set())
  const [selected, setSelected] = useState<string | null>(null)
  const [menu, setMenu] = useState<MenuState | null>(null)
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null)
  const [renaming, setRenaming] = useState<string | null>(null)
  const [creating, setCreating] = useState<{ parent: string; isDir: boolean } | null>(null)
  const [filter, setFilter] = useState('')
  const [searchResults, setSearchResults] = useState<string[] | null>(null)
  const [searching, setSearching] = useState(false)
  const [notice, setNotice] = useState<string | null>(null)
  const [dragOverPath, setDragOverPath] = useState<string | null>(null)
  const generation = useRef(0)
  const draggedPath = useRef<string | null>(null)
  const searchTimer = useRef<number | undefined>(undefined)
  const searchSeq = useRef(0)

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
    setMenu(null)
    setRenaming(null)
    setCreating(null)
    setSearchResults(null)
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

  // 搜索框：输入防抖后调用后端按文件名查找，结果以扁平列表展示。
  // searchSeq 保证只有最新一次查询的结果会被采纳：防抖只取消了定时器，
  // 已经在途的请求仍可能后到并覆盖结果。
  useEffect(() => {
    const query = filter.trim()
    if (query === '') {
      setSearchResults(null)
      setSearching(false)
      return
    }
    if (!sessionId || !workDir) return
    window.clearTimeout(searchTimer.current)
    setSearching(true)
    const seq = (searchSeq.current += 1)
    searchTimer.current = window.setTimeout(() => {
      void SearchSessionFiles(sessionId, workDir, query)
        .then((paths) => {
          if (seq !== searchSeq.current) return
          setSearchResults(paths ?? [])
          setSearching(false)
        })
        .catch((err: unknown) => {
          if (seq !== searchSeq.current) return
          setSearchResults([])
          setSearching(false)
          setNotice(errorMessage(err))
        })
    }, SEARCH_DEBOUNCE_MS)
    return () => window.clearTimeout(searchTimer.current)
  }, [filter, sessionId, workDir])

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

  // changeBadge 返回文件的 git 状态字母，用于在树上标记改动的文件。
  const changeBadge = useMemo(() => {
    const map = new Map<string, string>()
    for (const change of changes.list) {
      const letter =
        change.status === 'added'
          ? 'A'
          : change.status === 'deleted'
            ? 'D'
            : change.status === 'renamed'
              ? 'R'
              : change.status === 'untracked'
                ? 'U'
                : change.status === 'conflicted'
                  ? '!'
                  : 'M'
      map.set(change.path, letter)
    }
    return map
  }, [changes.list])

  const runAction = async (action: () => Promise<void>, success: string): Promise<boolean> => {
    try {
      await action()
      setNotice(success)
      bumpFilesRevision()
      return true
    } catch (err) {
      setNotice(errorMessage(err))
      return false
    }
  }

  const startRename = (entry: app.FileEntryDTO) => {
    setRenaming(entry.path)
    setSelected(entry.path)
  }

  const commitRename = async (name: string) => {
    if (!renaming) return
    const target = renaming
    setRenaming(null)
    const trimmed = name.trim()
    if (trimmed === '' || trimmed === baseName(target)) return
    if (trimmed.includes('/')) {
      setNotice('名称不能包含路径分隔符')
      return
    }
    await runAction(
      async () => {
        const next = joinPath(parentPath(target), trimmed)
        await RenameSessionFile(sessionId ?? '', workDir, target, next)
        renameTab(target, next)
      },
      '已重命名',
    )
  }

  const startCreate = (parent: string, isDir: boolean) => {
    setCreating({ parent, isDir })
    if (parent !== '' && !expanded.has(parent)) {
      setExpanded((prev) => new Set(prev).add(parent))
      if (!dirs[parent]?.entries) void loadDir(parent)
    }
  }

  const commitCreate = async (name: string) => {
    if (!creating) return
    const { parent, isDir } = creating
    setCreating(null)
    const trimmed = name.trim()
    if (trimmed === '') return
    if (trimmed.includes('/')) {
      setNotice('名称不能包含路径分隔符')
      return
    }
    const path = joinPath(parent, trimmed)
    const created = await runAction(
      async () => {
        await CreateSessionFile(sessionId ?? '', workDir, path, isDir)
      },
      isDir ? '已新建文件夹' : '已新建文件',
    )
    if (!created) return
    if (isDir) {
      setExpanded((prev) => new Set(prev).add(path))
    } else {
      void openFile(path)
    }
  }

  const deleteEntry = async (entry: app.FileEntryDTO) => {
    await runAction(
      async () => {
        await DeleteSessionFile(sessionId ?? '', workDir, entry.path)
        // 文件没了，对应的预览标签也要关掉。
        closeTab(entry.path)
      },
      `已删除 ${entry.name}`,
    )
    setExpanded((prev) => {
      const next = new Set(prev)
      for (const path of next) {
        if (path === entry.path || path.startsWith(`${entry.path}/`)) next.delete(path)
      }
      return next
    })
  }

  const moveEntry = async (from: string, toDir: string) => {
    if (toDir === `${from}/` || toDir.startsWith(`${from}/`)) {
      setNotice('不能把目录移动到它自己里面')
      return
    }
    // 拖回原目录等于没动。
    if (toDir === parentPath(from)) return
    try {
      const moved = await MoveSessionFile(sessionId ?? '', workDir, from, toDir)
      setNotice(`已移动到 ${parentPath(moved) || '根目录'}`)
      bumpFilesRevision()
    } catch (err) {
      setNotice(errorMessage(err))
    }
  }

  const copyPath = async (path: string) => {
    try {
      await navigator.clipboard.writeText(path)
      setNotice('已复制路径')
    } catch {
      setNotice('复制失败')
    }
  }

  // 操作提示几秒后自动消失，不长期占着面板。
  useEffect(() => {
    if (!notice) return
    const timer = window.setTimeout(() => setNotice(null), 3000)
    return () => window.clearTimeout(timer)
  }, [notice])

  const buildMenuItems = (): MenuItem[] => {
    const target = menu?.target ?? null
    const dir = target?.isDir ? target.path : target ? parentPath(target.path) : ''
    const items: MenuItem[] = []
    if (target) {
      items.push({
        id: 'open',
        label: target.isDir ? '展开 / 收起' : '打开预览',
        onSelect: () => {
          if (target.isDir) toggleDir(target)
          else activateFile(target)
        },
      })
      items.push({
        id: 'new-file',
        label: '新建文件…',
        onSelect: () => startCreate(dir, false),
      })
      items.push({
        id: 'new-folder',
        label: '新建文件夹…',
        onSelect: () => startCreate(dir, true),
      })
      items.push({
        id: 'rename',
        label: '重命名…',
        separatorBefore: true,
        onSelect: () => startRename(target),
      })
      items.push({
        id: 'copy-path',
        label: '复制路径',
        onSelect: () => void copyPath(target.path),
      })
      // 删除不可撤销：第一次点击只把菜单项变成确认态。
      const confirming = confirmDelete === target.path
      items.push({
        id: 'delete',
        label: confirming ? '确认删除（不可撤销）' : '删除',
        danger: true,
        separatorBefore: true,
        keepOpen: !confirming,
        onSelect: () => {
          if (!confirming) {
            setConfirmDelete(target.path)
            return
          }
          setConfirmDelete(null)
          void deleteEntry(target)
        },
      })
    } else {
      items.push({
        id: 'new-file',
        label: '新建文件…',
        onSelect: () => startCreate('', false),
      })
      items.push({
        id: 'new-folder',
        label: '新建文件夹…',
        onSelect: () => startCreate('', true),
      })
    }
    return items
  }

  const startResize = (event: ReactPointerEvent<HTMLDivElement>) => {
    event.preventDefault()
    const handle = event.currentTarget
    const startX = event.clientX
    const startWidth = width
    // 给中间对话区至少留 MIN_CHAT_WIDTH；左侧栏按当前展开宽度计算。
    const leftRail = leftSidebarCollapsed ? SIDEBAR_COLLAPSED_WIDTH : leftSidebarWidth
    const maximum = Math.min(
      RIGHT_SIDEBAR_MAX_WIDTH,
      Math.max(RIGHT_SIDEBAR_MIN_WIDTH, window.innerWidth - leftRail - MIN_CHAT_WIDTH),
    )
    const previousSelect = document.body.style.userSelect
    document.body.style.userSelect = 'none'
    document.body.classList.add('is-resizing')
    handle.dataset.resizing = 'true'
    trackResizeShadow(handle, event.clientY)
    const onMove = (moveEvent: PointerEvent) => {
      trackResizeShadow(handle, moveEvent.clientY)
      const next = startWidth + (startX - moveEvent.clientX)
      setWidth(Math.min(maximum, Math.max(RIGHT_SIDEBAR_MIN_WIDTH, next)))
    }
    const onUp = () => {
      document.body.style.userSelect = previousSelect
      document.body.classList.remove('is-resizing')
      delete handle.dataset.resizing
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
      persistWidth()
    }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
  }

  const rootState = dirs[''] ?? { ...EMPTY_DIR, loading: Boolean(sessionId) }
  const folderName = useMemo(() => (workDir ? baseName(workDir) : ''), [workDir])

  const renderTree = (path: string, depth: number): ReactNode => {
    const state = dirs[path]
    if (!state || (state.loading && state.entries === null)) {
      return <TreeNotice key={`${path}:loading`} depth={depth} text="正在加载…" />
    }
    if (state.error) {
      return <TreeNotice key={`${path}:error`} depth={depth} tone="danger" text={state.error} />
    }
    const entries = state.entries ?? []
    if (entries.length === 0 && !(creating && creating.parent === path)) {
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
        {creating && creating.parent === path && (
          <NewItemInput
            depth={depth}
            isDir={creating.isDir}
            onCommit={(name) => void commitCreate(name)}
            onCancel={() => setCreating(null)}
          />
        )}
        {entries.map((entry) => {
          const open = entry.isDir && expanded.has(entry.path)
          return (
            <Fragment key={entry.path}>
              <FileRow
                entry={entry}
                depth={depth}
                open={open}
                selected={selected === entry.path || activePath === entry.path}
                badge={changeBadge.get(entry.path) ?? null}
                renaming={renaming === entry.path}
                dragOver={dragOverPath === entry.path}
                onActivate={activateFile}
                onToggle={toggleDir}
                onContextMenu={(target, event) =>
                  setMenu({ x: event.clientX, y: event.clientY, target })
                }
                onRenameStart={startRename}
                onRenameCommit={(name) => void commitRename(name)}
                onRenameCancel={() => setRenaming(null)}
                onDragStart={(target, event) => {
                  draggedPath.current = target.path
                  event.dataTransfer.effectAllowed = 'move'
                  event.dataTransfer.setData(DRAG_MIME, target.path)
                  event.dataTransfer.setData('text/plain', target.path)
                }}
                onDragOver={(target) => setDragOverPath(target.path)}
                onDragLeave={(target) => {
                  if (dragOverPath === target.path) setDragOverPath(null)
                }}
                onDrop={(target) => {
                  setDragOverPath(null)
                  const from = draggedPath.current
                  draggedPath.current = null
                  if (from) void moveEntry(from, target.path)
                }}
              />
              {open && renderTree(entry.path, depth + 1)}
            </Fragment>
          )
        })}
        {state.truncated && (
          <TreeNotice key={`${path}:truncated`} depth={depth} text="目录项过多，仅显示前 1000 项" />
        )}
      </Fragment>
    )
  }

  const renderSearchResults = (): ReactNode => {
    if (searching) {
      return <TreeNotice text="正在搜索…" />
    }
    const results = searchResults ?? []
    if (results.length === 0) {
      return <TreeNotice text="没有匹配的文件" />
    }
    return (
      <Fragment>
        {results.map((path) => (
          <button
            key={path}
            type="button"
            data-testid="file-search-result"
            data-path={path}
            onClick={() => {
              setSelected(path)
              void openFile(path)
            }}
            className="mx-1 flex h-7 w-[calc(100%-0.5rem)] min-w-0 cursor-pointer items-center gap-1.5 px-2 text-left transition hover:bg-hover hover:text-fg"
          >
            <FileIcon name={baseName(path)} />
            <span className="min-w-0 flex-1 truncate" title={path}>
              {path}
            </span>
          </button>
        ))}
        <TreeNotice text={`共 ${results.length} 个结果`} />
      </Fragment>
    )
  }

  let body: ReactNode
  if (!sessionId) {
    body = <TreeNotice text="先选择一个会话" />
  } else if (!workDir) {
    body = <TreeNotice text="未选择工作目录" />
  } else if (filter.trim() !== '') {
    body = renderSearchResults()
  } else if (rootState.error) {
    body = (
      <div className="px-3 py-4 text-[11px]">
        <p className="text-danger-text">读取目录失败</p>
        <p className="mt-1 break-words text-subtle">{rootState.error}</p>
        <button
          type="button"
          onClick={refreshAll}
          className="mt-2 cursor-pointer border border-line px-2 py-1 text-[11px] text-muted hover:bg-hover hover:text-fg"
        >
          重试
        </button>
      </div>
    )
  } else {
    body = renderTree('', 0)
  }

  return (
    <aside
      data-testid="right-sidebar"
      style={{
        width,
        maxWidth: `max(${RIGHT_SIDEBAR_MIN_WIDTH}px, calc(100vw - ${
          (leftSidebarCollapsed ? SIDEBAR_COLLAPSED_WIDTH : leftSidebarWidth) + MIN_CHAT_WIDTH
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
        onPointerEnter={(event) => trackResizeShadow(event.currentTarget, event.clientY)}
        onPointerMove={(event) => trackResizeShadow(event.currentTarget, event.clientY)}
        onDoubleClick={() => {
          setWidth(RIGHT_SIDEBAR_DEFAULT_WIDTH)
          persistWidth()
        }}
        title="拖动调整宽度，双击恢复默认"
        className="resize-handle absolute inset-y-0 -left-1 z-20 w-2 cursor-col-resize"
      />

      <header
        style={DRAG_REGION}
        className="flex h-9 shrink-0 items-center gap-2 border-b border-line px-3"
      >
        <FolderOpenIcon />
        <span className="shrink-0 text-xs font-medium text-fg">文件</span>
        <span className="min-w-0 flex-1 truncate text-[10px] text-subtle" title={workDir}>
          {folderName}
        </span>
        <button
          type="button"
          data-testid="file-tree-new-file"
          style={NO_DRAG}
          aria-label="新建文件"
          title="新建文件"
          onClick={() => startCreate('', false)}
          disabled={!sessionId}
          className="cursor-pointer p-1 text-subtle transition hover:bg-hover hover:text-fg disabled:cursor-not-allowed disabled:opacity-40"
        >
          <NewFileIcon />
        </button>
        <button
          type="button"
          data-testid="file-tree-new-folder"
          style={NO_DRAG}
          aria-label="新建文件夹"
          title="新建文件夹"
          onClick={() => startCreate('', true)}
          disabled={!sessionId}
          className="cursor-pointer p-1 text-subtle transition hover:bg-hover hover:text-fg disabled:cursor-not-allowed disabled:opacity-40"
        >
          <NewFolderIcon />
        </button>
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

      <div className="shrink-0 border-b border-line px-2 py-1.5">
        <input
          data-testid="file-tree-filter"
          value={filter}
          onChange={(event) => setFilter(event.target.value)}
          placeholder="搜索文件…"
          className="w-full border border-line bg-surface px-2 py-1 text-[11px] text-fg outline-none placeholder:text-subtle focus:border-accent"
        />
      </div>

      {notice && (
        <p className="shrink-0 border-b border-line bg-raised px-3 py-1 font-mono text-[10px] text-muted">
          {notice}
        </p>
      )}

      <div
        data-testid="file-tree"
        role="tree"
        aria-label="文件树"
        onContextMenu={(event) => {
          if (event.target !== event.currentTarget) return
          event.preventDefault()
          setMenu({ x: event.clientX, y: event.clientY, target: null })
        }}
        onDragOver={(event: DragEvent<HTMLDivElement>) => {
          // 拖到空白处表示移动到工作目录根。
          if (event.target !== event.currentTarget) return
          event.preventDefault()
          event.dataTransfer.dropEffect = 'move'
        }}
        onDrop={(event: DragEvent<HTMLDivElement>) => {
          if (event.target !== event.currentTarget) return
          event.preventDefault()
          const from = draggedPath.current
          draggedPath.current = null
          if (from) void moveEntry(from, '')
        }}
        className="min-h-0 flex-1 overflow-auto py-1.5"
      >
        {body}
      </div>

      {menu && (
        <ContextMenu
          x={menu.x}
          y={menu.y}
          items={buildMenuItems()}
          onClose={() => {
            setMenu(null)
            setConfirmDelete(null)
          }}
        />
      )}
    </aside>
  )
}
