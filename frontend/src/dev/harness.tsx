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
 * 断言通过 window.__bdesk 暴露的钩子完成，不接触真实后端与真实屏幕。
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
  projectPath?: string
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
    workDir: '/tmp/bdesk-harness',
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
            input: { file_path: '/tmp/bdesk-harness/README.md' },
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
  workDir: '/tmp/bdesk-harness',
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
    workDir: '/tmp/bdesk-harness',
    projectPath: '/tmp/bdesk-harness',
    messages: 3,
    updatedAtMs: NOW - 100_000,
    running: false,
  },
  {
    id: SESSION_EMPTY,
    title: '空会话',
    workDir: '/tmp/bdesk-harness',
    projectPath: '/tmp/bdesk-harness',
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

// 两个项目：bdesk-harness 有会话，bdesk-empty 用于验证“暂无会话”的空分组。
const projects: FakeProject[] = [
  { path: '/tmp/bdesk-harness', name: 'bdesk-harness', addedAtMs: NOW - 7_200_000 },
  { path: '/tmp/bdesk-empty', name: 'bdesk-empty', addedAtMs: NOW - 86_400_000 },
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
  workDir: '/tmp/bdesk-harness',
}

// ---------------- 工作台面板假数据 ----------------

interface FakeWorktree {
  path: string
  branch: string
  head: string
  detached: boolean
  bare: boolean
  locked: boolean
  prunable: boolean
  main: boolean
  managed: boolean
}

interface FakeSkill {
  name: string
  description: string
  source: string
}

interface FakeMcpServer {
  name: string
  command: string
  args: string[]
  envKeys: string[]
  state: string
  error?: string
  toolCount: number
}

interface FakeMcpTool {
  name: string
  server: string
  tool: string
  description: string
  inputSchema: Dict
  readOnly: boolean
}

interface FakeSubagentType {
  name: string
  description: string
  path?: string
}

interface FakeSubagentTask {
  id: string
  sessionId: string
  description: string
  agentType: string
  status: string
  background: boolean
  startedAt: string
  finishedAt: string
  result?: string
  error?: string
  turns: number
  inputTokens: number
  outputTokens: number
}

interface FakeRule {
  id: string
  tool: string
  pattern?: string
  matchMode: string
  effect: string
  source: string
  note?: string
}

const fakeWorktrees = new Map<string, FakeWorktree[]>()

// worktreesRootFor 复刻后端约定：<项目父目录>/worktrees/<项目名>。
function worktreesRootFor(projectPath: string): string {
  const parts = projectPath.split('/').filter(Boolean)
  const name = parts.pop() ?? 'project'
  return `/tmp/worktrees/${name}`
}

function projectPathForWorkDir(workDir: string): string {
  const exact = projects.find((project) => project.path === workDir)
  if (exact) return exact.path
  const owner = projects.find((project) => workDir.startsWith(`${worktreesRootFor(project.path)}/`))
  return owner?.path ?? ''
}

function worktreesFor(repo: string): FakeWorktree[] {
  let list = fakeWorktrees.get(repo)
  if (!list) {
    list = [
      {
        path: repo,
        branch: 'main',
        head: 'abc1234',
        detached: false,
        bare: false,
        locked: false,
        prunable: false,
        main: true,
        managed: false,
      },
    ]
    fakeWorktrees.set(repo, list)
  }
  return list
}

const fakeSkills: FakeSkill[] = [
  { name: 'code-review', description: '按项目规范审阅改动并给出可执行的修改建议', source: 'user' },
  { name: 'release-notes', description: '从提交记录生成面向用户的发布说明', source: 'project' },
]

const fakeMcpServers: FakeMcpServer[] = [
  {
    name: 'filesystem',
    command: 'npx',
    args: ['-y', '@modelcontextprotocol/server-filesystem', '/tmp'],
    envKeys: [],
    state: 'ready',
    toolCount: 2,
  },
]

const fakeMcpTools: FakeMcpTool[] = [
  {
    name: 'mcp__filesystem__read_file',
    server: 'filesystem',
    tool: 'read_file',
    description: '读取指定路径的文件内容',
    inputSchema: {},
    readOnly: true,
  },
  {
    name: 'mcp__filesystem__list_directory',
    server: 'filesystem',
    tool: 'list_directory',
    description: '列出目录下的文件',
    inputSchema: {},
    readOnly: true,
  },
]

interface FakeHook {
  event: string
  matcher: string
  command: string
  source: string
}

const fakeHooks: FakeHook[] = [
  { event: 'PreToolUse', matcher: 'Bash', command: './scripts/check-bash.sh', source: 'user' },
  { event: 'UserPromptSubmit', matcher: '', command: './scripts/context.sh', source: 'project' },
]

const fakeSubagentTypes: FakeSubagentType[] = [
  { name: 'general-purpose', description: '通用子 Agent，可读写文件并执行命令', path: '' },
  { name: 'explore', description: '只读检索子 Agent，用于快速定位代码', path: '' },
]

const fakeSubagentTasks: FakeSubagentTask[] = [
  {
    id: 'subagent_harness_1',
    sessionId: SESSION_MD,
    description: '梳理前端状态管理入口',
    agentType: 'explore',
    status: 'completed',
    background: false,
    startedAt: new Date(NOW - 300_000).toISOString(),
    finishedAt: new Date(NOW - 240_000).toISOString(),
    result: '定位到 stores/workbench.ts。',
    turns: 3,
    inputTokens: 2400,
    outputTokens: 180,
  },
]

const fakeRules: FakeRule[] = [
  {
    id: 'rule_harness_1',
    tool: 'Bash',
    pattern: 'go test',
    matchMode: 'prefix',
    effect: 'allow',
    source: 'session',
  },
]

interface FakeDailyUsage {
  date: string
  requestCount: number
  errorCount: number
  inputTokens: number
  outputTokens: number
  cacheReadInputTokens: number
  cacheCreationInputTokens: number
  totalTokens: number
  durationMs: number
  models: Dict[]
}

function fakeUsageSummary(days: number): Dict {
  const today = Date.now()
  const span = Math.max(1, Math.min(days, 7))
  const rows: FakeDailyUsage[] = []
  for (let index = span - 1; index >= 0; index -= 1) {
    const date = new Date(today - index * 86_400_000).toISOString().slice(0, 10)
    const inputTokens = 1200 + index * 340
    const outputTokens = 300 + index * 60
    rows.push({
      date,
      requestCount: 2 + index,
      errorCount: index === 0 ? 1 : 0,
      inputTokens,
      outputTokens,
      cacheReadInputTokens: 0,
      cacheCreationInputTokens: 0,
      totalTokens: inputTokens + outputTokens,
      durationMs: 2400 + index * 500,
      models: [],
    })
  }
  let requests = 0
  let errors = 0
  let inputTokens = 0
  let outputTokens = 0
  let totalTokens = 0
  let durationMs = 0
  for (const row of rows) {
    requests += row.requestCount
    errors += row.errorCount
    inputTokens += row.inputTokens
    outputTokens += row.outputTokens
    totalTokens += row.totalTokens
    durationMs += row.durationMs
  }
  return {
    from: rows[0]?.date ?? '',
    to: rows[rows.length - 1]?.date ?? '',
    requestCount: requests,
    errorCount: errors,
    inputTokens,
    outputTokens,
    cacheReadInputTokens: 0,
    cacheCreationInputTokens: 0,
    totalTokens,
    durationMs,
    days: rows,
    models: [
      {
        model: 'deepseek-flash',
        requestCount: requests,
        errorCount: errors,
        inputTokens,
        outputTokens,
        cacheReadInputTokens: 0,
        cacheCreationInputTokens: 0,
        totalTokens,
        durationMs,
      },
    ],
  }
}

function fakeTraces(limit: number): Dict[] {
  const count = Math.max(0, Math.min(limit, 5))
  return Array.from({ length: count }, (_, index) => ({
    atMs: Date.now() - index * 120_000,
    sessionId: SESSION_MD,
    model: 'deepseek-flash',
    inputTokens: 1200 + index * 100,
    outputTokens: 320,
    cacheReadInputTokens: 640,
    cacheCreationInputTokens: 0,
    totalTokens: 2160 + index * 100,
    durationMs: 1800 + index * 120,
    stopReason: 'end_turn',
  }))
}

// ---------------- 假 Wails 绑定 ----------------

interface FakeFileEntry {
  name: string
  path: string
  isDir: boolean
  isSymlink: boolean
  size: number
}

const fakeFileEntries: Record<string, FakeFileEntry[]> = {
  '': [
    { name: 'assets', path: 'assets', isDir: true, isSymlink: false, size: 0 },
    { name: 'src', path: 'src', isDir: true, isSymlink: false, size: 0 },
    { name: 'README.md', path: 'README.md', isDir: false, isSymlink: false, size: 42 },
    { name: 'package.json', path: 'package.json', isDir: false, isSymlink: false, size: 36 },
  ],
  src: [
    { name: 'components', path: 'src/components', isDir: true, isSymlink: false, size: 0 },
    { name: 'App.tsx', path: 'src/App.tsx', isDir: false, isSymlink: false, size: 64 },
  ],
  'src/components': [
    {
      name: 'RightSidebar.tsx',
      path: 'src/components/RightSidebar.tsx',
      isDir: false,
      isSymlink: false,
      size: 72,
    },
  ],
  assets: [
    { name: 'logo.png', path: 'assets/logo.png', isDir: false, isSymlink: false, size: 2048 },
  ],
}

const fakeFileContents: Record<string, string> = {
  'README.md': '# bdesk-harness\n\n右侧文件栏验证。\n',
  'package.json': '{\n  "name": "bdesk-harness"\n}\n',
  'src/App.tsx': 'export default function App() {\n  return <div>demo</div>\n}\n',
  'src/components/RightSidebar.tsx': 'export default function RightSidebar() {\n  return null\n}\n',
}

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
  Greet: (name: string) => `Hello ${name}, B Desktop is running.`,

  ListSessions: async () => clone(sessions),

  ListProjects: async () => clone(projects),

  // 模拟用户选中一个新项目：登记后由 workbench store 自动建会话。
  OpenProject: async () => {
    const path = '/tmp/bdesk-demo'
    const existing = projects.find((project) => project.path === path)
    if (existing) return clone(existing)
    const project: FakeProject = { path, name: 'bdesk-demo', addedAtMs: Date.now() }
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

  ListSessionFiles: async (_sessionId: string, _workDir: string, path: string) => ({
    entries: clone(fakeFileEntries[path] ?? []),
    truncated: false,
  }),

  ReadSessionFile: async (_sessionId: string, _workDir: string, path: string) => {
    if (path.endsWith('.png')) {
      return { path, size: 2048, content: '', binary: true, truncated: false }
    }
    const content = fakeFileContents[path] ?? `// ${path}\n`
    return {
      path,
      size: new TextEncoder().encode(content).length,
      content,
      binary: false,
      truncated: false,
    }
  },

  CreateSession: async (input: { title?: string; workDir?: string }) => {
    const id = `s_harness_${views.size + 1}`
    const title = input.title || '新会话'
    const workDir = input.workDir || '/tmp/bdesk-harness'
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
      projectPath: projectPathForWorkDir(workDir),
      messages: 0,
      updatedAtMs: Date.now(),
      running: false,
    }
    sessions.unshift(info)
    return clone(info)
  },

  SendMessage: async (sessionId: string, text: string) => {
    const view = views.get(sessionId)
    const firstMessage = (view?.messages.length ?? 0) === 0
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

    // 模拟后端的首轮自动命名：先落一个截断标题，再推送 session:title。
    if (firstMessage && view) {
      const line = text.trim().split(/\r?\n/)[0]?.replace(/^[#>*+\-•\s]+/, '').trim() ?? ''
      const title = line ? Array.from(line).slice(0, 24).join('') : '新会话'
      setTimeout(() => {
        view.title = title
        const info = sessions.find((item) => item.id === sessionId)
        if (info) info.title = title
        emitRaw('session:title', { sessionId, title })
      }, 600)
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

  // ---- 工作台面板：搜索 / 工作树 / 扩展设置 ----

  SearchSessions: async (query: string, limit: number) => {
    const needle = query.trim().toLowerCase()
    if (!needle) return []
    return clone(sessions)
      .filter(
        (session) =>
          session.title.toLowerCase().includes(needle) ||
          session.workDir.toLowerCase().includes(needle),
      )
      .slice(0, Math.max(limit, 1))
      .map((session) => ({
        sessionId: session.id,
        title: session.title,
        workDir: session.workDir,
        updatedAt: new Date(session.updatedAtMs).toISOString(),
        matchCount: 1,
        matches: [
          {
            role: 'user',
            line: 1,
            snippet: `匹配 “${query.trim()}” 的示例片段`,
            time: new Date(session.updatedAtMs).toISOString(),
          },
        ],
      }))
  },

  ListProjectWorktrees: async (projectPath: string) => ({
    projectPath,
    repoRoot: projectPath,
    worktreesRoot: worktreesRootFor(projectPath),
    worktrees: clone(worktreesFor(projectPath)),
  }),

  CreateProjectWorktree: async (projectPath: string, branch: string, base: string) => {
    const list = worktreesFor(projectPath)
    const worktree: FakeWorktree = {
      path: `${worktreesRootFor(projectPath)}/${branch || 'worktree'}`,
      branch: branch || `wt-${list.length}`,
      head: base || 'HEAD',
      detached: false,
      bare: false,
      locked: false,
      prunable: false,
      main: false,
      managed: true,
    }
    list.push(worktree)
    return clone(worktree)
  },

  RemoveProjectWorktree: async (projectPath: string, path: string) => {
    const list = worktreesFor(projectPath)
    const index = list.findIndex((item) => item.path === path)
    if (index >= 0) list.splice(index, 1)
  },

  ListMcpServers: async () => clone(fakeMcpServers),
  ListMcpTools: async () => clone(fakeMcpTools),
  ReloadMcp: async () => {},
  SaveMCPServer: async (input: {
    name: string
    command: string
    args: string[]
    env: Record<string, string>
  }) => {
    const existing = fakeMcpServers.find((item) => item.name === input.name)
    const envKeys = Object.keys(input.env ?? {}).sort()
    if (existing) {
      existing.command = input.command
      existing.args = [...input.args]
      existing.envKeys = envKeys
      existing.state = 'stopped'
      return
    }
    fakeMcpServers.push({
      name: input.name,
      command: input.command,
      args: [...input.args],
      envKeys,
      state: 'stopped',
      toolCount: 0,
    })
  },
  DeleteMCPServer: async (name: string) => {
    const index = fakeMcpServers.findIndex((item) => item.name === name)
    if (index >= 0) fakeMcpServers.splice(index, 1)
  },

  ListSkills: async () => clone(fakeSkills),
  ReloadSkills: async () => clone(fakeSkills),
  GetSkillDetail: async (name: string) => {
    const skill = fakeSkills.find((item) => item.name === name)
    if (!skill) throw new Error(`skill not found: ${name}`)
    return {
      ...clone(skill),
      path: `/tmp/bdesk-harness/.b-desktop/skills/${name}/SKILL.md`,
      content: `# ${skill.name}\n\n${skill.description}\n\n## 用法\n\n在对话中调用 \`Skill\` 工具加载。`,
    }
  },
  CreateSkill: async (input: { name: string; description: string; content: string }) => {
    if (fakeSkills.some((item) => item.name === input.name)) {
      throw new Error(`skill already exists: ${input.name}`)
    }
    fakeSkills.push({ name: input.name, description: input.description, source: 'user' })
    return {
      name: input.name,
      description: input.description,
      source: 'user',
      path: `/tmp/bdesk-harness/.b-desktop/skills/${input.name}/SKILL.md`,
      content: input.content,
    }
  },
  UpdateSkill: async (input: { name: string; description: string; content: string }) => {
    const skill = fakeSkills.find((item) => item.name === input.name)
    if (!skill) throw new Error(`skill not found: ${input.name}`)
    skill.description = input.description
    return {
      ...clone(skill),
      path: `/tmp/bdesk-harness/.b-desktop/skills/${input.name}/SKILL.md`,
      content: input.content,
    }
  },
  DeleteSkill: async (name: string) => {
    const index = fakeSkills.findIndex((item) => item.name === name)
    if (index >= 0) fakeSkills.splice(index, 1)
  },

  ListHooks: async (_workDir: string) => clone(fakeHooks),
  ReloadHooks: async (_workDir: string) => clone(fakeHooks),
  SaveHook: async (
    _workDir: string,
    input: {
      event: string
      matcher: string
      command: string
      scope: string
      originalEvent: string
      originalMatcher: string
      originalCommand: string
      originalScope: string
    },
  ) => {
    if (input.originalEvent) {
      const index = fakeHooks.findIndex(
        (item) =>
          item.event === input.originalEvent &&
          item.matcher === input.originalMatcher &&
          item.command === input.originalCommand &&
          item.source === (input.originalScope || input.scope),
      )
      if (index >= 0) fakeHooks.splice(index, 1)
    }
    fakeHooks.push({
      event: input.event,
      matcher: input.matcher,
      command: input.command,
      source: input.scope,
    })
    return clone(fakeHooks)
  },
  DeleteHook: async (
    _workDir: string,
    source: string,
    event: string,
    matcher: string,
    command: string,
  ) => {
    const index = fakeHooks.findIndex(
      (item) => item.source === source && item.event === event && item.matcher === matcher && item.command === command,
    )
    if (index >= 0) fakeHooks.splice(index, 1)
    return clone(fakeHooks)
  },

  ListSubagentTypes: async () => clone(fakeSubagentTypes),
  ListSubagentTasks: async () => clone(fakeSubagentTasks),
  CancelSubagentTask: async (id: string) => {
    const task = fakeSubagentTasks.find((item) => item.id === id)
    if (task) {
      task.status = 'canceled'
      task.finishedAt = new Date().toISOString()
    }
  },

  GetUsageSummary: async (days: number) => fakeUsageSummary(days),
  ListRequestTraces: async (limit: number) => fakeTraces(limit),

  ListModelPresets: async () => [
    {
      id: 'current',
      name: String(providerConfig.model || '当前模型（未配置）'),
      provider: String(providerConfig.provider || 'anthropic'),
      baseUrl: String(providerConfig.baseUrl || ''),
      defaultModel: String(providerConfig.model || ''),
      needsKey: true,
      local: false,
    },
  ],

  ListPermissionRules: async (sessionId: string) => (sessionId ? clone(fakeRules) : []),
  AddPermissionRule: async (_sessionId: string, input: Dict) => {
    const rule: FakeRule = {
      id: `rule_harness_${fakeRules.length + 1}`,
      tool: String(input.tool ?? ''),
      pattern: typeof input.pattern === 'string' ? input.pattern : undefined,
      matchMode: String(input.matchMode ?? 'exact'),
      effect: String(input.effect ?? 'ask'),
      source: 'session',
      note: typeof input.note === 'string' ? input.note : undefined,
    }
    fakeRules.push(rule)
    return clone(rule)
  },
  RemovePermissionRule: async (_sessionId: string, id: string) => {
    const index = fakeRules.findIndex((rule) => rule.id === id)
    if (index >= 0) fakeRules.splice(index, 1)
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
  toggleRightSidebar: () => useSettingsStore.getState().toggleRightSidebar(),
  openFile: (path: string) => useWorkbenchStore.getState().openFile(path),
  closePreview: () => useWorkbenchStore.getState().closePreview(),
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
      rightSidebarOpen: useSettingsStore.getState().rightSidebarOpen,
      rightSidebarWidth:
        (document.querySelector('[data-testid="right-sidebar"]') as HTMLElement | null)
          ?.offsetWidth ?? 0,
      fileRows: document.querySelectorAll('[data-testid="file-row"]').length,
      filePaths: [...document.querySelectorAll('[data-testid="file-row"]')].map(
        (element) => element.getAttribute('data-path') ?? '',
      ),
      filePreview: document.querySelector('[data-testid="file-preview"]') !== null,
      filePreviewPath: useWorkbenchStore.getState().preview?.path ?? null,
      filePreviewLines:
        (document.querySelector('[data-testid="file-preview-content"]')?.textContent ?? '').split(
          '\n',
        ).length,
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
  __bdesk: typeof api
  __bdeskReady: Promise<void>
}
globalWindow.__bdesk = api
globalWindow.__bdeskReady = ready
