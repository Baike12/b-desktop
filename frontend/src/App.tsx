import { useEffect } from 'react'
import AskUserQuestionDock from './components/AskUserQuestionDock'
import Composer from './components/Composer'
import FileTree from './components/FileTree'
import Header from './components/Header'
import MascotAvatar from './components/MascotAvatar'
import MascotStage from './components/MascotStage'
import MessageList from './components/MessageList'
import PermissionDialog from './components/PermissionDialog'
import PreviewPane from './components/PreviewPane'
import RewindPicker from './components/RewindPicker'
import SettingsDialog from './components/SettingsDialog'
import Sidebar from './components/Sidebar'
import TerminalPanel from './components/TerminalPanel'
import TodoPanel from './components/TodoPanel'
import WorkspaceView from './components/WorkspaceView'
import { useAgentEvents } from './hooks/useAgentEvents'
import { useSessionTitleEvents } from './hooks/useSessionTitleEvents'
import { applyTheme } from './lib/theme'
import { useSettingsStore } from './stores/settings'
import { useTerminalStore } from './stores/terminal'
import { useWorkbenchStore } from './stores/workbench'

// App 是三栏工作台：左侧项目与任务列表，中间对话，右侧「文件预览 + 文件树」。
//
// 右侧区域里预览在左、文件树在右，两栏宽度都可以拖动调整；
// 中间对话区始终保持挂载，切换视图后回来时消息、滚动位置与输入草稿都不丢。
export default function App() {
  const init = useWorkbenchStore((state) => state.init)
  const items = useWorkbenchStore((state) => state.items)
  const loading = useWorkbenchStore((state) => state.loading)
  const activePanel = useWorkbenchStore((state) => state.activePanel)
  const theme = useSettingsStore((state) => state.theme)
  const rightSidebarOpen = useSettingsStore((state) => state.rightSidebarOpen)

  // 没有消息时输入框悬浮在内容区中央；出现第一条消息后落回底部。
  const welcome = items.length === 0 && !loading

  useEffect(() => {
    void init()
  }, [init])

  // 跟随系统时监听外观变化；固定主题下不需要监听。
  useEffect(() => {
    if (theme !== 'system') return
    const media = window.matchMedia('(prefers-color-scheme: dark)')
    const onChange = () => applyTheme('system')
    media.addEventListener('change', onChange)
    return () => media.removeEventListener('change', onChange)
  }, [theme])

  // 订阅 "agent:event"；卸载时 EventsOff（见 hooks/useAgentEvents.ts）。
  useAgentEvents()
  // 订阅 "session:title"：会话首轮后自动命名的结果。
  useSessionTitleEvents()

  return (
    <div
      data-testid="app"
      className="app-backdrop flex h-screen gap-2 overflow-hidden p-2 pl-0 text-fg"
    >
      <Sidebar />
      <div className="floating-pane relative flex min-w-0 flex-1 overflow-hidden border border-line bg-surface">
        <div
          data-testid="chat-view"
          className={
            activePanel === 'chat'
              ? 'relative flex min-h-0 min-w-0 flex-1 flex-col'
              : 'hidden'
          }
        >
          <Header />
          {welcome ? (
            <main
              data-testid="welcome"
              className="@container flex min-h-0 flex-1 flex-col items-center justify-center px-6 pb-[10vh]"
            >
              <div className="mb-8 w-full max-w-[46rem]">
                <div className="flex items-start justify-between gap-6">
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      <MascotAvatar className="size-5" />
                      <span className="font-mono text-[10px] uppercase tracking-[0.2em] text-subtle">
                        B Desktop
                      </span>
                    </div>
                    <h1 className="mt-4 text-[28px] font-semibold tracking-[-0.025em] text-fg @max-[560px]:text-[24px]">
                      开始新的任务
                    </h1>
                    <p className="mt-2 max-w-xl text-[13px] leading-relaxed text-muted">
                      描述目标，Agent 会在当前项目中读写文件并执行命令。
                    </p>
                  </div>
                  <MascotStage className="mt-0.5 size-28 shrink-0 sm:size-32 @max-[560px]:hidden" />
                </div>
              </div>
              <AskUserQuestionDock />
              <Composer variant="hero" />
            </main>
          ) : (
            <>
              <TodoPanel />
              <MessageList />
              <AskUserQuestionDock />
              <Composer />
            </>
          )}
          <TerminalDrawer />
        </div>
        {activePanel !== 'chat' && <WorkspaceView panel={activePanel} />}
        {rightSidebarOpen && (
          <>
            <PreviewPane />
            <FileTree />
          </>
        )}
      </div>
      <PermissionDialog />
      <RewindPicker />
      <SettingsDialog />
    </div>
  )
}

// TerminalDrawer 只在对话视图渲染终端抽屉；store.open 控制展开。
// 挂载本身零成本：未展开时组件直接返回 null。
function TerminalDrawer() {
  const activePanel = useWorkbenchStore((state) => state.activePanel)
  const terminalOpen = useTerminalStore((state) => state.open)
  if (activePanel !== 'chat' || !terminalOpen) return null
  return <TerminalPanel />
}
