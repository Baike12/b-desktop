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

export interface WorkbenchState {
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

  init: () => Promise<void>
  refreshSessions: () => Promise<void>
  selectSession: (id: string) => Promise<void>
  newSession: (projectPath?: string) => Promise<void>
  openProject: () => Promise<void>
  send: (prompt: string) => Promise<void>
  cancel: () => Promise<void>
  approve: (requestId: string, option: string) => Promise<void>
  setPermissionMode: (mode: PermissionMode) => Promise<void>
  applyProviderConfig: (config: app.ProviderConfigDTO) => void
  ingest: (events: AgentEvent[]) => void
}

export const useWorkbenchStore = create<WorkbenchState>((set, get) => ({
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
    if (get().activeId === id && get().running) return
    set({ loading: true, error: null, notice: null })
    try {
      const view = await LoadSession(id)
      set({
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
    })

    if (ended) {
      void get().refreshSessions()
    }
  },
}))
