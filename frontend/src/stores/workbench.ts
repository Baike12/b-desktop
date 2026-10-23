import { create } from 'zustand'
import {
  ApproveTool,
  CancelTurn,
  CreateSession,
  GetProviderConfig,
  ListProjects,
  ListSessions,
  LoadSession,
  OpenProject,
  ReadSessionFile,
  SendMessage,
  SetPermissionMode,
} from '../../wailsjs/go/app/App'
import type { app } from '../../wailsjs/go/models'
import { addUsage, emptyUsage } from '../lib/format'
import { normalizePermissionMode } from '../lib/permission'
import { reduceAgentEvents, type StreamState } from '../lib/reduceAgentEvents'
import { viewToItems } from '../lib/sessionView'
import type { AgentEvent, ApprovalView, PermissionMode, RetryInfo, Usage } from '../types/agent'
import type { ChatItem } from '../types/chat'
import { useAppStore } from './app'

let localSeq = 0

function localId(prefix: string): string {
  localSeq += 1
  return `${prefix}-local-${localSeq}`
}

// errorMessage 把 Wails 的 reject 值（通常是 string）转成可展示文本。
export function errorMessage(err: unknown): string {
  if (err instanceof Error) return err.message
  if (typeof err === 'string') return err
  try {
    return JSON.stringify(err)
  } catch {
    return String(err)
  }
}

function toUsage(value: app.UsageDTO | undefined): Usage {
  if (!value) return emptyUsage()
  return {
    inputTokens: value.inputTokens,
    outputTokens: value.outputTokens,
    cacheReadInputTokens: value.cacheReadInputTokens ?? 0,
    cacheCreationInputTokens: value.cacheCreationInputTokens ?? 0,
  }
}

function withSystemError(items: ChatItem[], text: string): ChatItem[] {
  return [
    ...items,
    { kind: 'system', id: localId('sys'), tone: 'error' as const, text, timeMs: Date.now() },
  ]
}

// PanelId 是主内容区的一级视图；'chat' 之外的视图都是独立工作台页面。
// Worktree 不做独立视图：它挂在侧边栏项目行上，创建后直接进入会话。
export type PanelId = 'chat' | 'search'

// FilePreviewState 是右侧文件树点击文件后的只读预览。
export interface FilePreviewState {
  path: string
  loading: boolean
  content: string
  size: number
  binary: boolean
  truncated: boolean
  error: string | null
}

export interface WorkbenchState {
  // 当前主视图；切换视图不会重置会话状态。
  activePanel: PanelId
  // 会话列表
  sessions: app.SessionInfoDTO[]
  // 已打开的项目
  projects: app.ProjectDTO[]
  // 当前会话
  activeId: string | null
  title: string
  workDir: string
  model: string
  contextWindow: number
  permissionMode: PermissionMode
  // 消息流与运行状态
  items: ChatItem[]
  usage: Usage
  roundUsage: Usage | null
  contextTokens: number
  running: boolean
  step: number
  compacting: boolean
  retry: RetryInfo | null
  approvals: ApprovalView[]
  stopReason: string | null
  error: string | null
  notice: string | null
  loading: boolean
  bootstrapped: boolean
  bootstrapping: boolean
  // 中间区域的只读文件预览；null 表示显示对话。
  preview: FilePreviewState | null
  // 文件树刷新信号：每轮结束后自增，右侧栏据此重载已展开目录。
  filesRevision: number

  init: () => Promise<void>
  setPanel: (panel: PanelId) => void
  refreshSessions: () => Promise<void>
  selectSession: (id: string) => Promise<void>
  newSession: (projectPath?: string) => Promise<void>
  openProject: () => Promise<void>
  send: (prompt: string) => Promise<void>
  cancel: () => Promise<void>
  approve: (requestId: string, option: string) => Promise<void>
  setPermissionMode: (mode: PermissionMode) => Promise<void>
  applyProviderConfig: (config: app.ProviderConfigDTO) => void
  openFile: (path: string) => Promise<void>
  closePreview: () => void
  applySessionTitle: (sessionId: string, title: string) => void
  ingest: (events: AgentEvent[]) => void
}

