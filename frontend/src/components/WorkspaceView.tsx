import { useState } from 'react'
import type { app } from '../../wailsjs/go/models'
import { DRAG_REGION, NO_DRAG } from '../lib/platform'
import { useWorkbenchStore, type PanelId } from '../stores/workbench'
import { useSettingsStore } from '../stores/settings'
import ExtensionsPanel, { type ExtensionsTabId } from './ExtensionsPanel'
import FilesPanelIcon from './FilesPanelIcon'
import ImagePanel from './ImagePanel'
import SearchPanel from './SearchPanel'
import SkillMarket from './SkillMarket'
import TaskPanel from './TaskPanel'
import UsagePanel from './UsagePanel'
import WorktreePanel from './WorktreePanel'

export type WorkspacePanelId = Exclude<PanelId, 'chat'>

const PANEL_META: Record<WorkspacePanelId, { title: string; hint: string }> = {
  search: { title: '搜索', hint: '在全部会话存档中查找内容、工具调用与命令' },
  tasks: { title: '定时任务', hint: '按 cron 周期自动执行任务' },
  worktrees: { title: 'Worktree', hint: '为分支创建独立的 git 工作树' },
  extensions: { title: '扩展管理', hint: '管理 MCP 服务器、Skills 与 SubAgent' },
  usage: { title: '用量统计', hint: '本地模型请求与 token 消耗' },
  image: { title: '图片生成', hint: '调用当前 Provider 的图片接口，结果落盘到本地' },
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

const EXTENSIONS_MODES = [
  { id: 'manage', label: '扩展' },
  { id: 'market', label: '技能市场' },
] as const

type ExtensionsMode = (typeof EXTENSIONS_MODES)[number]['id']

interface WorkspaceViewProps {
  panel: WorkspacePanelId
}

// WorkspaceView 承载对话之外的一级视图：统一标题栏、返回对话按钮与内容区。
// 视图切换只改变 activePanel，会话状态仍由 workbench store 保存。
export default function WorkspaceView({ panel }: WorkspaceViewProps) {
  const setPanel = useWorkbenchStore((state) => state.setPanel)
  const workDir = useWorkbenchStore((state) => state.workDir)
  const sessions = useWorkbenchStore((state) => state.sessions)
  const selectSession = useWorkbenchStore((state) => state.selectSession)
  const newSession = useWorkbenchStore((state) => state.newSession)
  const refreshSessions = useWorkbenchStore((state) => state.refreshSessions)
  const rightSidebarOpen = useSettingsStore((state) => state.rightSidebarOpen)
  const toggleRightSidebar = useSettingsStore((state) => state.toggleRightSidebar)

  const [extensionsMode, setExtensionsMode] = useState<ExtensionsMode>('manage')
  const [extensionsTab, setExtensionsTab] = useState<ExtensionsTabId>('mcp')

  const meta = PANEL_META[panel]

  // 搜索结果点击后直接恢复该会话；selectSession 会把视图切回对话。
  const openSession = (sessionId: string) => {
    void selectSession(sessionId)
  }

  // 打开工作树：已有会话则复用，否则在该路径新建会话并切回对话。
  const openWorktree = (worktree: app.WorktreeDTO) => {
    const existing = sessions.find((session) => session.workDir === worktree.path)
    if (existing) {
      void selectSession(existing.id)
      return
    }
    void newSession(worktree.path)
  }

  // 技能市场里点卡片后回到 Skills 标签；key 变化触发 ExtensionsPanel 重新挂载。
  const openSkill = () => {
    setExtensionsTab('skills')
    setExtensionsMode('manage')
  }

  const scrollsInPage = panel === 'usage' || panel === 'image'

  return (
    <div data-testid="workspace-view" data-panel={panel} className="flex min-h-0 flex-1 flex-col bg-surface">
      <header
        style={DRAG_REGION}
        className="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-2 border-b border-line bg-panel px-4 py-2.5"
      >
        <button
          type="button"
          data-testid="panel-back"
          style={NO_DRAG}
          onClick={() => setPanel('chat')}
          className="flex cursor-pointer items-center gap-1 rounded-md border border-line px-2 py-1 text-xs text-muted transition hover:bg-hover hover:text-fg"
        >
          <BackIcon />
          返回对话
        </button>

        <div className="min-w-0 flex-1">
          <h1 className="truncate text-sm font-medium text-fg">{meta.title}</h1>
          <p className="truncate text-[11px] text-subtle">{meta.hint}</p>
        </div>

        {panel === 'extensions' && (
          <div
            role="tablist"
            aria-label="扩展视图"
            style={NO_DRAG}
            className="flex shrink-0 gap-1 rounded-lg border border-line bg-surface p-0.5"
          >
            {EXTENSIONS_MODES.map((mode) => {
              const active = mode.id === extensionsMode
              return (
                <button
                  key={mode.id}
                  type="button"
                  role="tab"
                  aria-selected={active}
                  data-testid={`extensions-mode-${mode.id}`}
                  onClick={() => setExtensionsMode(mode.id)}
                  className={`cursor-pointer rounded-md px-2.5 py-1 text-[11px] transition ${
                    active ? 'bg-hover text-fg' : 'text-subtle hover:text-fg'
                  }`}
                >
                  {mode.label}
                </button>
              )
            })}
          </div>
        )}

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

      {scrollsInPage ? (
        <main className="min-h-0 flex-1 overflow-y-auto px-4 py-4">
          <div className="mx-auto w-full max-w-3xl">
            {panel === 'usage' && <UsagePanel />}
            {panel === 'image' && <ImagePanel />}
          </div>
        </main>
      ) : (
        <main className="min-h-0 flex-1 overflow-hidden">
          {panel === 'search' && <SearchPanel className="h-full" onOpenSession={openSession} />}
          {panel === 'tasks' && (
            <TaskPanel
              className="h-full"
              defaultWorkDir={workDir}
              onChanged={() => void refreshSessions()}
            />
          )}
          {panel === 'worktrees' && (
            <WorktreePanel
              className="h-full"
              defaultRepo={workDir}
              onOpenWorktree={openWorktree}
            />
          )}
          {panel === 'extensions' && (
            <div className="h-full min-h-0 overflow-hidden p-3">
              {extensionsMode === 'manage' ? (
                <ExtensionsPanel
                  key={extensionsTab}
                  initialTab={extensionsTab}
                  className="h-full"
                />
              ) : (
                <SkillMarket className="h-full" onSelect={openSkill} />
              )}
            </div>
          )}
        </main>
      )}
    </div>
  )
}
