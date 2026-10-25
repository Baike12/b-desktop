import { useCallback, useEffect, useMemo, useRef, useState, type PointerEvent as ReactPointerEvent } from 'react'

import { formatBytes } from '../lib/format'
import { detectLanguage } from '../lib/highlight'
import { DRAG_REGION, NO_DRAG } from '../lib/platform'
import { trackResizeShadow } from '../lib/resize'
import {
  MIN_CHAT_WIDTH,
  PREVIEW_PANE_MIN_WIDTH,
  SIDEBAR_COLLAPSED_WIDTH,
  useSettingsStore,
} from '../stores/settings'
import { CHANGES_TAB, usePreviewStore } from '../stores/preview'
import { useWorkbenchStore } from '../stores/workbench'
import ChangesPanel from './ChangesPanel'
import CodeEditor from './CodeEditor'
import CodeView from './CodeView'
import { MarkdownText } from './MarkdownText'

function baseName(path: string): string {
  const parts = path.split(/[\\/]/).filter(Boolean)
  return parts[parts.length - 1] ?? path
}

// isMarkdownPath 判断文件是否用 Markdown 渲染；MDX 仍按普通文本预览。
function isMarkdownPath(path: string): boolean {
  return /\.(md|markdown)$/i.test(path)
}

function CloseIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      className="size-3 shrink-0"
      aria-hidden="true"
    >
      <path d="M18 6 6 18M6 6l12 12" />
    </svg>
  )
}

function PencilIcon() {
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
      <path d="M12 20h9" />
      <path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z" />
    </svg>
  )
}

function GitIcon() {
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
      <circle cx="6" cy="6" r="2.5" />
      <circle cx="6" cy="18" r="2.5" />
      <circle cx="18" cy="9" r="2.5" />
      <path d="M6 8.5v7" />
      <path d="M8.5 6h4a2 2 0 0 1 2 2v1" />
    </svg>
  )
}

function CopyIcon() {
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
      <rect x="9" y="9" width="11" height="11" rx="2" />
      <path d="M5 15V6a2 2 0 0 1 2-2h9" />
    </svg>
  )
}

