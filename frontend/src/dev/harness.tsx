import { createElement } from 'react'
import { createRoot } from 'react-dom/client'
import App from '../App'
import '../style.css'
import { applyTheme, readStoredTheme, type ThemeMode } from '../lib/theme'
import { useSettingsStore } from '../stores/settings'
import { useWorkbenchStore } from '../stores/workbench'
import type { AgentEvent, PermissionMode } from '../types/agent'

/*
 * 浏览器验证入口：http://localhost:5173/harness.html
 *
 * 与 bnote 的 harness 同一思路：不启动 Wails，只用内存假实现顶替
 *   - window.go.app.App.*（Wails 绑定）
 *   - window.runtime.*（事件与窗口外观）
 * 然后挂载真实的 <App/>，所有组件、store、reducer 都是生产代码。
 * 断言通过 window.__anc 暴露的钩子完成，不接触真实后端与真实屏幕。
 */

type Listener = (...data: unknown[]) => void
type Dict = Record<string, unknown>

const AGENT_EVENT = 'agent:event'
const listeners = new Map<string, Set<Listener>>()

function on(name: string, cb: Listener): () => void {
  let set = listeners.get(name)
  if (!set) {
    set = new Set()
    listeners.set(name, set)
  }
  set.add(cb)
  return () => set?.delete(cb)
}

function off(name: string): void {
  listeners.delete(name)
}

function emitRaw(name: string, ...data: unknown[]): void {
  for (const cb of listeners.get(name) ?? []) cb(...data)
}

const runtime = {
  // 生成的 runtime.js 里 EventsOn 走的是 EventsOnMultiple。
  EventsOnMultiple: (name: string, cb: Listener, max: number) => {
    let remaining = max
    const unsubscribe = on(name, (...data: unknown[]) => {
      cb(...data)
      if (remaining === -1) return
      remaining -= 1
      if (remaining <= 0) unsubscribe()
    })
    return unsubscribe
  },
  EventsOn: on,
  EventsOnce: (name: string, cb: Listener) => {
    const unsubscribe = on(name, (...data: unknown[]) => {
      unsubscribe()
      cb(...data)
    })
    return unsubscribe
  },
  EventsOff: off,
  EventsOffAll: () => listeners.clear(),
  EventsEmit: emitRaw,
  WindowSetLightTheme: () => {},
  WindowSetDarkTheme: () => {},
  WindowSetSystemDefaultTheme: () => {},
  Environment: async () => ({ platform: 'darwin', buildtype: 'dev' }),
  BrowserOpenURL: (url: string) => console.info('[harness] BrowserOpenURL', url),
  ClipboardSetText: async () => true,
  ClipboardGetText: async () => '',
}
;(window as unknown as { runtime: typeof runtime }).runtime = runtime

// ---------------- 假会话数据 ----------------

const NOW = Date.now()
const SESSION_MD = 's_harness_md'
const SESSION_EMPTY = 's_harness_empty'
const SESSION_ORPHAN = 's_harness_orphan'

interface FakeMessage {
  id: string
  role: 'user' | 'assistant'
  blocks: Dict[]
  timeMs: number
}
interface FakeView {
  id: string
  title: string
  workDir: string
  model: string
  permissionMode: PermissionMode
  createdAtMs: number
  messages: FakeMessage[]
  usage: Dict
  contextTokens: number
  running: boolean
}
interface FakeSessionInfo {
  id: string
  title: string
  workDir: string
  messages: number
  updatedAtMs: number
  running: boolean
}

const MARKDOWN_REPLY = [
  '## 渲染检查',
  '',
  '这是一段 **加粗**、*斜体* 和 `inline code`，以及 [链接](https://example.com)。',
  '',
  '1. 第一项',
  '2. 第二项',
  '',
  '- 无序项 A',
  '- 无序项 B',
  '',
  '> 引用块：验证引用样式。',
  '',
  '```bash',
  'go test ./... && go vet ./...',
  '```',
  '',
  '| 工具 | 作用 |',
  '| --- | --- |',
  '| Read | 读文件 |',
  '| Write | 写文件 |',
  '',
  '---',
  '',
  '## Mermaid',
  '',
  '```mermaid',
  'flowchart LR',
  '  A[用户] --> B{Agent}',
  '  B -->|工具| C[执行]',
  '  C --> D[结果]',
  '```',
  '',
  '### 错误回退',
  '',
  '```mermaid',
  'this is not valid mermaid',
  '```',
  '',
  '结束。',
].join('\n')

