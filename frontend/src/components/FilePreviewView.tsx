import { useMemo } from 'react'
import { formatBytes } from '../lib/format'
import { DRAG_REGION, NO_DRAG } from '../lib/platform'
import { useSettingsStore } from '../stores/settings'
import { useWorkbenchStore } from '../stores/workbench'
import FilesPanelIcon from './FilesPanelIcon'

// MAX_PREVIEW_LINES 限制单次渲染的行数，超大文件仍可滚动前缀，避免 DOM 过大。
const MAX_PREVIEW_LINES = 4000

function BackIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-3.5 shrink-0"
      aria-hidden="true"
    >
      <path d="M15 18l-6-6 6-6" />
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

// FilePreviewView 在中间主区域显示右侧文件树打开的只读文件。
// 与对话区互斥：对话保持挂载，返回后消息与滚动位置不丢。
export default function FilePreviewView() {
  const preview = useWorkbenchStore((state) => state.preview)
  const closePreview = useWorkbenchStore((state) => state.closePreview)
  const openFile = useWorkbenchStore((state) => state.openFile)
  const rightSidebarOpen = useSettingsStore((state) => state.rightSidebarOpen)
  const toggleRightSidebar = useSettingsStore((state) => state.toggleRightSidebar)

  const view = useMemo(() => {
    if (!preview) return null
    const lines = preview.content.split('\n')
    const visible = lines.length > MAX_PREVIEW_LINES ? lines.slice(0, MAX_PREVIEW_LINES) : lines
    return {
      lines: visible.join('\n'),
      gutter: visible.map((_, index) => index + 1).join('\n'),
      lineCapped: lines.length > MAX_PREVIEW_LINES,
      empty: preview.content.length === 0,
    }
  }, [preview])

  if (!preview || !view) return null

  const body = () => {
    if (preview.loading) {
      return (
        <div className="flex min-h-0 flex-1 items-center justify-center text-xs text-subtle">
          正在读取…
        </div>
      )
    }
    if (preview.error) {
      return (
        <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-2 px-6 text-center">
          <p className="text-xs text-danger-text">无法预览这个文件</p>
          <p className="max-w-xl break-words text-[11px] text-subtle">{preview.error}</p>
          <button
            type="button"
            onClick={() => void openFile(preview.path)}
            className="mt-1 cursor-pointer border border-line px-2.5 py-1 text-[11px] text-muted hover:bg-hover hover:text-fg"
          >
            重试
          </button>
        </div>
      )
    }
    if (preview.binary) {
      return (
        <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-1 px-6 text-center">
          <p className="text-xs text-fg">二进制文件</p>
          <p className="text-[11px] text-subtle">
            {formatBytes(preview.size)} · 文件栏只预览文本内容
          </p>
        </div>
      )
    }
    if (view.empty) {
      return (
        <div className="flex min-h-0 flex-1 items-center justify-center text-xs text-subtle">
          空文件
        </div>
      )
    }
    return (
      <div className="min-h-0 flex-1 overflow-auto bg-code">
        <div className="flex min-w-max py-3 font-mono text-xs leading-5">
          <pre
            aria-hidden="true"
            className="sticky left-0 shrink-0 select-none border-r border-line bg-code px-3 text-right text-subtle"
          >
            {view.gutter}
          </pre>
          <pre data-testid="file-preview-content" className="whitespace-pre px-3 text-fg">
            {view.lines}
          </pre>
        </div>
      </div>
    )
  }

  return (
    <div data-testid="file-preview" className="flex min-h-0 flex-1 flex-col bg-surface">
      <header
        style={DRAG_REGION}
        className="flex shrink-0 items-center gap-3 border-b border-line bg-panel px-4 py-2.5"
      >
        <button
          type="button"
          data-testid="file-preview-back"
          style={NO_DRAG}
          onClick={closePreview}
          className="flex shrink-0 cursor-pointer items-center gap-1 border border-line px-2 py-1 text-xs text-muted transition hover:bg-hover hover:text-fg"
        >
          <BackIcon />
          返回对话
        </button>
        <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-fg" title={preview.path}>
          {preview.path}
        </span>
        <span className="shrink-0 font-mono text-[10px] tabular-nums text-subtle">
          {formatBytes(preview.size)}
        </span>
        <button
          type="button"
          data-testid="file-preview-sidebar-toggle"
          style={NO_DRAG}
          aria-pressed={rightSidebarOpen}
          aria-label={rightSidebarOpen ? '隐藏文件栏' : '显示文件栏'}
          title={rightSidebarOpen ? '隐藏文件栏' : '显示文件栏'}
          onClick={toggleRightSidebar}
          className={
            rightSidebarOpen
              ? 'shrink-0 cursor-pointer bg-hover p-1.5 text-fg'
              : 'shrink-0 cursor-pointer p-1.5 text-subtle transition hover:bg-hover hover:text-fg'
          }
        >
          <FilesPanelIcon open={rightSidebarOpen} />
        </button>
        <button
          type="button"
          data-testid="file-preview-close"
          style={NO_DRAG}
          aria-label="关闭预览"
          title="关闭预览"
          onClick={closePreview}
          className="shrink-0 cursor-pointer p-1 text-subtle transition hover:bg-hover hover:text-fg"
        >
          <CloseIcon />
        </button>
      </header>

      {(preview.truncated || view.lineCapped) && (
        <p className="shrink-0 border-b border-line bg-warn-soft px-4 py-1.5 text-[11px] text-warn-text">
          {preview.truncated ? '文件较大，预览已截断。' : `仅显示前 ${MAX_PREVIEW_LINES} 行。`}
        </p>
      )}

      {body()}
    </div>
  )
}
