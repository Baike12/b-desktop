import { memo, useCallback, useEffect, useMemo, useState, type ReactElement } from 'react'
import type { app } from '../../wailsjs/go/models'
import { formatRelative } from '../lib/format'
import { DRAG_REGION, IS_MAC, NO_DRAG } from '../lib/platform'
import { groupSessionsByProject } from '../lib/projects'
import { useAppStore } from '../stores/app'
import { useSettingsStore } from '../stores/settings'
import { useWorkbenchStore, type PanelId } from '../stores/workbench'

const statusLabel = {
  idle: '正在连接后端…',
  ready: '后端已连接',
  error: '后端未连接',
} as const

const statusColor = {
  idle: 'bg-amber-500',
  ready: 'bg-emerald-500',
  error: 'bg-rose-500',
} as const

function GearIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-4 shrink-0"
      aria-hidden="true"
    >
      <circle cx="12" cy="12" r="3" />
      <path d="M19.4 15a1.7 1.7 0 0 0 .3 1.9l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.9-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.9.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.9 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.9l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.9.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.9-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.9V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1Z" />
    </svg>
  )
}

function FolderIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-3.5 shrink-0"
      aria-hidden="true"
    >
      <path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2Z" />
    </svg>
  )
}

function PlusIcon({ className = 'size-4' }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      className={`shrink-0 ${className}`}
      aria-hidden="true"
    >
      <path d="M12 5v14M5 12h14" />
    </svg>
  )
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

function SearchIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-4 shrink-0"
      aria-hidden="true"
    >
      <circle cx="11" cy="11" r="7" />
      <path d="m20 20-3.5-3.5" />
    </svg>
  )
}

function ClockIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-4 shrink-0"
      aria-hidden="true"
    >
      <circle cx="12" cy="12" r="9" />
      <path d="M12 7v5l3 2" />
    </svg>
  )
}

function BranchIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-4 shrink-0"
      aria-hidden="true"
    >
      <circle cx="6" cy="6" r="2.5" />
      <circle cx="6" cy="18" r="2.5" />
      <circle cx="18" cy="8" r="2.5" />
      <path d="M6 8.5v7M18 10.5c0 3-2 5-5.5 6" />
    </svg>
  )
}

function ExtensionsIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-4 shrink-0"
      aria-hidden="true"
    >
      <rect x="3" y="3" width="7" height="7" rx="1.5" />
      <rect x="14" y="3" width="7" height="7" rx="1.5" />
      <rect x="3" y="14" width="7" height="7" rx="1.5" />
      <rect x="14" y="14" width="7" height="7" rx="1.5" />
    </svg>
  )
}

function ChartIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-4 shrink-0"
      aria-hidden="true"
    >
      <path d="M4 20V4" />
      <path d="M4 20h16" />
      <path d="M8 16v-4M12 16V8M16 16v-6M20 16v-9" />
    </svg>
  )
}

function ImageIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-4 shrink-0"
      aria-hidden="true"
    >
      <rect x="3" y="4" width="18" height="16" rx="2" />
      <circle cx="8.5" cy="9.5" r="1.5" />
      <path d="m4 17 5-5 4 4 3-3 4 4" />
    </svg>
  )
}

interface NavItem {
  id: PanelId
  label: string
  Icon: () => ReactElement
}

// 一级导航：对话之外的工作台视图，展开态显示文字，折叠态只留图标。
const NAV_ITEMS: readonly NavItem[] = [
  { id: 'search', label: '搜索', Icon: SearchIcon },
  { id: 'tasks', label: '定时任务', Icon: ClockIcon },
  { id: 'worktrees', label: 'Worktree', Icon: BranchIcon },
  { id: 'extensions', label: '扩展管理', Icon: ExtensionsIcon },
  { id: 'usage', label: '用量', Icon: ChartIcon },
  { id: 'image', label: '图片生成', Icon: ImageIcon },
]

function PanelIcon({ open }: { open: boolean }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-4 shrink-0"
      aria-hidden="true"
    >
      <rect x="3" y="4" width="18" height="16" rx="2" />
      <path d="M9 4v16" />
      {open ? <path d="m15 9-3 3 3 3" /> : <path d="m13 9 3 3-3 3" />}
    </svg>
  )
}

