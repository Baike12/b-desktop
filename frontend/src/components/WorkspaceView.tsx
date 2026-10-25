import { DRAG_REGION, NO_DRAG } from '../lib/platform'
import { useWorkbenchStore, type PanelId } from '../stores/workbench'
import { useSettingsStore } from '../stores/settings'
import FilesPanelIcon from './FilesPanelIcon'
import SearchPanel from './SearchPanel'

export type WorkspacePanelId = Exclude<PanelId, 'chat'>

const PANEL_META: Record<WorkspacePanelId, { title: string; hint: string }> = {
  search: { title: '搜索', hint: '在全部会话存档中查找内容、工具调用与命令' },
}

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

interface WorkspaceViewProps {
  panel: WorkspacePanelId
}

// WorkspaceView 承载对话之外的一级视图：统一标题栏、返回对话按钮与内容区。
// 视图切换只改变 activePanel，会话状态仍由 workbench store 保存。
export default function WorkspaceView({ panel }: WorkspaceViewProps) {
  const setPanel = useWorkbenchStore((state) => state.setPanel)
  const selectSession = useWorkbenchStore((state) => state.selectSession)
  const rightSidebarOpen = useSettingsStore((state) => state.rightSidebarOpen)
  const toggleRightSidebar = useSettingsStore((state) => state.toggleRightSidebar)

  const meta = PANEL_META[panel]

  // 搜索结果点击后直接恢复该会话；selectSession 会把视图切回对话。
  const openSession = (sessionId: string) => {
    void selectSession(sessionId)
  }

  return (
    <div data-testid="workspace-view" data-panel={panel} className="flex min-h-0 flex-1 flex-col bg-surface">
      <header
        style={DRAG_REGION}
        className="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-2 border-b border-line/70 bg-panel px-4 py-2.5"
      >
        <button
          type="button"
          data-testid="panel-back"
          style={NO_DRAG}
          onClick={() => setPanel('chat')}
          className="flex cursor-pointer items-center gap-1 rounded-lg border border-line px-2.5 py-1.5 text-xs text-muted transition hover:bg-hover hover:text-fg"
        >
          <BackIcon />
          返回对话
        </button>

        <div className="min-w-0 flex-1">
          <h1 className="truncate text-[13px] font-medium tracking-tight text-fg">{meta.title}</h1>
          <p className="truncate font-mono text-[10px] text-subtle">{meta.hint}</p>
        </div>

        <button
          type="button"
          data-testid="workspace-sidebar-toggle"
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
      </header>

      <main className="min-h-0 flex-1 overflow-hidden">
        {panel === 'search' && <SearchPanel className="h-full" onOpenSession={openSession} />}
      </main>
    </div>
  )
}
