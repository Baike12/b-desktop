import { DRAG_REGION, NO_DRAG } from '../lib/platform'
import { useSettingsStore } from '../stores/settings'
import { useTerminalStore } from '../stores/terminal'
import { useWorkbenchStore } from '../stores/workbench'
import FilesPanelIcon from './FilesPanelIcon'

// TerminalIcon 是终端面板开关图标。
function TerminalIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-4 shrink-0"
      aria-hidden="true"
    >
      <rect x="3" y="4" width="18" height="16" rx="2.5" />
      <path d="m7 9 3 3-3 3M12.5 15H17" />
    </svg>
  )
}

// Header 只显示当前会话标题，高度与右侧预览/文件树头部一致（h-9）。
export default function Header() {
  const title = useWorkbenchStore((state) => state.title)
  const running = useWorkbenchStore((state) => state.running)
  const step = useWorkbenchStore((state) => state.step)
  const error = useWorkbenchStore((state) => state.error)
  const rightSidebarOpen = useSettingsStore((state) => state.rightSidebarOpen)
  const toggleRightSidebar = useSettingsStore((state) => state.toggleRightSidebar)
  const terminalOpen = useTerminalStore((state) => state.open)
  const toggleTerminal = useTerminalStore((state) => state.toggle)

  return (
    <header className="relative flex h-10 shrink-0 items-center gap-2 border-b border-line/70 bg-surface px-4">
      <div style={DRAG_REGION} className="flex min-w-0 flex-1 items-center gap-2">
        <h1
          className="min-w-0 flex-1 truncate text-[13px] font-medium tracking-tight text-fg"
          title={title || undefined}
        >
          {title || '未选择会话'}
        </h1>
        {running && (
          <span className="flex shrink-0 items-center gap-1.5 font-mono text-[10px] tracking-[0.14em] text-accent-text">
            <span className="size-1.5 animate-pulse rounded-full bg-accent" />
            运行中{step > 0 ? ` · ${step}` : ''}
          </span>
        )}
        <button
          type="button"
          data-testid="terminal-toggle"
          style={NO_DRAG}
          aria-pressed={terminalOpen}
          aria-label={terminalOpen ? '关闭终端' : '打开终端'}
          title={terminalOpen ? '关闭终端' : '打开终端'}
          onClick={toggleTerminal}
          className={
            terminalOpen
              ? 'shrink-0 cursor-pointer rounded-md bg-hover p-1 text-fg'
              : 'shrink-0 cursor-pointer rounded-md p-1 text-subtle transition hover:bg-hover hover:text-fg'
          }
        >
          <TerminalIcon />
        </button>
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
              ? 'shrink-0 cursor-pointer rounded-md bg-hover p-1 text-fg'
              : 'shrink-0 cursor-pointer rounded-md p-1 text-subtle transition hover:bg-hover hover:text-fg'
          }
        >
          <FilesPanelIcon open={rightSidebarOpen} />
        </button>
      </div>

      {error && (
        <p
          role="alert"
          className="absolute inset-x-3 top-full z-20 mt-1 rounded-lg border border-danger-line bg-danger-soft px-3 py-1.5 text-[11px] text-danger-text shadow-card"
        >
          {error}
        </p>
      )}
    </header>
  )
}