interface SessionRowProps {
  session: app.SessionInfoDTO
  active: boolean
  onSelect: (id: string) => void | Promise<void>
}

// SessionRow 用 memo 固定：只有自身 session 对象或选中态变化时才重渲染。
const SessionRow = memo(function SessionRow({ session, active, onSelect }: SessionRowProps) {
  return (
    <button
      type="button"
      data-testid="session-row"
      onClick={() => void onSelect(session.id)}
      title={`${session.title}\n${session.workDir}`}
      className={
        active
          ? 'w-full cursor-pointer rounded-md border border-line-strong bg-hover px-2 py-1.5 text-left'
          : 'w-full cursor-pointer rounded-md border border-transparent px-2 py-1.5 text-left hover:bg-hover'
      }
    >
      <span className="flex items-center gap-1.5">
        <span className="min-w-0 flex-1 truncate text-xs text-fg">{session.title}</span>
        {session.running && (
          <span className="size-1.5 shrink-0 animate-pulse rounded-full bg-sky-400" />
        )}
      </span>
      <span className="mt-0.5 flex items-center gap-1 text-[10px] text-subtle">
        <span className="truncate">{formatRelative(session.updatedAtMs)}</span>
        <span>·</span>
        <span>{session.messages} 条</span>
      </span>
    </button>
  )
})

interface ProjectSectionProps {
  project: app.ProjectDTO
  sessions: app.SessionInfoDTO[]
  activeId: string | null
  open: boolean
  onToggle: (path: string) => void
  onSelect: (id: string) => void | Promise<void>
  onCreate: (path: string) => void | Promise<void>
}

// ProjectSection 是侧边栏的一个项目分组：标题行可折叠，悬停出现“新建会话”。
const ProjectSection = memo(function ProjectSection({
  project,
  sessions,
  activeId,
  open,
  onToggle,
  onSelect,
  onCreate,
}: ProjectSectionProps) {
  return (
    <section data-testid="project-item" data-project={project.name} className="mt-1">
      <div className="group flex items-center gap-0.5 rounded-md px-1.5 py-1 hover:bg-hover">
        <button
          type="button"
          onClick={() => onToggle(project.path)}
          title={project.path}
          className="flex min-w-0 flex-1 cursor-pointer items-center gap-1.5 text-left"
        >
          <ChevronIcon open={open} />
          <FolderIcon />
          <span className="min-w-0 flex-1 truncate text-xs font-medium text-fg">
            {project.name}
          </span>
          {sessions.length > 0 && (
            <span className="shrink-0 text-[10px] tabular-nums text-subtle">
              {sessions.length}
            </span>
          )}
        </button>
        <button
          type="button"
          data-testid="project-new-session"
          aria-label={`在 ${project.name} 中新建会话`}
          title="新建会话"
          onClick={() => void onCreate(project.path)}
          className="cursor-pointer rounded p-1 text-subtle opacity-0 transition hover:bg-raised hover:text-fg focus:opacity-100 group-hover:opacity-100"
        >
          <PlusIcon className="size-3.5" />
        </button>
      </div>

      {open && (
        <div className="ml-3.5 space-y-0.5 border-l border-line pl-1.5">
          {sessions.length === 0 ? (
            <button
              type="button"
              onClick={() => void onCreate(project.path)}
              className="w-full cursor-pointer rounded-md px-2 py-1.5 text-left text-[11px] text-subtle hover:bg-hover hover:text-fg"
            >
              暂无会话，点击新建
            </button>
          ) : (
            sessions.map((session) => (
              <SessionRow
                key={session.id}
                session={session}
                active={session.id === activeId}
                onSelect={onSelect}
              />
            ))
          )}
        </div>
      )}
    </section>
  )
})

