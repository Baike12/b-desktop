import { DRAG_REGION, NO_DRAG } from '../lib/platform'
import { useSettingsStore } from '../stores/settings'
import { useWorkbenchStore } from '../stores/workbench'
import FilesPanelIcon from './FilesPanelIcon'

export default function Header() {
  const title = useWorkbenchStore((state) => state.title)
  const workDir = useWorkbenchStore((state) => state.workDir)
  const running = useWorkbenchStore((state) => state.running)
  const step = useWorkbenchStore((state) => state.step)
  const error = useWorkbenchStore((state) => state.error)
  const rightSidebarOpen = useSettingsStore((state) => state.rightSidebarOpen)
  const toggleRightSidebar = useSettingsStore((state) => state.toggleRightSidebar)

  return (
    <header className="shrink-0 border-b border-line bg-panel px-5 py-3">
      <div style={DRAG_REGION}>
        <div className="flex items-center gap-3">
          <h1 className="min-w-0 flex-1 truncate text-sm font-medium text-fg">
            {title || '未选择会话'}
          </h1>
          {running && (
            <span className="flex items-center gap-1.5 rounded-full bg-accent-soft px-2.5 py-1 text-[11px] text-accent-text">
              <span className="size-1.5 animate-pulse rounded-full bg-sky-400" />
              运行中{step > 0 ? ` · 第 ${step} 步` : ''}
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
                ? 'shrink-0 cursor-pointer rounded-md bg-hover p-1.5 text-fg'
                : 'shrink-0 cursor-pointer rounded-md p-1.5 text-subtle transition hover:bg-hover hover:text-fg'
            }
          >
            <FilesPanelIcon open={rightSidebarOpen} />
          </button>
        </div>

        <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-2">
          <div className="flex min-w-0 flex-1 items-center gap-2 text-[11px] text-subtle">
            <span className="truncate" title={workDir}>
              {workDir || '未设置工作目录'}
            </span>
          </div>
        </div>
      </div>

      {error && (
        <p
          role="alert"
          className="mt-2 rounded-md border border-danger-line bg-danger-soft px-3 py-1.5 text-xs text-danger-text"
        >
          {error}
        </p>
      )}
    </header>
  )
}
