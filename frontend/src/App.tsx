import { useEffect } from 'react'
import Composer from './components/Composer'
import FilePreviewView from './components/FilePreviewView'
import Header from './components/Header'
import MessageList from './components/MessageList'
import PermissionDialog from './components/PermissionDialog'
import RightSidebar from './components/RightSidebar'
import SettingsDialog from './components/SettingsDialog'
import Sidebar from './components/Sidebar'
import WorkspaceView from './components/WorkspaceView'
import { useAgentEvents } from './hooks/useAgentEvents'
import { useSessionTitleEvents } from './hooks/useSessionTitleEvents'
import { applyTheme } from './lib/theme'
import { useSettingsStore } from './stores/settings'
import { useWorkbenchStore } from './stores/workbench'

export default function App() {
  const init = useWorkbenchStore((state) => state.init)
  const items = useWorkbenchStore((state) => state.items)
  const loading = useWorkbenchStore((state) => state.loading)
  const activePanel = useWorkbenchStore((state) => state.activePanel)
  const preview = useWorkbenchStore((state) => state.preview)
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
    <div data-testid="app" className="flex h-screen overflow-hidden bg-surface text-fg">
      <Sidebar />
      <div className="flex min-w-0 flex-1 flex-col">
        {/* 对话区保持挂载，切换视图后回来时消息、滚动位置与输入草稿都不丢。 */}
        <div
          data-testid="chat-view"
          className={
            activePanel === 'chat' && !preview
              ? 'flex min-h-0 flex-1 flex-col'
              : 'hidden'
          }
        >
          <Header />
          {welcome ? (
            <main
              data-testid="welcome"
              className="flex min-h-0 flex-1 flex-col items-center justify-center px-6 pb-[12vh]"
            >
              <div className="mb-8 text-center">
                <p className="text-xl font-medium tracking-tight text-fg">开始新的任务</p>
                <p className="mt-2 text-xs text-subtle">
                  描述目标，Agent 会在当前项目中读写文件并执行命令
                </p>
              </div>
              <Composer variant="hero" />
            </main>
          ) : (
            <>
              <MessageList />
              <Composer />
            </>
          )}
        </div>
        {activePanel !== 'chat' && !preview && <WorkspaceView panel={activePanel} />}
        {preview && <FilePreviewView />}
      </div>
      {rightSidebarOpen && <RightSidebar />}
      <PermissionDialog />
      <SettingsDialog />
    </div>
  )
}
