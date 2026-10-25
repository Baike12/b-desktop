import { create } from 'zustand'
import {
  ApproveTool,
  CancelTurn,
  CompactSession,
  CreateSession,
  ExportSession,
  GetProviderConfig,
  ListProjects,
  ListRewindPoints,
  ListSessions,
  LoadSession,
  OpenProject,
  RewindSession,
  SendMessage,
  SetPermissionMode,
  SetProviderConfig,
} from '../../wailsjs/go/app/App'
import type { app } from '../../wailsjs/go/models'
import { addUsage, emptyUsage } from '../lib/format'
import { parseUserMentions } from '../lib/mentions'
import { normalizePermissionMode } from '../lib/permission'
import { reduceAgentEvents, type StreamState } from '../lib/reduceAgentEvents'
import { viewToItems } from '../lib/sessionView'
import type { AgentEvent, ApprovalView, PermissionMode, RetryInfo, TodoItemView, Usage } from '../types/agent'
import type { UserImage } from '../types/chat'
import type { ChatItem } from '../types/chat'
import { useAppStore } from './app'
import { useSettingsStore } from './settings'

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

// QueuedInput 是运行中排队的一条用户输入；轮次结束后合并发送。
export interface QueuedInput {
  text: string
  images: UserImage[]
}

// Prefill 是回溯后回填输入框的内容；nonce 用于触发 Composer 消费。
export interface Prefill {
  text: string
  images: UserImage[]
  nonce: number
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
  // 文件树刷新信号：每轮结束后自增，右侧栏据此重载已展开目录。
  filesRevision: number
  // todos 是会话待办清单（todo_update 事件驱动），驱动计划面板。
  todos: TodoItemView[]
  // supportsImages 报告当前模型是否接受图像输入（能力门控）。
  supportsImages: boolean
  // queues 是每个会话的运行中排队输入；轮次结束后合并发送。
  queues: Record<string, QueuedInput[]>
  // 回溯状态：选择器打开时展示 rewindPoints。
  rewindOpen: boolean
  rewindPoints: app.RewindPointDTO[]
  rewindLoading: boolean
  // prefill 是回溯后回填输入框的内容；Composer 按 nonce 消费。
  prefill: Prefill | null

  init: () => Promise<void>
  setPanel: (panel: PanelId) => void
  refreshSessions: () => Promise<void>
  selectSession: (id: string) => Promise<void>
  newSession: (projectPath?: string) => Promise<void>
  openProject: () => Promise<void>
  send: (prompt: string, images?: UserImage[]) => Promise<void>
  // compact 手动压缩当前会话上下文（用户输入 /compact 时由 send 拦截调用）。
  compact: () => Promise<void>
  cancel: () => Promise<void>
  approve: (requestId: string, option: string, updatedInput?: unknown) => Promise<void>
  setPermissionMode: (mode: PermissionMode) => Promise<void>
  applyProviderConfig: (config: app.ProviderConfigDTO) => void
  bumpFilesRevision: () => void
  applySessionTitle: (sessionId: string, title: string) => void
  ingest: (events: AgentEvent[]) => void
  // clearConversation 实现 /clear：同项目开新会话，当前会话保留在历史里。
  clearConversation: () => Promise<void>
  // changeModel 实现 /model <模型名>；空参数打开设置页。
  changeModel: (modelName: string) => Promise<void>
  // exportConversation 实现 /export：导出 Markdown 并展示路径。
  exportConversation: () => Promise<void>
  // openRewind 加载回溯锚点并打开选择器（双击 Esc 或双击用户气泡）。
  openRewind: () => Promise<void>
  closeRewind: () => void
  // rewindTo 回退到指定用户消息之前，并把消息内容回填输入框。
  rewindTo: (messageId: string) => Promise<void>
  consumePrefill: () => Prefill | null
}

// LOCAL_COMMAND_RE 是由前端本地执行的内置命令；后端 expandSlashCommand 对
// 同名命令不展开（见 internal/app/app_slash.go 的 builtinCommandNames）。
const LOCAL_COMMAND_RE = /^\/(compact|clear|model|export)(?:\s|$)/