function seedView(): FakeView {
  return {
    id: SESSION_MD,
    title: 'Markdown 验证',
    workDir: '/tmp/anc-harness',
    model: 'deepseek-flash',
    permissionMode: 'default',
    createdAtMs: NOW - 600_000,
    messages: [
      {
        id: 'm_user',
        role: 'user',
        blocks: [{ type: 'text', text: '用 markdown 展示一下渲染能力' }],
        timeMs: NOW - 120_000,
      },
      {
        id: 'm_assistant',
        role: 'assistant',
        blocks: [
          { type: 'thinking', thinking: '先确认渲染管线。' },
          { type: 'text', text: MARKDOWN_REPLY },
          {
            type: 'tool_use',
            toolUseId: 'toolu_harness',
            toolName: 'Read',
            input: { file_path: '/tmp/anc-harness/README.md' },
          },
        ],
        timeMs: NOW - 110_000,
      },
      {
        id: 'm_tool_result',
        role: 'user',
        blocks: [{ type: 'tool_result', toolUseId: 'toolu_harness', text: '# demo\nok' }],
        timeMs: NOW - 105_000,
      },
    ],
    usage: {
      inputTokens: 11234,
      outputTokens: 1456,
      cacheReadInputTokens: 19700,
      cacheCreationInputTokens: 0,
    },
    contextTokens: 32390,
    running: false,
  }
}

const views = new Map<string, FakeView>([[SESSION_MD, seedView()], [SESSION_EMPTY, {
  id: SESSION_EMPTY,
  title: '空会话',
  workDir: '/tmp/anc-harness',
  model: 'deepseek-flash',
  permissionMode: 'default',
  createdAtMs: NOW - 3_600_000,
  messages: [],
  usage: { inputTokens: 0, outputTokens: 0, cacheReadInputTokens: 0, cacheCreationInputTokens: 0 },
  contextTokens: 0,
  running: false,
}], [SESSION_ORPHAN, {
  id: SESSION_ORPHAN,
  title: '未归属会话',
  workDir: '/tmp/orphan-workspace',
  model: 'deepseek-flash',
  permissionMode: 'default',
  createdAtMs: NOW - 172_800_000,
  messages: [],
  usage: { inputTokens: 0, outputTokens: 0, cacheReadInputTokens: 0, cacheCreationInputTokens: 0 },
  contextTokens: 0,
  running: false,
}]])

const sessions: FakeSessionInfo[] = [
  {
    id: SESSION_MD,
    title: 'Markdown 验证',
    workDir: '/tmp/anc-harness',
    messages: 3,
    updatedAtMs: NOW - 100_000,
    running: false,
  },
  {
    id: SESSION_EMPTY,
    title: '空会话',
    workDir: '/tmp/anc-harness',
    messages: 0,
    updatedAtMs: NOW - 3_600_000,
    running: false,
  },
  {
    id: SESSION_ORPHAN,
    title: '未归属会话',
    workDir: '/tmp/orphan-workspace',
    messages: 0,
    updatedAtMs: NOW - 172_800_000,
    running: false,
  },
]

interface FakeProject {
  path: string
  name: string
  addedAtMs: number
}

// 两个项目：anc-harness 有会话，anc-empty 用于验证“暂无会话”的空分组。
const projects: FakeProject[] = [
  { path: '/tmp/anc-harness', name: 'anc-harness', addedAtMs: NOW - 7_200_000 },
  { path: '/tmp/anc-empty', name: 'anc-empty', addedAtMs: NOW - 86_400_000 },
]

const clone = <T,>(value: T): T => JSON.parse(JSON.stringify(value)) as T

// 模型配置与后端一样可变：设置页保存后再次读取必须看到新值。
let providerConfig: Dict = {
  provider: 'anthropic',
  baseUrl: 'https://harness.local',
  model: 'deepseek-flash',
  hasApiKey: true,
  apiKeyMasked: '****test',
  contextWindow: 128000,
  maxOutputTokens: 8192,
  permissionMode: 'default',
  workDir: '/tmp/anc-harness',
}

// ---------------- 假 Wails 绑定 ----------------

let seq = 0
let turnSeq = 0
let msgSeq = 0
const activeTurns = new Map<string, string>()