// PreviewPane 是右侧面板里的文件预览区：顶部是打开的文件 tab，正文是文件内容。
//
// 它与右侧文件树并排：预览在左、文件树在右，两栏宽度都可以拖动调整。
export default function PreviewPane() {
  const tabs = usePreviewStore((state) => state.tabs)
  const active = usePreviewStore((state) => state.active)
  const files = usePreviewStore((state) => state.files)
  const changes = usePreviewStore((state) => state.changes)
  const setActive = usePreviewStore((state) => state.setActive)
  const closeTab = usePreviewStore((state) => state.closeTab)
  const openChanges = usePreviewStore((state) => state.openChanges)
  const refreshChanges = usePreviewStore((state) => state.refreshChanges)
  const saveFile = usePreviewStore((state) => state.saveFile)
  const discardDraft = usePreviewStore((state) => state.discardDraft)
  const setDraft = usePreviewStore((state) => state.setDraft)

  const activeId = useWorkbenchStore((state) => state.activeId)
  const workDir = useWorkbenchStore((state) => state.workDir)
  const bumpFilesRevision = useWorkbenchStore((state) => state.bumpFilesRevision)

  const width = useSettingsStore((state) => state.previewPaneWidth)
  const setWidth = useSettingsStore((state) => state.setPreviewPaneWidth)
  const persistWidth = useSettingsStore((state) => state.persistPreviewPaneWidth)
  const rightSidebarWidth = useSettingsStore((state) => state.rightSidebarWidth)
  const sidebarCollapsed = useSettingsStore((state) => state.sidebarCollapsed)
  const sidebarWidth = useSettingsStore((state) => state.sidebarWidth)

  // editing 是正在编辑的文件路径；null 表示处于只读预览态。
  const [editing, setEditing] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  // markdownRaw 记录被手动切到原文的 Markdown 文件；默认按渲染态打开。
  const [markdownRaw, setMarkdownRaw] = useState<ReadonlySet<string>>(() => new Set())
  const sessionRef = useRef(activeId)

  // 切换会话时清空已打开的文件：文件路径属于某个工作目录，不能跨会话复用。
  useEffect(() => {
    if (sessionRef.current === activeId) return
    sessionRef.current = activeId
    setEditing(null)
    setNotice(null)
    setMarkdownRaw(new Set())
    usePreviewStore.setState({ tabs: [], active: null, files: {} })
  }, [activeId])

  // 会话或工作目录变化时刷新 git 变更：tab 栏上的徽标要立刻反映真实改动数。
  useEffect(() => {
    if (!activeId) return
    void refreshChanges()
  }, [activeId, workDir, refreshChanges])

  // 操作提示几秒后自动消失，不长期占着面板。
  useEffect(() => {
    if (!notice) return
    const timer = window.setTimeout(() => setNotice(null), 3000)
    return () => window.clearTimeout(timer)
  }, [notice])

  // 预览面板宽度要跟着窗口变化收敛，保证对话区留得下。
  const [viewportWidth, setViewportWidth] = useState(() => window.innerWidth)
  useEffect(() => {
    const onResize = () => setViewportWidth(window.innerWidth)
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [])

  const maxWidth = useMemo(() => {
    const leftRail = sidebarCollapsed ? SIDEBAR_COLLAPSED_WIDTH : sidebarWidth
    return Math.max(
      PREVIEW_PANE_MIN_WIDTH,
      viewportWidth - leftRail - rightSidebarWidth - MIN_CHAT_WIDTH,
    )
  }, [rightSidebarWidth, sidebarCollapsed, sidebarWidth, viewportWidth])

  const effectiveWidth = Math.min(width, maxWidth)

  const startResize = useCallback(
    (event: ReactPointerEvent<HTMLDivElement>) => {
      event.preventDefault()
      const handle = event.currentTarget
      const startX = event.clientX
      const startWidth = effectiveWidth
      const previousSelect = document.body.style.userSelect
      document.body.style.userSelect = 'none'
      document.body.classList.add('is-resizing')
      handle.dataset.resizing = 'true'
      trackResizeShadow(handle, event.clientY)
      const onMove = (moveEvent: PointerEvent) => {
        trackResizeShadow(handle, moveEvent.clientY)
        const next = startWidth + (startX - moveEvent.clientX)
        setWidth(Math.min(maxWidth, Math.max(PREVIEW_PANE_MIN_WIDTH, next)))
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
    },
    [effectiveWidth, maxWidth, persistWidth, setWidth],
  )

  const file = active && active !== CHANGES_TAB ? files[active] : undefined
  const isImage = Boolean(file?.image)
  const language = useMemo(
    () => (file && !file.image ? detectLanguage(file.path) : null),
    [file],
  )
  // Markdown 文件默认渲染；用户切到原文后只影响当前 tab。
  const markdown = Boolean(
    file && isMarkdownPath(file.path) && !file.loading && !file.error && !file.binary && !file.image,
  )
  const showMarkdownSource = Boolean(file && markdown && markdownRaw.has(file.path))

  const setMarkdownRawMode = (path: string, raw: boolean) => {
    setMarkdownRaw((prev) => {
      const next = new Set(prev)
      if (raw) next.add(path)
      else next.delete(path)
      return next
    })
  }

  // 关闭 tab 后忘掉该文件的显示方式，重新打开时恢复默认的渲染态。
  const forgetMarkdownMode = (path: string) => {
    setMarkdownRaw((prev) => {
      if (!prev.has(path)) return prev
      const next = new Set(prev)
      next.delete(path)
      return next
    })
  }

  const startEdit = () => {
    if (!file) return
    setEditing(file.path)
  }

  const cancelEdit = () => {
    if (editing) discardDraft(editing)
    setEditing(null)
  }

  const commitEdit = async () => {
    if (!editing) return
    const current = files[editing]
    if (!current) return
    const draft = current.draft ?? current.content
    const ok = await saveFile(editing, draft)
    if (ok) {
      setEditing(null)
      setNotice('已保存')
      bumpFilesRevision()
    } else {
      setNotice('保存失败')
    }
  }

  const copyPath = async () => {
    if (!file) return
    try {
      await navigator.clipboard.writeText(file.path)
      setNotice('已复制路径')
    } catch {
      setNotice('复制失败')
    }
  }

  const changeCount = changes.list.length

  return (
    <section
      data-testid="preview-pane"
      style={{ width: effectiveWidth }}
      className="relative flex min-w-0 shrink-0 flex-col border-l border-r border-line bg-surface"
    >
      <div
        role="separator"
        aria-orientation="vertical"
        aria-label="调整预览宽度"
        data-testid="preview-pane-resize"
        onPointerDown={startResize}
        onPointerEnter={(event) => trackResizeShadow(event.currentTarget, event.clientY)}
        onPointerMove={(event) => trackResizeShadow(event.currentTarget, event.clientY)}
        onDoubleClick={() => {
          setWidth(Math.min(maxWidth, Math.round(viewportWidth * 0.32)))
          persistWidth()
        }}
        title="拖动调整宽度，双击恢复默认"
        className="resize-handle absolute inset-y-0 -left-1 z-20 w-2 cursor-col-resize"
      />

      <header
        style={DRAG_REGION}
        className="flex h-10 shrink-0 items-center gap-1 border-b border-line/70 bg-panel px-2"
      >
        <button
          type="button"
          data-testid="preview-tab-changes"
          style={NO_DRAG}
          onClick={() => void openChanges()}
          className={`flex h-7 shrink-0 cursor-pointer items-center gap-1.5 rounded-md px-2 text-[11px] transition ${
            active === CHANGES_TAB
              ? 'bg-surface text-fg shadow-card'
              : 'text-muted hover:bg-hover hover:text-fg'
          }`}
          title="审阅工作区的 git 变更"
        >
          <GitIcon />
          变更
          {changeCount > 0 && (
            <span className="font-mono text-[10px] tabular-nums text-subtle">{changeCount}</span>
          )}
        </button>

        <div className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto">
          {tabs
            .filter((tab) => tab !== CHANGES_TAB)
            .map((tab) => {
              const tabFile = files[tab]
              const isActive = active === tab
              const dirty = tabFile?.draft !== null && tabFile?.draft !== undefined
              return (
                <div
                  key={tab}
                  data-testid="preview-tab"
                  data-path={tab}
                  onClick={() => setActive(tab)}
                  className={`group flex h-7 shrink-0 cursor-pointer items-center gap-1.5 rounded-md pl-2 pr-1 text-[11px] transition ${
                    isActive ? 'bg-surface text-fg shadow-card' : 'text-muted hover:bg-hover hover:text-fg'
                  }`}
                >
                  <span className="max-w-[180px] truncate font-mono text-[11px]">{baseName(tab)}</span>
                  {dirty && (
                    <span
                      className="size-1.5 shrink-0 rounded-full bg-warn-text"
                      title="有未保存的修改"
                      aria-label="有未保存的修改"
                    />
                  )}
                  <button
                    type="button"
                    aria-label={`关闭 ${baseName(tab)}`}
                    onClick={(event) => {
                      event.stopPropagation()
                      closeTab(tab)
                      if (editing === tab) setEditing(null)
                      forgetMarkdownMode(tab)
                    }}
                    className="shrink-0 cursor-pointer rounded-sm p-0.5 text-subtle opacity-0 transition hover:bg-hover hover:text-fg group-hover:opacity-100"
                  >
                    <CloseIcon />
                  </button>
                </div>
              )
            })}
        </div>

        <div className="flex shrink-0 items-center gap-1">
          {file && markdown && editing !== file.path && (
            <div
              data-testid="markdown-view-toggle"
              data-mode={showMarkdownSource ? 'source' : 'rendered'}
              role="group"
              aria-label="Markdown 显示方式"
              className="flex items-center rounded-md border border-line/70 bg-panel p-0.5"
            >
              <button
                type="button"
                data-testid="markdown-view-rendered"
                aria-pressed={!showMarkdownSource}
                onClick={() => setMarkdownRawMode(file.path, false)}
                className={`cursor-pointer rounded-sm px-2 py-0.5 text-[11px] transition ${
                  !showMarkdownSource
                    ? 'bg-surface text-fg shadow-card'
                    : 'text-muted hover:bg-hover hover:text-fg'
                }`}
              >
                渲染
              </button>
              <button
                type="button"
                data-testid="markdown-view-source"
                aria-pressed={showMarkdownSource}
                onClick={() => setMarkdownRawMode(file.path, true)}
                className={`cursor-pointer rounded-sm px-2 py-0.5 text-[11px] transition ${
                  showMarkdownSource
                    ? 'bg-surface text-fg shadow-card'
                    : 'text-muted hover:bg-hover hover:text-fg'
                }`}
              >
                原文
              </button>
            </div>
          )}
          {file && editing !== file.path && (
            <button
              type="button"
              data-testid="preview-edit"
              style={NO_DRAG}
              onClick={startEdit}
              disabled={file.binary || isImage}
              title="编辑这个文件"
              className="cursor-pointer rounded-md p-1 text-subtle transition hover:bg-hover hover:text-fg disabled:cursor-not-allowed disabled:opacity-40"
            >
              <PencilIcon />
            </button>
          )}
          {file && (
            <button
              type="button"
              data-testid="preview-copy-path"
              style={NO_DRAG}
              onClick={() => void copyPath()}
              title="复制文件路径"
              className="cursor-pointer rounded-md p-1 text-subtle transition hover:bg-hover hover:text-fg"
            >
              <CopyIcon />
            </button>
          )}
        </div>
      </header>

      {notice && (
        <p className="shrink-0 border-b border-line/70 bg-raised px-3 py-1 font-mono text-[10px] text-muted">
          {notice}
        </p>
      )}

      <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
        {active === CHANGES_TAB ? (
          <ChangesPanel />
        ) : !active ? (
          <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-2 px-6 text-center">
            <p className="text-xs text-subtle">在右侧文件树里选择一个文件</p>
            <p className="text-[11px] text-subtle">也可以打开「变更」审阅工作区的 git 改动</p>
          </div>
        ) : !file ? (
          <div className="flex min-h-0 flex-1 items-center justify-center text-xs text-subtle">
            正在读取…
          </div>
        ) : file.loading ? (
          <div className="flex min-h-0 flex-1 items-center justify-center text-xs text-subtle">
            正在读取…
          </div>
        ) : file.error ? (
          <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-2 px-6 text-center">
            <p className="text-xs text-danger-text">无法预览这个文件</p>
            <p className="max-w-xl break-words text-[11px] text-subtle">{file.error}</p>
          </div>
        ) : file.image ? (
          <>
            <div className="flex shrink-0 items-center gap-2 border-b border-line/70 bg-panel px-3 py-1">
              <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-muted" title={file.path}>
                {file.path}
              </span>
              <span className="shrink-0 text-[10px] tabular-nums text-subtle">
                {formatBytes(file.size)}
              </span>
            </div>
            <div className="image-canvas flex min-h-0 flex-1 items-center justify-center overflow-auto p-4">
              <img
                data-testid="file-preview-image"
                src={`data:${file.image.mediaType};base64,${file.image.base64}`}
                alt={file.path}
                draggable={false}
                className="max-h-full max-w-full object-contain"
              />
            </div>
          </>
        ) : file.binary ? (
          <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-1 px-6 text-center">
            <p className="text-xs text-fg">二进制文件</p>
            <p className="text-[11px] text-subtle">{formatBytes(file.size)} · 预览仅支持文本与图片</p>
          </div>
        ) : editing === file.path ? (
          <CodeEditor
            value={file.draft ?? file.content}
            onChange={(value) => setDraft(file.path, value)}
            onSave={() => void commitEdit()}
            onCancel={cancelEdit}
          />
        ) : (
          <>
            <div className="flex shrink-0 items-center gap-2 border-b border-line/70 bg-panel px-3 py-1">
              <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-muted" title={file.path}>
                {file.path}
              </span>
              <span className="shrink-0 text-[10px] tabular-nums text-subtle">
                {formatBytes(file.size)}
              </span>
            </div>
            {file.truncated && (
              <p className="shrink-0 border-b border-line bg-warn-soft px-3 py-1 text-[11px] text-warn-text">
                文件较大，预览已截断。
              </p>
            )}
            {markdown && !showMarkdownSource ? (
              <div
                data-testid="file-preview-markdown"
                className="min-h-0 flex-1 overflow-auto"
              >
                <div className="mx-auto w-full max-w-[52rem] px-5 py-6">
                  <MarkdownText text={file.content} />
                </div>
              </div>
            ) : (
              <CodeView
                content={file.content}
                language={language}
                testId="file-preview-content"
              />
            )}
          </>
        )}
      </div>
    </section>
  )
}