export default function Sidebar() {
  const sessions = useWorkbenchStore((state) => state.sessions)
  const projects = useWorkbenchStore((state) => state.projects)
  const activeId = useWorkbenchStore((state) => state.activeId)
  const workDir = useWorkbenchStore((state) => state.workDir)
  const loading = useWorkbenchStore((state) => state.loading)
  const newSession = useWorkbenchStore((state) => state.newSession)
  const openProject = useWorkbenchStore((state) => state.openProject)
  const selectSession = useWorkbenchStore((state) => state.selectSession)
  const activePanel = useWorkbenchStore((state) => state.activePanel)
  const setPanel = useWorkbenchStore((state) => state.setPanel)
  const status = useAppStore((state) => state.status)
  const openSettings = useSettingsStore((state) => state.openSettings)
  const sidebarCollapsed = useSettingsStore((state) => state.sidebarCollapsed)
  const toggleSidebar = useSettingsStore((state) => state.toggleSidebar)

  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({})
  const { groups, orphans } = useMemo(
    () => groupSessionsByProject(projects, sessions),
    [projects, sessions],
  )

  // 当前会话所属项目始终展开；“新建任务”也优先落在该项目里。
  const activeProjectPath = projects.find((project) => project.path === workDir)?.path ?? ''
  const targetPath = activeProjectPath || projects[0]?.path
  const toggleProject = useCallback((path: string) => {
    setCollapsed((state) => ({ ...state, [path]: !state[path] }))
  }, [])
  // 在折叠分组里新建会话时先展开该分组，避免新会话不可见。
  const createSession = useCallback(
    (path?: string) => {
      if (path) setCollapsed((state) => ({ ...state, [path]: false }))
      void newSession(path)
    },
    [newSession],
  )

  // 切换到某个会话时展开它所属的项目；其余分组的折叠状态保持不变。
  useEffect(() => {
    if (!activeProjectPath) return
    setCollapsed((state) =>
      state[activeProjectPath] ? { ...state, [activeProjectPath]: false } : state,
    )
  }, [activeProjectPath])

  // 折叠态只保留一条窄栏：展开、新建任务与设置仍然可达。
  if (sidebarCollapsed) {
    return (
      <aside
        data-testid="sidebar"
        data-collapsed="true"
        className="flex w-[72px] shrink-0 flex-col items-center border-r border-line bg-panel"
      >
        {/* 与展开态一样，为 macOS 红绿灯留出顶部安全区并作为拖动区域。 */}
        <div style={DRAG_REGION} className={`w-full ${IS_MAC ? 'h-11' : 'h-3'}`} />
        <button
          type="button"
          data-testid="sidebar-toggle"
          aria-label="展开侧边栏"
          title="展开侧边栏"
          onClick={toggleSidebar}
          className="mt-1 cursor-pointer rounded-lg p-2 text-subtle transition hover:bg-hover hover:text-fg"
        >
          <PanelIcon open={false} />
        </button>
        <button
          type="button"
          data-testid="new-task-button"
          aria-label="新建任务"
          title="新建任务"
          onClick={() => createSession(targetPath)}
          disabled={loading}
          className="mt-1 cursor-pointer rounded-lg border border-line-strong bg-raised p-2 text-fg transition hover:bg-hover disabled:cursor-not-allowed disabled:opacity-50"
        >
          <PlusIcon />
        </button>
        <nav aria-label="工作台面板" className="mt-1 flex flex-col items-center gap-1">
          {NAV_ITEMS.map(({ id, label, Icon }) => {
            const active = activePanel === id
            return (
              <button
                key={id}
                type="button"
                data-testid={`nav-${id}`}
                aria-label={label}
                aria-current={active ? 'page' : undefined}
                title={label}
                onClick={() => setPanel(id)}
                className={
                  active
                    ? 'cursor-pointer rounded-lg bg-hover p-2 text-fg'
                    : 'cursor-pointer rounded-lg p-2 text-subtle transition hover:bg-hover hover:text-fg'
                }
              >
                <Icon />
              </button>
            )
          })}
        </nav>
        <div className="flex-1" />
        <button
          type="button"
          data-testid="settings-button"
          aria-label="设置"
          title="设置"
          onClick={openSettings}
          className="mb-3 cursor-pointer rounded-lg p-2 text-subtle transition hover:bg-hover hover:text-fg"
        >
          <GearIcon />
        </button>
      </aside>
    )
  }

  return (
    <aside
      data-testid="sidebar"
      className="flex w-64 shrink-0 flex-col border-r border-line bg-panel"
    >
      {/* macOS 隐藏标题栏后红绿灯浮在左上角，顶部留出安全区并作为拖动区域。 */}
      <div
        style={DRAG_REGION}
        className={`border-b border-line px-4 pb-4 ${IS_MAC ? 'pt-11' : 'pt-4'}`}
      >
        <div className="flex items-start justify-between gap-2">
          <div>
            <div className="text-sm font-semibold tracking-tight text-fg">B Desktop</div>
            <div className="mt-1 flex items-center gap-1.5 text-[11px] text-subtle">
              <span className={`size-1.5 rounded-full ${statusColor[status]}`} />
              {statusLabel[status]}
            </div>
          </div>
          <button
            type="button"
            data-testid="sidebar-toggle"
            style={NO_DRAG}
            aria-label="折叠侧边栏"
            title="折叠侧边栏"
            onClick={toggleSidebar}
            className="-mr-1 cursor-pointer rounded p-1 text-subtle transition hover:bg-hover hover:text-fg"
          >
            <PanelIcon open />
          </button>
        </div>
      </div>

      <div className="space-y-1.5 px-3 py-3">
        <button
          type="button"
          data-testid="new-task-button"
          onClick={() => createSession(targetPath)}
          disabled={loading}
          className="flex w-full cursor-pointer items-center gap-2 rounded-lg border border-line-strong bg-raised px-3 py-2 text-sm text-fg hover:bg-hover disabled:cursor-not-allowed disabled:opacity-50"
        >
          <PlusIcon />
          新建任务
        </button>
        <button
          type="button"
          data-testid="open-project-button"
          onClick={() => void openProject()}
          disabled={loading}
          className="flex w-full cursor-pointer items-center gap-2 rounded-lg px-3 py-2 text-left text-sm text-muted transition hover:bg-hover hover:text-fg disabled:cursor-not-allowed disabled:opacity-50"
        >
          <FolderIcon />
          打开项目
        </button>
      </div>

      <nav
        data-testid="panel-nav"
        aria-label="工作台面板"
        className="space-y-0.5 border-t border-line px-2 py-2"
      >
        {NAV_ITEMS.map(({ id, label, Icon }) => {
          const active = activePanel === id
          return (
            <button
              key={id}
              type="button"
              data-testid={`nav-${id}`}
              aria-current={active ? 'page' : undefined}
              onClick={() => setPanel(id)}
              className={
                active
                  ? 'flex w-full cursor-pointer items-center gap-2 rounded-lg bg-hover px-3 py-1.5 text-left text-[13px] text-fg'
                  : 'flex w-full cursor-pointer items-center gap-2 rounded-lg px-3 py-1.5 text-left text-[13px] text-muted transition hover:bg-hover hover:text-fg'
              }
            >
              <Icon />
              <span className="min-w-0 flex-1 truncate">{label}</span>
            </button>
          )
        })}
      </nav>

      <div className="flex items-center justify-between px-4 pb-1.5 pt-2 text-[10px] font-medium uppercase tracking-wider text-subtle">
        <span>任务列表</span>
        <span className="tabular-nums">{sessions.length}</span>
      </div>

      <nav
        data-testid="project-list"
        className="min-h-0 flex-1 overflow-y-auto px-2 pb-4"
        aria-label="项目与会话"
      >
        {projects.length === 0 && sessions.length === 0 && (
          <p className="px-3 py-6 text-center text-xs leading-relaxed text-subtle">
            还没有项目
            <br />
            点击上方“打开项目”选择文件夹
          </p>
        )}

        {groups.map(({ project, sessions: items }) => {
          const open = !collapsed[project.path]
          return (
            <ProjectSection
              key={project.path}
              project={project}
              sessions={items}
              activeId={activeId}
              open={open}
              onToggle={toggleProject}
              onSelect={selectSession}
              onCreate={createSession}
            />
          )
        })}

        {orphans.length > 0 && (
          <section className="mt-3">
            <div className="px-2 py-1 text-[10px] font-medium uppercase tracking-wider text-subtle">
              其他会话
            </div>
            <div className="space-y-0.5">
              {orphans.map((session) => (
                <SessionRow
                  key={session.id}
                  session={session}
                  active={session.id === activeId}
                  onSelect={selectSession}
                />
              ))}
            </div>
          </section>
        )}
      </nav>

      <div className="border-t border-line p-3">
        <button
          type="button"
          data-testid="settings-button"
          onClick={openSettings}
          className="flex w-full cursor-pointer items-center gap-2 rounded-lg px-3 py-2 text-sm text-muted transition hover:bg-hover hover:text-fg"
        >
          <GearIcon />
          设置
        </button>
      </div>
    </aside>
  )
}
