import { DRAG_REGION, NO_DRAG } from '../lib/platform'
import { useSettingsStore } from '../stores/settings'
import { useWorkbenchStore } from '../stores/workbench'
import FilesPanelIcon from './FilesPanelIcon'

// Header 只显示当前会话标题，高度与右侧预览/文件树头部一致（h-9）。
export default function Header() {
  const title = useWorkbenchStore((state) => state.title)
  const running = useWorkbenchStore((state) => state.running)
  const step = useWorkbenchStore((state) => state.step)
  const error = useWorkbenchStore((state) => state.error)
  const rightSidebarOpen = useSettingsStore((state) => state.rightSidebarOpen)
  const toggleRightSidebar = useSettingsStore((state) => state.toggleRightSidebar)

  return (
    <header className="relative flex h-9 shrink-0 items-center gap-2 border-b border-line bg-surface px-4">
      <div style={DRAG_REGION} className="flex min-w-0 flex-1 items-center gap-2">
        <h1
          className="min-w-0 flex-1 truncate text-[13px] font-medium tracking-tight text-fg"
          title={title || undefined}
        >
          {title || '未选择会话'}
        </h1>
        {running && (
          <span className="flex shrink-0 items-center gap-1.5 font-mono text-[10px] tracking-[0.14em] text-accent-text">
            <span className="size-1.5 animate-pulse bg-accent" />
            运行中{step > 0 ? ` · ${step}` : ''}
          </span>
        )}
        <button
          type="button"
          data-testid="right-sidebar-toggle"
          style={NO_DRAG}
          aria-pressed={rightSidebarOpen}
          aria-label={rightSidebarOpen ? '隐藏文件栏' : '显示文件栏'}
          title={rightSidebarOpen ? '隐藏文件栏' : '显示文件栏'}
          onClick={toggleRightSidebar}
          className={
            rightSidebarOpen
              ? 'shrink-0 cursor-pointer bg-hover p-1 text-fg'
              : 'shrink-0 cursor-pointer p-1 text-subtle transition hover:bg-hover hover:text-fg'
          }
        >
          <FilesPanelIcon open={rightSidebarOpen} />
        </button>
      </div>

      {error && (
        <p
          role="alert"
          className="absolute inset-x-0 top-full z-20 border-b border-danger-line bg-danger-soft px-4 py-1.5 text-[11px] text-danger-text"
        >
          {error}
        </p>
      )}
    </header>
  )
}
