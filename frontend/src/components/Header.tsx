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
    <header className="shrink-0 border-b border-line bg-panel px-5 py-2.5">
      <div style={DRAG_REGION}>
        <div className="flex items-center gap-3">
          <h1 className="min-w-0 flex-1 truncate text-[13px] font-medium tracking-tight text-fg">
            {title || '未选择会话'}
          </h1>
          {running && (
            <span className="flex shrink-0 items-center gap-1.5 border border-accent/40 bg-accent-soft px-2 py-0.5 font-mono text-[10px] tracking-[0.14em] text-accent-text">
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
                ? 'shrink-0 cursor-pointer bg-hover p-1.5 text-fg'
                : 'shrink-0 cursor-pointer p-1.5 text-subtle transition hover:bg-hover hover:text-fg'
            }
          >
            <FilesPanelIcon open={rightSidebarOpen} />
          </button>
        </div>

        <div className="mt-1.5 flex min-w-0 items-center gap-2 font-mono text-[10px] text-subtle">
          <span className="shrink-0 uppercase tracking-[0.14em]">cwd</span>
          <span className="truncate" title={workDir}>
            {workDir || '未设置工作目录'}
          </span>
        </div>
      </div>

      {error && (
        <p
          role="alert"
          className="mt-2 border border-danger-line bg-danger-soft px-3 py-1.5 text-[11px] text-danger-text"
        >
          {error}
        </p>
      )}
    </header>
  )
}