export const useWorkbenchStore = create<WorkbenchState>((set, get) => ({
  activePanel: 'chat',
  sessions: [],
  projects: [],
  activeId: null,
  title: '',
  workDir: '',
  model: '',
  contextWindow: 0,
  permissionMode: 'default',
  items: [],
  usage: emptyUsage(),
  roundUsage: null,
  contextTokens: 0,
  running: false,
  step: 0,
  compacting: false,
  retry: null,
  approvals: [],
  stopReason: null,
  error: null,
  notice: null,
  loading: false,
  bootstrapped: false,
  bootstrapping: false,
  preview: null,
  filesRevision: 0,

  init: async () => {
    if (get().bootstrapping || get().bootstrapped) return
    set({ bootstrapping: true })
    useAppStore.getState().setStatus('idle')
    try {
      const [sessions, projects, config] = await Promise.all([
        ListSessions(),
        ListProjects(),
        GetProviderConfig(),
      ])
      set({
        sessions,
        projects,
        model: config.model,
        contextWindow: config.contextWindow,
        permissionMode: normalizePermissionMode(config.permissionMode),
        error: config.initError ? `后端初始化提示：${config.initError}` : null,
        bootstrapped: true,
        bootstrapping: false,
      })
      useAppStore.getState().setStatus('ready')
      if (sessions.length > 0) {
        await get().selectSession(sessions[0].id)
      }
    } catch (err) {
      set({ bootstrapping: false, error: errorMessage(err) })
      useAppStore.getState().setStatus('error')
    }
  },

  setPanel: (panel) => {
    // 只切换视图，不动 items / running / approvals 等会话状态。
    // 离开对话视图时关闭文件预览，避免预览遮挡工作台页面。
    if (get().activePanel !== panel) set({ activePanel: panel, preview: null })
  },

  refreshSessions: async () => {
    try {
      const sessions = await ListSessions()
      set({ sessions })
      useAppStore.getState().setStatus('ready')
    } catch (err) {
      set({ error: errorMessage(err) })
      useAppStore.getState().setStatus('error')
    }
  },

  selectSession: async (id) => {
    // 重复点击正在运行的会话不应重载：那会丢掉尚未落盘的流式消息。
    if (get().activeId === id && get().running) {
      set({ activePanel: 'chat' })
      return
    }
    set({ loading: true, error: null, notice: null })
    try {
      const view = await LoadSession(id)
      set({
        activePanel: 'chat',
        activeId: view.id,
        title: view.title,
        workDir: view.workDir,
        model: view.model || get().model,
        permissionMode: normalizePermissionMode(view.permissionMode || get().permissionMode),
        items: viewToItems(view),
        usage: toUsage(view.usage),
        roundUsage: null,
        contextTokens: view.contextTokens ?? 0,
        running: view.running,
        step: 0,
        compacting: false,
        retry: null,
        approvals: [],
        stopReason: null,
        loading: false,
        error: null,
        preview: null,
      })
    } catch (err) {
      set({ loading: false, error: errorMessage(err) })
    }
  },

  newSession: async (projectPath) => {
    set({ loading: true, error: null })
    try {
      const info = await CreateSession({ title: '新会话', workDir: projectPath })
      set((state) => ({
        sessions: [info, ...state.sessions.filter((session) => session.id !== info.id)],
        loading: false,
      }))
      await get().selectSession(info.id)
    } catch (err) {
      set({ loading: false, error: errorMessage(err) })
    }
  },

  openProject: async () => {
    set({ error: null })
    try {
      const project = await OpenProject()
      set((state) => ({
        projects: [project, ...state.projects.filter((item) => item.path !== project.path)],
      }))
      // 已有会话时直接打开最近一个，否则在项目里新建会话。
      const existing = get().sessions.find((session) => session.workDir === project.path)
      if (existing) {
        await get().selectSession(existing.id)
      } else {
        await get().newSession(project.path)
      }
    } catch (err) {
      // 用户点“取消”不是错误。
      if (errorMessage(err).includes('project dialog canceled')) return
      set({ error: errorMessage(err) })
    }
  },

  send: async (prompt) => {
    const text = prompt.trim()
    if (text === '' || get().running) return

    let sessionId = get().activeId
    if (!sessionId) {
      await get().newSession()
      sessionId = get().activeId
      if (!sessionId) return
    }

    const userItem: ChatItem = {
      kind: 'user',
      id: localId('user'),
      text,
      timeMs: Date.now(),
    }
    set((state) => ({
      items: [...state.items, userItem],
      running: true,
      error: null,
      notice: null,
      step: 0,
      roundUsage: null,
      stopReason: null,
      retry: null,
    }))

    try {
      const result = await SendMessage(sessionId, text)
      if (get().activeId !== sessionId) return
      if (!result.running) set({ running: false })
      void get().refreshSessions()
    } catch (err) {
      const message = errorMessage(err)
      if (get().activeId !== sessionId) return
      set((state) => ({
        running: false,
        error: message,
        items: withSystemError(state.items, message),
      }))
    }
  },

  cancel: async () => {
    const sessionId = get().activeId
    if (!sessionId || !get().running) return
    set({ notice: '正在停止本轮…' })
    try {
      await CancelTurn(sessionId)
    } catch (err) {
      set({ notice: null, error: errorMessage(err) })
    }
  },

  approve: async (requestId, option) => {
    const sessionId = get().activeId
    if (!sessionId) return
    try {
      await ApproveTool(sessionId, requestId, option)
      set((state) => ({
        approvals: state.approvals.filter((item) => item.requestId !== requestId),
      }))
    } catch (err) {
      set((state) => ({
        error: errorMessage(err),
        approvals: state.approvals.filter((item) => item.requestId !== requestId),
      }))
    }
  },

  setPermissionMode: async (mode) => {
    const sessionId = get().activeId
    if (!sessionId) return
    const previous = get().permissionMode
    if (previous === mode) return
    set({ permissionMode: mode, error: null })
    try {
      await SetPermissionMode(sessionId, mode)
    } catch (err) {
      set({ permissionMode: previous, error: errorMessage(err) })
    }
  },

  // applyProviderConfig 在设置页保存后同步模型名与上下文窗口；
  // 已在运行中的会话在下一轮由后端按新配置重建引擎。
  applyProviderConfig: (config) => {
    set({
      model: config.model,
      contextWindow: config.contextWindow,
    })
  },

  // applySessionTitle 同步后端生成的标题；只改标题，不重载消息历史。
  applySessionTitle: (sessionId, title) => {
    if (!sessionId || !title) return
    set((state) => ({
      sessions: state.sessions.map((session) =>
        session.id === sessionId ? { ...session, title } : session,
      ),
      ...(state.activeId === sessionId ? { title } : {}),
    }))
  },

  // openFile 打开右侧文件树点击的文件：切回对话视图并显示只读预览。
  // 快速连点不同文件时，只接受最后一次请求的结果。
  openFile: async (path) => {
    const sessionId = get().activeId
    if (!sessionId || !path) return
    const workDir = get().workDir
    set({
      activePanel: 'chat',
      preview: {
        path,
        loading: true,
        content: '',
        size: 0,
        binary: false,
        truncated: false,
        error: null,
      },
    })
    try {
      const file = await ReadSessionFile(sessionId, workDir, path)
      if (get().preview?.path !== path) return
      set({
        preview: {
          path: file.path || path,
          loading: false,
          content: file.content ?? '',
          size: file.size ?? 0,
          binary: Boolean(file.binary),
          truncated: Boolean(file.truncated),
          error: null,
        },
      })
    } catch (err) {
      if (get().preview?.path !== path) return
      set((state) =>
        state.preview
          ? { preview: { ...state.preview, loading: false, error: errorMessage(err) } }
          : {},
      )
    }
  },

  closePreview: () => set({ preview: null }),

  ingest: (events) => {
    const activeId = get().activeId
    if (!activeId) return
    const relevant = events.filter((event) => event.sessionId === activeId)
    if (relevant.length === 0) return
    relevant.sort((a, b) => a.seq - b.seq)

    const previous = get()
    const stream: StreamState = {
      items: previous.items,
      running: previous.running,
      step: previous.step,
      compacting: previous.compacting,
      retry: previous.retry,
      approvals: previous.approvals,
      roundUsage: previous.roundUsage,
      contextTokens: previous.contextTokens,
      stopReason: previous.stopReason,
    }
    const next = reduceAgentEvents(stream, relevant)
    const ended = relevant.some((event) => event.type === 'turn_end')
    // 一轮结束时把本轮累计用量并入会话累计；过程中只展示本轮用量。
    const usage =
      ended && next.roundUsage ? addUsage(previous.usage, next.roundUsage) : previous.usage

    set({
      items: next.items,
      running: next.running,
      step: next.step,
      compacting: next.compacting,
      retry: next.retry,
      approvals: next.approvals,
      roundUsage: ended ? null : next.roundUsage,
      contextTokens: next.contextTokens,
      stopReason: next.stopReason,
      usage,
      notice: null,
      // 一轮结束可能改了文件；右侧文件树据此刷新已展开目录。
      filesRevision: ended ? previous.filesRevision + 1 : previous.filesRevision,
    })

    if (ended) {
      void get().refreshSessions()
    }
  },
}))