function emitEvent(event: Dict): void {
  seq += 1
  emitRaw(AGENT_EVENT, {
    seq,
    timeMs: Date.now(),
    sessionId: event.sessionId,
    turnId: event.turnId,
    ...event,
  })
}

const bindings = {
  Greet: (name: string) => `Hello ${name}, ANC DSK is running.`,

  ListSessions: async () => clone(sessions),

  ListProjects: async () => clone(projects),

  // 模拟用户选中一个新项目：登记后由 workbench store 自动建会话。
  OpenProject: async () => {
    const path = '/tmp/anc-demo'
    const existing = projects.find((project) => project.path === path)
    if (existing) return clone(existing)
    const project: FakeProject = { path, name: 'anc-demo', addedAtMs: Date.now() }
    projects.unshift(project)
    return clone(project)
  },

  AddProject: async (path: string) => {
    const existing = projects.find((project) => project.path === path)
    if (existing) return clone(existing)
    const project: FakeProject = {
      path,
      name: path.split('/').filter(Boolean).pop() || path,
      addedAtMs: Date.now(),
    }
    projects.unshift(project)
    return clone(project)
  },

  GetProviderConfig: async () => clone(providerConfig),

  SetProviderConfig: async (input: {
    provider: string
    baseUrl?: string
    model?: string
    apiKey?: string
    contextWindow?: number
    maxOutputTokens?: number
  }) => {
    providerConfig = {
      ...providerConfig,
      provider: input.provider,
      baseUrl: input.baseUrl ?? '',
      model: input.model ?? '',
      contextWindow: input.contextWindow ?? providerConfig.contextWindow,
      maxOutputTokens: input.maxOutputTokens ?? providerConfig.maxOutputTokens,
      hasApiKey: Boolean(input.apiKey) || providerConfig.hasApiKey,
      apiKeyMasked: input.apiKey ? '****test' : providerConfig.apiKeyMasked,
    }
  },

  LoadSession: async (id: string) => {
    const view = views.get(id)
    if (!view) throw new Error(`session not found: ${id}`)
    return clone(view)
  },

  CreateSession: async (input: { title?: string; workDir?: string }) => {
    const id = `s_harness_${views.size + 1}`
    const title = input.title || '新会话'
    const workDir = input.workDir || '/tmp/anc-harness'
    const view: FakeView = {
      id,
      title,
      workDir,
      model: 'deepseek-flash',
      permissionMode: 'default',
      createdAtMs: Date.now(),
      messages: [],
      usage: { inputTokens: 0, outputTokens: 0, cacheReadInputTokens: 0, cacheCreationInputTokens: 0 },
      contextTokens: 0,
      running: false,
    }
    views.set(id, view)
    const info: FakeSessionInfo = {
      id,
      title,
      workDir,
      messages: 0,
      updatedAtMs: Date.now(),
      running: false,
    }
    sessions.unshift(info)
    return clone(info)
  },

  SendMessage: async (sessionId: string, text: string) => {
    const view = views.get(sessionId)
    const turnId = `turn_harness_${(turnSeq += 1)}`
    const messageId = `msg_harness_${(msgSeq += 1)}`
    activeTurns.set(sessionId, turnId)

    if (view) {
      view.messages.push({
        id: `${messageId}_user`,
        role: 'user',
        blocks: [{ type: 'text', text }],
        timeMs: Date.now(),
      })
      view.messages.push({
        id: messageId,
        role: 'assistant',
        blocks: [{ type: 'text', text: MARKDOWN_REPLY }],
        timeMs: Date.now(),
      })
    }

    const chunks = MARKDOWN_REPLY.match(/[\s\S]{1,12}/g) ?? []
    let cursor = 0
    const pump = () => {
      if (activeTurns.get(sessionId) !== turnId) return
      if (cursor < chunks.length) {
        emitEvent({
          type: 'text_delta',
          sessionId,
          turnId,
          messageId,
          blockIndex: 0,
          text: chunks[cursor],
        })
        cursor += 1
        setTimeout(pump, 16)
        return
      }
      const usage = {
        inputTokens: 1200,
        outputTokens: 320,
        cacheReadInputTokens: 640,
        cacheCreationInputTokens: 0,
      }
      emitEvent({
        type: 'usage',
        sessionId,
        turnId,
        messageId,
        usage,
        contextTokens: usage.inputTokens + usage.cacheReadInputTokens + usage.outputTokens,
      })
      emitEvent({ type: 'message_stop', sessionId, turnId, messageId })
      emitEvent({
        type: 'turn_end',
        sessionId,
        turnId,
        stopReason: 'completed',
        usage,
        contextTokens: usage.inputTokens + usage.cacheReadInputTokens + usage.outputTokens,
      })
      activeTurns.delete(sessionId)
      const info = sessions.find((item) => item.id === sessionId)
      if (info) {
        info.messages += 2
        info.updatedAtMs = Date.now()
      }
    }

    setTimeout(() => {
      if (activeTurns.get(sessionId) !== turnId) return
      emitEvent({ type: 'turn_start', sessionId, turnId })
      emitEvent({ type: 'step_start', sessionId, turnId, step: 1 })
      emitEvent({ type: 'message_start', sessionId, turnId, messageId })
      pump()
    }, 30)

    return { sessionId, turnId, running: true }
  },

  CancelTurn: async (sessionId: string) => {
    const turnId = activeTurns.get(sessionId)
    if (!turnId) return
    activeTurns.delete(sessionId)
    emitEvent({ type: 'turn_end', sessionId, turnId, stopReason: 'canceled' })
  },

  ApproveTool: async (sessionId: string, requestId: string, option: string) => {
    const turnId = activeTurns.get(sessionId) ?? ''
    emitEvent({
      type: 'permission_resolved',
      sessionId,
      turnId,
      text: option.startsWith('deny') ? 'deny' : 'allow',
      approval: { requestId },
    })
  },

  SetPermissionMode: async (sessionId: string, mode: PermissionMode) => {
    const view = views.get(sessionId)
    if (view) view.permissionMode = mode
  },
}
;(window as unknown as { go: unknown }).go = { app: { App: bindings } }