// drainQueue 取走会话的排队输入并合并发送；队列元素在发送前已从 items 移除。
function drainQueue(sessionId: string): void {
  const state = useWorkbenchStore.getState()
  const queued = state.queues[sessionId]
  if (!queued || queued.length === 0) return
  useWorkbenchStore.setState((prev) => ({
    queues: { ...prev.queues, [sessionId]: [] },
    // 移除排队乐观气泡；send 会追加正式的用户消息。
    items: prev.items.filter((item) => !(item.kind === 'user' && item.queued)),
  }))
  const text = queued
    .map((item) => item.text)
    .filter((value) => value !== '')
    .join('\n\n')
  const images = queued.flatMap((item) => item.images)
  void useWorkbenchStore.getState().send(text, images.length > 0 ? images : undefined)
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
  filesRevision: 0,
  todos: [],
  supportsImages: true,
  queues: {},
  rewindOpen: false,
  rewindPoints: [],
  rewindLoading: false,
  prefill: null,

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
        supportsImages: config.supportsImages ?? true,
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
    if (get().activePanel !== panel) set({ activePanel: panel })
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
        // 清单是会话级的：恢复会话后等下一次 todo_update 再展示。
        todos: [],
        rewindOpen: false,
        rewindPoints: [],
      })
      // 运行中排队后切走的会话：turn_end 事件当时未经过 ingest，
      // 回到该会话且已空闲时补发遗留队列。
      if (!view.running && (get().queues[id]?.length ?? 0) > 0) {
        drainQueue(id)
      }
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

  send: async (prompt, images) => {
    const text = prompt.trim()
    const attachments = images ?? []
    if (text === '' && attachments.length === 0) return
    if (get().running) {
      // 运行中输入排队（cc 语义）：轮次结束后自动发送。
      const sessionId = get().activeId
      if (!sessionId) return
      set((state) => ({
        queues: {
          ...state.queues,
          [sessionId]: [...(state.queues[sessionId] ?? []), { text, images: attachments }],
        },
        items: [
          ...state.items,
          {
            kind: 'user',
            id: localId('queued'),
            text,
            images: attachments.length > 0 ? attachments : undefined,
            attachments: parseUserMentions(text).length > 0 ? parseUserMentions(text) : undefined,
            queued: true,
            timeMs: Date.now(),
          } as ChatItem,
        ],
      }))
      return
    }

    let sessionId = get().activeId
    if (!sessionId) {
      await get().newSession()
      sessionId = get().activeId
      if (!sessionId) return
    }

    // 内置本地命令：不进入模型消息，由前端直接执行。
    if (LOCAL_COMMAND_RE.test(text)) {
      const name = text.slice(1).split(/\s/, 1)[0]
      const args = text.slice(1 + name.length).trim()
      if (name === 'compact') {
        await get().compact()
      } else if (name === 'clear') {
        await get().clearConversation()
      } else if (name === 'model') {
        await get().changeModel(args)
      } else if (name === 'export') {
        await get().exportConversation()
      }
      return
    }

    const mentions = parseUserMentions(text)
    const userItem: ChatItem = {
      kind: 'user',
      id: localId('user'),
      text,
      images: attachments.length > 0 ? attachments : undefined,
      attachments: mentions.length > 0 ? mentions : undefined,
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
      const result = await SendMessage(
        sessionId,
        text,
        attachments.map((img) => ({ mediaType: img.mediaType, data: img.data })),
      )
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

  compact: async () => {
    const sessionId = get().activeId
    if (!sessionId || get().running) return
    set({ notice: '压缩中…', error: null })
    try {
      await CompactSession(sessionId)
      // 压缩结果由 compact_start/compact_end 事件流推送：结束时会推系统消息。
    } catch (err) {
      set({ error: errorMessage(err) })
    } finally {
      if (get().notice === '压缩中…') set({ notice: null })
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

  approve: async (requestId, option, updatedInput) => {
    const sessionId = get().activeId
    if (!sessionId) return
    try {
      await ApproveTool(
        sessionId,
        requestId,
        option,
        updatedInput === undefined ? '' : JSON.stringify(updatedInput),
      )
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

  // applyProviderConfig 在设置页保存后同步模型名、上下文窗口与图像能力；
  // 已在运行中的会话在下一轮由后端按新配置重建引擎。
  applyProviderConfig: (config) => {
    set({
      model: config.model,
      contextWindow: config.contextWindow,
      supportsImages: config.supportsImages ?? true,
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

  // bumpFilesRevision 通知右侧文件树与预览面板：磁盘上的文件发生了变化。
  // 文件树据此重载已展开目录，预览面板据此重新读取已打开的文件。
  bumpFilesRevision: () => set((state) => ({ filesRevision: state.filesRevision + 1 })),

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
      todos: previous.todos,
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
      todos: next.todos,
      // 一轮结束可能改了文件；右侧文件树据此刷新已展开目录。
      filesRevision: ended ? previous.filesRevision + 1 : previous.filesRevision,
    })

    if (ended) {
      void get().refreshSessions()
      // 轮次结束自动发送排队输入（cc 的队列语义）。
      if ((get().queues[activeId]?.length ?? 0) > 0) {
        drainQueue(activeId)
      }
    }
  },

  // ── 内置本地命令 ────────────────────────────────────────────────

  clearConversation: async () => {
    const workDir = get().workDir
    try {
      const info = await CreateSession({ title: '新会话', workDir: workDir || undefined })
      set((state) => ({
        sessions: [info, ...state.sessions.filter((session) => session.id !== info.id)],
      }))
      await get().selectSession(info.id)
    } catch (err) {
      set({ error: errorMessage(err) })
    }
  },

  changeModel: async (modelName) => {
    const trimmed = modelName.trim()
    if (trimmed === '') {
      const { openSettings } = useSettingsStore.getState()
      openSettings()
      return
    }
    try {
      const config = await GetProviderConfig()
      await SetProviderConfig({
        provider: config.provider,
        baseUrl: config.baseUrl,
        model: trimmed,
        apiKey: '',
        contextWindow: config.contextWindow,
        maxOutputTokens: config.maxOutputTokens,
        thinkingEnabled: config.thinkingEnabled,
        thinkingBudget: config.thinkingBudget,
      })
      const next = await GetProviderConfig()
      get().applyProviderConfig(next)
      pushSystemItem(`模型已切换为 ${trimmed}`)
    } catch (err) {
      set({ error: errorMessage(err) })
    }
  },

  exportConversation: async () => {
    const sessionId = get().activeId
    if (!sessionId) return
    try {
      const result = await ExportSession(sessionId, '')
      pushSystemItem(
        `已导出 ${result.messageCount} 条消息 → ${result.path}（内容已复制到剪贴板）`,
      )
    } catch (err) {
      set({ error: errorMessage(err) })
    }
  },

  // ── 回溯（双击 Esc / 双击用户气泡） ─────────────────────────────

  openRewind: async () => {
    const sessionId = get().activeId
    if (!sessionId) return
    if (get().running) {
      set({ notice: '请先停止当前轮次再回溯' })
      return
    }
    set({ rewindLoading: true })
    try {
      const points = await ListRewindPoints(sessionId)
      set({ rewindPoints: points, rewindOpen: true, rewindLoading: false })
    } catch (err) {
      set({ rewindLoading: false, error: errorMessage(err) })
    }
  },

  closeRewind: () => set({ rewindOpen: false, rewindPoints: [] }),

  rewindTo: async (messageId) => {
    const sessionId = get().activeId
    if (!sessionId) return
    try {
      const result = await RewindSession(sessionId, messageId)
      get().closeRewind()
      // transcript 已截断：重载会话视图，再把锚点消息回填输入框。
      await get().selectSession(sessionId)
      set({
        prefill: {
          text: result.text,
          images: (result.images ?? []).map((img) => ({
            mediaType: img.mediaType,
            data: img.data,
          })),
          nonce: Date.now(),
        },
      })
    } catch (err) {
      set({ error: errorMessage(err) })
    }
  },

  consumePrefill: () => {
    const prefill = get().prefill
    if (prefill) set({ prefill: null })
    return prefill
  },
}))

// pushSystemItem 追加一条本地系统消息（导出结果、模型切换等）。
function pushSystemItem(text: string): void {
  useWorkbenchStore.setState((state) => ({
    items: [
      ...state.items,
      { kind: 'system', id: localId('sys'), tone: 'info' as const, text, timeMs: Date.now() },
    ],
  }))
}