// ---------------- 挂载真实 App ----------------

applyTheme(readStoredTheme())

const host = document.getElementById('root')
if (!host) throw new Error('harness: missing #root')
createRoot(host).render(createElement(App))

// ---------------- 断言钩子 ----------------

function textOf(selector: string): string | null {
  const element = document.querySelector(selector)
  return element?.textContent?.trim() ?? null
}

const ready = (async () => {
  for (let i = 0; i < 500; i += 1) {
    const state = useWorkbenchStore.getState()
    if (state.bootstrapped && state.activeId) return
    await new Promise((resolve) => setTimeout(resolve, 10))
  }
  throw new Error('harness: app did not become ready')
})()

const api = {
  store: useWorkbenchStore,
  settings: useSettingsStore,
  ready,

  /** 直接注入 agent 事件（走真实 EventsOn → store.ingest 链路）。 */
  emit: (events: AgentEvent | AgentEvent[]) => {
    for (const event of Array.isArray(events) ? events : [events]) emitRaw(AGENT_EVENT, event)
  },

  /** 走真实 SendMessage → 模拟流式回复。 */
  send: async (text: string) => {
    const sessionId = useWorkbenchStore.getState().activeId
    if (!sessionId) throw new Error('no active session')
    await useWorkbenchStore.getState().send(text)
  },

  select: (id: string) => useWorkbenchStore.getState().selectSession(id),
  openProject: () => useWorkbenchStore.getState().openProject(),
  newSession: (projectPath?: string) => useWorkbenchStore.getState().newSession(projectPath),
  openSettings: () => useSettingsStore.getState().openSettings(),
  closeSettings: () => useSettingsStore.getState().closeSettings(),
  toggleSidebar: () => useSettingsStore.getState().toggleSidebar(),
  setTheme: (mode: ThemeMode) => useSettingsStore.getState().setTheme(mode),
  setPermissionMode: (mode: PermissionMode) => useWorkbenchStore.getState().setPermissionMode(mode),

  /** 一次取回所有可断言的 DOM/状态指标。 */
  metrics: () => {
    const markdown = document.querySelector('[data-testid="markdown"]')
    return {
      theme: document.documentElement.dataset.theme ?? null,
      settingsOpen: document.querySelector('#settings-title') !== null,
      settingsText: textOf('[role="dialog"][aria-labelledby="settings-title"]'),
      usage: textOf('[data-testid="usage"]'),
      usageTooltip: document.querySelector('[data-testid="usage"]')?.getAttribute('title') ?? null,
      usageInsideComposer:
        document.querySelector('[data-testid="composer"] [data-testid="usage"]') !== null,
      statusBarPresent: document.querySelector('[data-testid="status-bar"]') !== null,
      markdownRoots: document.querySelectorAll('[data-testid="markdown"]').length,
      headings: markdown?.querySelectorAll('h1, h2, h3').length ?? 0,
      codeBlocks: markdown?.querySelectorAll('pre code').length ?? 0,
      highlightedTokens: markdown?.querySelectorAll('pre code .hljs-keyword, pre code .hljs-built_in')
        .length ?? 0,
      tables: markdown?.querySelectorAll('table').length ?? 0,
      inlineCode: markdown?.querySelectorAll('code:not(pre code)').length ?? 0,
      links: markdown?.querySelectorAll('a[target="_blank"]').length ?? 0,
      toolCards: document.querySelectorAll('button[aria-expanded]').length,
      items: useWorkbenchStore.getState().items.length,
      running: useWorkbenchStore.getState().running,
      permissionMode: useWorkbenchStore.getState().permissionMode,
      activeTitle: useWorkbenchStore.getState().title,
      activeWorkDir: useWorkbenchStore.getState().workDir,
      projectCount: useWorkbenchStore.getState().projects.length,
      projectNames: [...document.querySelectorAll('[data-testid="project-item"]')].map(
        (element) => element.getAttribute('data-project') ?? '',
      ),
      projectSessionCounts: [...document.querySelectorAll('[data-testid="project-item"]')].map(
        (element) => element.querySelectorAll('[data-testid="session-row"]').length,
      ),
      orphanRows:
        document.querySelector('[data-testid="project-list"]')?.textContent?.includes('其他会话') ??
        false,
      hasOpenProjectButton: document.querySelector('[data-testid="open-project-button"]') !== null,
      hasNewTaskButton: document.querySelector('[data-testid="new-task-button"]') !== null,
      welcome: document.querySelector('[data-testid="welcome"]') !== null,
      heroComposer: document.querySelector('[data-testid="welcome"] [data-testid="composer"]') !== null,
      modelContext: textOf('[data-testid="model-context"]'),
      modelContextTitle:
        document.querySelector('[data-testid="model-context"]')?.getAttribute('title') ?? null,
      contextTokens: useWorkbenchStore.getState().contextTokens,
      contextWindow: useWorkbenchStore.getState().contextWindow,
      model: useWorkbenchStore.getState().model,
      headerText: textOf('header'),
      enterHintPresent:
        document.querySelector('[data-testid="app"]')?.textContent?.includes('Shift+Enter') ?? false,
      sidebarCollapsed: useSettingsStore.getState().sidebarCollapsed,
      sidebarWidth: document.querySelector('[data-testid="sidebar"]')?.clientWidth ?? 0,
      mermaidBlocks: document.querySelectorAll('[data-testid="mermaid"]').length,
      mermaidSvgs: document.querySelectorAll('[data-testid="mermaid"] .mermaid-svg > svg').length,
      mermaidErrors: document.querySelectorAll('[data-testid="mermaid-error"]').length,
      mermaidStreaming: document.querySelectorAll('[data-testid="mermaid-streaming"]').length,
      mermaidErrorText: textOf('[data-testid="mermaid-error"]'),
      mermaidFullscreen: document.querySelector('[data-testid="mermaid-fullscreen"]') !== null,
      mermaidFullscreenSvg:
        document.querySelector('[data-testid="mermaid-fullscreen"] .mermaid-fullscreen svg') !==
        null,
      settingsModelValue:
        (document.querySelector('[data-testid="settings-model"]') as HTMLInputElement | null)
          ?.value ?? null,
      settingsContextValue:
        (document.querySelector('[data-testid="settings-context-window"]') as HTMLInputElement | null)
          ?.value ?? null,
      settingsSaveDisabled:
        (document.querySelector('[data-testid="settings-save"]') as HTMLButtonElement | null)
          ?.disabled ?? null,
    }
  },

  text: () => document.body.innerText,
  bootErrors: () => (window as unknown as { __bootErrors: string[] }).__bootErrors,
}

const globalWindow = window as unknown as {
  __anc: typeof api
  __ancReady: Promise<void>
}
globalWindow.__anc = api
globalWindow.__ancReady = ready
