import { createElement } from 'react'
import { createRoot } from 'react-dom/client'
import App from '../App'
import '../style.css'
import { applyTheme, readStoredTheme, type ThemeMode } from '../lib/theme'
import type { CharacterId } from '../lib/character'
import { useSettingsStore } from '../stores/settings'
import { usePreviewStore } from '../stores/preview'
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
  agent: string
}
interface FakeSessionInfo {
  id: string
  title: string
  workDir: string
  projectPath?: string
  messages: number
  updatedAtMs: number
  running: boolean
  questioning: boolean
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
    agent: 'benben',
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
  agent: 'benben',
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
  agent: 'benben',
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
    questioning: false,
  },
  {
    id: SESSION_EMPTY,
    title: '空会话',
    workDir: '/tmp/bdesk-harness',
    projectPath: '/tmp/bdesk-harness',
    messages: 0,
    updatedAtMs: NOW - 3_600_000,
    running: false,
    questioning: false,
  },
  {
    id: SESSION_ORPHAN,
    title: '未归属会话',
    workDir: '/tmp/orphan-workspace',
    messages: 0,
    updatedAtMs: NOW - 172_800_000,
    running: false,
    questioning: false,
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
  memoryAutoExtract: true,
}

// 假记忆库：与后端 internal/memory 的条目字段一致。
interface FakeMemory {
  dirKey: string
  name: string
  description: string
  type: string
  fileName: string
  scope: string
  keywords: string[]
  importance: number
  status: string
  createdAtMs: number
  updatedAtMs: number
  body: string
  sources: string[]
  accessCount: number
}

const fakeMemories: FakeMemory[] = [
  {
    dirKey: 'user',
    name: '提交前确认',
    description: '用户要求 git 提交前展示 diff',
    type: 'feedback',
    fileName: '提交前确认.md',
    scope: 'user',
    keywords: ['git', '提交'],
    importance: 8,
    status: 'active',
    createdAtMs: NOW - 7_200_000,
    updatedAtMs: NOW - 3_600_000,
    body: 'git 提交前必须先展示完整 diff 并获得确认，不要自动提交。',
    sources: ['turn:1'],
    accessCount: 2,
  },
]

let memoryFileSeq = 1

function slugify(name: string): string {
  const base = name.trim().toLowerCase().replace(/[^\p{L}\p{N}_-]+/gu, '-')
  return (base.replace(/^-+|-+$/g, '') || 'memory') + '.md'
}

// memoryDirKey 计算作用域目录键：用户级共享一个键，项目级按工作目录区分。
function memoryDirKey(scope: string, workDir: string): string {
  return scope === 'project' ? 'project:' + (workDir || '') : 'user'
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
    { name: 'main.go', path: 'main.go', isDir: false, isSymlink: false, size: 128 },
  ],
  src: [
    { name: 'components', path: 'src/components', isDir: true, isSymlink: false, size: 0 },
    { name: 'App.tsx', path: 'src/App.tsx', isDir: false, isSymlink: false, size: 64 },
    { name: 'main.rs', path: 'src/main.rs', isDir: false, isSymlink: false, size: 96 },
    { name: 'widget.c', path: 'src/widget.c', isDir: false, isSymlink: false, size: 80 },
    { name: 'crlf.txt', path: 'src/crlf.txt', isDir: false, isSymlink: false, size: 32 },
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
  'main.go':
    'package main\n\nimport "fmt"\n\n// Run 启动服务。\nfunc Run(port int) error {\n\tfmt.Println("listening", port)\n\treturn nil\n}\n',
  'src/main.rs':
    'fn main() {\n    let port: u16 = 8080;\n    println!("listening {port}");\n}\n',
  'src/widget.c':
    '#include <stdio.h>\n\nint add(int a, int b) {\n    return a + b;\n}\n',
  // CRLF 文件：验证预览不会把行距渲染成两倍。
  'src/crlf.txt': 'line one\r\nline two\r\nline three\r\n',
}

// fakeChanges 是变更审阅面板的假数据。
const fakeChanges = [
  {
    path: 'src/App.tsx',
    oldPath: '',
    status: 'modified',
    staged: false,
    additions: 3,
    deletions: 1,
    binary: false,
  },
  {
    path: 'main.go',
    oldPath: '',
    status: 'untracked',
    staged: false,
    additions: 8,
    deletions: 0,
    binary: false,
  },
]

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

// Dock 图标切换由原生层执行；harness 记录调用参数供断言。
const dockIconCalls: string[] = []

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

  // agent 选择器：benben 恒可用，claude 代表外部 ACP 后端。
  ListAgents: async () => [
    { id: 'benben', name: 'Benben Agent', description: '本地内置 agent', available: true, detail: '' },
    { id: 'claude', name: 'Claude Code', description: '通过内置 ACP 桥接驱动 claude CLI', available: true, detail: '' },
  ],

  SetSessionAgent: async (sessionId: string, agentId: string) => {
    const view = views.get(sessionId)
    if (!view) throw new Error(`session not found: ${sessionId}`)
    view.agent = agentId
  },

  SetDockIcon: async (character: string) => {
    dockIconCalls.push(character)
  },

  SetProviderConfig: async (input: {
    provider: string
    baseUrl?: string
    model?: string
    apiKey?: string
    contextWindow?: number
    maxOutputTokens?: number
    thinkingEnabled?: boolean
    thinkingBudget?: number
    memoryAutoExtract?: boolean
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
      memoryAutoExtract: input.memoryAutoExtract ?? providerConfig.memoryAutoExtract,
    }
  },

  ListMemories: async (scope: string, workDir: string) => {
    const dir = memoryDirKey(scope, workDir)
    return clone(
      fakeMemories
        .filter((mem) => mem.status === 'active' && mem.dirKey === dir)
        .sort((a, b) => b.updatedAtMs - a.updatedAtMs),
    )
  },

  ReadMemory: async (scope: string, workDir: string, fileName: string) => {
    const dir = memoryDirKey(scope, workDir)
    const mem = fakeMemories.find((item) => item.fileName === fileName && item.dirKey === dir)
    if (!mem) throw new Error(`memory not found: ${fileName}`)
    return {
      name: mem.name,
      description: mem.description,
      type: mem.type,
      fileName: mem.fileName,
      scope: mem.scope,
      keywords: [...mem.keywords],
      importance: mem.importance,
      status: mem.status,
      createdAtMs: mem.createdAtMs,
      updatedAtMs: mem.updatedAtMs,
      body: mem.body,
      sources: [...mem.sources],
      accessCount: mem.accessCount,
    }
  },

  SaveMemory: async (input: {
    scope: string
    workDir: string
    name: string
    description?: string
    type?: string
    keywords?: string[]
    importance?: number
    content: string
  }) => {
    const trimmed = input.content.trim()
    const dirKey = memoryDirKey(input.scope, input.workDir)
    const duplicate = fakeMemories.find((mem) => mem.body === trimmed && mem.status === 'active' && mem.dirKey === dirKey)
    if (duplicate) throw new Error('memory already exists')
    const now = Date.now()
    const mem: FakeMemory = {
      dirKey,
      name: input.name,
      description: input.description ?? trimmed.split('\n')[0] ?? '',
      type: input.type || 'project',
      fileName: slugify(input.name),
      scope: input.scope === 'project' ? 'project' : 'user',
      keywords: [...(input.keywords ?? [])],
      importance: input.importance ?? 5,
      status: 'active',
      createdAtMs: now,
      updatedAtMs: now,
      body: trimmed,
      sources: [],
      accessCount: 0,
    }
    while (fakeMemories.some((item) => item.dirKey === mem.dirKey && item.fileName === mem.fileName)) {
      memoryFileSeq += 1
      mem.fileName = mem.fileName.replace(/\.md$/, `-${memoryFileSeq}.md`)
    }
    fakeMemories.unshift(mem)
    return {
      name: mem.name,
      description: mem.description,
      type: mem.type,
      fileName: mem.fileName,
      scope: mem.scope,
      keywords: [...mem.keywords],
      importance: mem.importance,
      status: mem.status,
      createdAtMs: mem.createdAtMs,
      updatedAtMs: mem.updatedAtMs,
    }
  },

  UpdateMemory: async (input: {
    scope: string
    workDir: string
    fileName: string
    name?: string
    description?: string
    type?: string
    keywords?: string[]
    importance?: number
    content?: string
  }) => {
    const dir = memoryDirKey(input.scope, input.workDir)
    const mem = fakeMemories.find((item) => item.fileName === input.fileName && item.dirKey === dir)
    if (!mem) throw new Error(`memory not found: ${input.fileName}`)
    const nextBody = input.content?.trim()
    if (nextBody && nextBody !== mem.body) {
      mem.body = nextBody
      mem.updatedAtMs = Date.now()
    }
    if (input.name && input.name !== mem.name) {
      mem.name = input.name
      mem.updatedAtMs = Date.now()
    }
    if (input.description !== undefined && input.description !== mem.description) {
      mem.description = input.description
      mem.updatedAtMs = Date.now()
    }
    if (input.type && input.type !== mem.type) mem.type = input.type
    if (input.keywords && JSON.stringify(input.keywords) !== JSON.stringify(mem.keywords)) {
      mem.keywords = [...input.keywords]
      mem.updatedAtMs = Date.now()
    }
    if (input.importance && input.importance !== mem.importance) mem.importance = input.importance
    return {
      name: mem.name,
      description: mem.description,
      type: mem.type,
      fileName: mem.fileName,
      scope: mem.scope,
      keywords: [...mem.keywords],
      importance: mem.importance,
      status: mem.status,
      createdAtMs: mem.createdAtMs,
      updatedAtMs: mem.updatedAtMs,
    }
  },

  DeleteMemory: async (scope: string, workDir: string, fileName: string) => {
    const dir = memoryDirKey(scope, workDir)
    const mem = fakeMemories.find((item) => item.fileName === fileName && item.dirKey === dir)
    if (!mem) throw new Error(`memory not found: ${fileName}`)
    mem.status = 'archived'
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

  ReadSessionImage: async (_sessionId: string, _workDir: string, path: string) => {
    if (!path.endsWith('.png')) throw new Error(`not an image: ${path}`)
    // 1x1 红色 PNG，供图片预览断言使用。
    return {
      path,
      size: 68,
      mediaType: 'image/png',
      dataBase64:
        'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==',
    }
  },

  // 文件写操作：改的是内存里的假目录树与假文件内容。
  CreateSessionFile: async (_sessionId: string, _workDir: string, path: string, isDir: boolean) => {
    const segments = path.split('/')
    const name = segments.pop() ?? path
    const parent = segments.join('/')
    const list = fakeFileEntries[parent] ?? (fakeFileEntries[parent] = [])
    if (!list.some((item) => item.path === path)) {
      list.push({ name, path, isDir, isSymlink: false, size: 0 })
    }
    if (!isDir) fakeFileContents[path] = ''
  },

  RenameSessionFile: async (_sessionId: string, _workDir: string, from: string, to: string) => {
    const oldParent = from.split('/').slice(0, -1).join('/')
    const newParent = to.split('/').slice(0, -1).join('/')
    const oldList = fakeFileEntries[oldParent]
    const entry = oldList?.find((item) => item.path === from)
    if (oldList && entry) {
      // 条目要换到新父目录的列表里，否则树刷新后还是旧位置。
      oldList.splice(oldList.indexOf(entry), 1)
      entry.path = to
      entry.name = to.split('/').pop() ?? to
      const newList = fakeFileEntries[newParent] ?? (fakeFileEntries[newParent] = [])
      if (!newList.some((item) => item.path === to)) newList.push(entry)
    }
    if (fakeFileContents[from] !== undefined) {
      fakeFileContents[to] = fakeFileContents[from]
      delete fakeFileContents[from]
    }
  },

  MoveSessionFile: async (_sessionId: string, _workDir: string, from: string, toDir: string) => {
    const name = from.split('/').pop() ?? from
    const to = toDir === '' ? name : `${toDir}/${name}`
    await bindings.RenameSessionFile('', '', from, to)
    return to
  },

  DeleteSessionFile: async (_sessionId: string, _workDir: string, path: string) => {
    const parent = path.split('/').slice(0, -1).join('/')
    const list = fakeFileEntries[parent]
    if (list) {
      const index = list.findIndex((item) => item.path === path)
      if (index >= 0) list.splice(index, 1)
    }
    delete fakeFileContents[path]
  },

  WriteSessionFile: async (_sessionId: string, _workDir: string, path: string, content: string) => {
    fakeFileContents[path] = content
  },

  SearchSessionFiles: async (_sessionId: string, _workDir: string, query: string) => {
    const needle = query.trim().toLowerCase()
    if (needle === '') return []
    return Object.keys(fakeFileContents).filter((path) => path.toLowerCase().includes(needle))
  },

  ListSessionChanges: async () => ({
    root: '/tmp/bdesk-harness',
    branch: 'main',
    isRepo: true,
    changes: clone(fakeChanges),
  }),

  GetSessionFileDiff: async (_sessionId: string, _workDir: string, path: string, staged: boolean) => {
    const content = fakeFileContents[path] ?? ''
    const lines = content.split('\n')
    const body = lines
      .map((line) => `+${line}`)
      .join('\n')
    return {
      path,
      oldPath: '',
      status: 'modified',
      diff: `diff --git a/${path} b/${path}\n--- a/${path}\n+++ b/${path}\n@@ -1,0 +1,${lines.length} @@\n${body}\n`,
      additions: lines.length,
      deletions: 0,
      binary: false,
      staged,
    }
  },

  StageSessionFiles: async () => undefined,
  UnstageSessionFiles: async () => undefined,
  DiscardSessionFile: async () => undefined,

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
      agent: 'benben',
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
      questioning: false,
    }
    sessions.unshift(info)
    return clone(info)
  },

  // 分叉会话：深拷贝视图与新列表项，标题追加后缀（与后端 ForkSession 语义一致）。
  ForkSession: async (sessionId: string) => {
    const src = views.get(sessionId)
    if (!src) throw new Error(`session not found: ${sessionId}`)
    const id = `s_harness_${views.size + 1}`
    const forked: FakeView = {
      ...clone(src),
      id,
      title: `${src.title} (fork)`,
      createdAtMs: Date.now(),
      running: false,
    }
    views.set(id, forked)
    const srcInfo = sessions.find((item) => item.id === sessionId)
    const info: FakeSessionInfo = {
      id,
      title: forked.title,
      workDir: forked.workDir,
      projectPath: srcInfo?.projectPath ?? projectPathForWorkDir(forked.workDir),
      messages: srcInfo?.messages ?? forked.messages.length,
      updatedAtMs: Date.now(),
      running: false,
      questioning: false,
    }
    sessions.unshift(info)
    return clone(info)
  },

  SendMessage: async (sessionId: string, text: string, images?: { mediaType?: string; data: string }[]) => {
    const view = views.get(sessionId)
    const firstMessage = (view?.messages.length ?? 0) === 0
    const turnId = `turn_harness_${(turnSeq += 1)}`
    const messageId = `msg_harness_${(msgSeq += 1)}`
    activeTurns.set(sessionId, turnId)

    if (view) {
      view.messages.push({
        id: `${messageId}_user`,
        role: 'user',
        blocks: [
          ...(text ? [{ type: 'text', text }] : []),
          ...(images ?? []).map((img, index) => ({
            type: 'image',
            mediaType: img.mediaType ?? 'image/png',
            data: img.data,
            _index: index,
          })),
        ],
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

    // 交错流式场景：思考 → 文本 → 工具 → 工具之后又出现的文本。
    // 用来验证「工具调用之间的模型输出」不会被并进工具之前那段文本。
    const INTERLEAVED_THINKING =
      '先确认要读哪个文件，再决定后续动作：需要看看 README 里写了什么，' +
      '确认工具参数怎么写，最后再总结结论。'
    const INTERLEAVED_TAIL = '读完了：文件里确认了两处待办，下面是结论。'
    // 模拟一轮 TodoWrite 计划：todo_update 事件驱动右上角计划面板。
    emitEvent({
      type: 'todo_update',
      sessionId,
      turnId,
      todos: {
        items: [
          { content: '调研现有实现', status: 'completed' },
          { content: '补齐缺失功能', status: 'in_progress', activeForm: '正在补齐缺失功能' },
          { content: '端到端验证并构建', status: 'pending' },
        ],
      },
    })
    const steps: Array<() => boolean> = [
      // 思考增量
      () => {
        const parts = INTERLEAVED_THINKING.match(/[\s\S]{1,8}/g) ?? []
        const part = parts[thinkingCursor]
        if (part === undefined) return true
        thinkingCursor += 1
        emitEvent({
          type: 'thinking_delta',
          sessionId,
          turnId,
          messageId,
          blockIndex: 0,
          text: part,
        })
        return false
      },      // 工具之前的文本
      () => {
        const parts = '我来读一下这个文件。'.match(/[\s\S]{1,8}/g) ?? []
        const part = parts[beforeCursor]
        if (part === undefined) return true
        beforeCursor += 1
        emitEvent({
          type: 'text_delta',
          sessionId,
          turnId,
          messageId,
          blockIndex: 1,
          text: part,
        })
        return false
      },
      // 工具调用
      () => {
        emitEvent({
          type: 'tool_call_start',
          sessionId,
          turnId,
          messageId,
          blockIndex: 2,
          toolUseId: 'toolu_interleaved',
          toolName: 'Read',
        })
        emitEvent({
          type: 'tool_call_ready',
          sessionId,
          turnId,
          messageId,
          blockIndex: 2,
          toolUseId: 'toolu_interleaved',
          toolName: 'Read',
          input: { file_path: '/tmp/bdesk-harness/README.md' },
        })
        return true
      },
      // 工具之后又出现的文本：必须是独立块（blockIndex 3）。
      () => {
        const parts = INTERLEAVED_TAIL.match(/[\s\S]{1,8}/g) ?? []
        const part = parts[tailCursor]
        if (part === undefined) return true
        tailCursor += 1
        emitEvent({
          type: 'text_delta',
          sessionId,
          turnId,
          messageId,
          blockIndex: 3,
          text: part,
        })
        return false
      },
    ]
    let thinkingCursor = 0
    let beforeCursor = 0
    let tailCursor = 0
    let stepIndex = 0
    const pumpSteps = () => {
      if (activeTurns.get(sessionId) !== turnId) return
      while (stepIndex < steps.length) {
        const done = steps[stepIndex]()
        if (!done) {
          // 放慢节奏，便于观察流式中间态（思考中、块逐步增长）。
          setTimeout(pumpSteps, 120)
          return
        }
        stepIndex += 1
      }
      emitEvent({
        type: 'tool_result',
        sessionId,
        turnId,
        toolUseId: 'toolu_interleaved',
        toolName: 'Read',
        result: { preview: '# demo\nok', isError: false, truncated: false, bytes: 9 },
      })
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
      pumpSteps()
    }, 30)

    return { sessionId, turnId, running: true }
  },

  CancelTurn: async (sessionId: string) => {
    const turnId = activeTurns.get(sessionId)
    if (!turnId) return
    activeTurns.delete(sessionId)
    emitEvent({ type: 'turn_end', sessionId, turnId, stopReason: 'canceled' })
  },

  ApproveTool: async (sessionId: string, requestId: string, option: string, _updatedInput?: string) => {
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
  ListSlashCommands: async (_workDir: string) => [
    { name: 'compact', description: '压缩当前会话上下文，生成摘要并保留最近消息', argumentHint: '', source: 'builtin' },
    { name: 'clear', description: '清空当前对话，在同一项目下开始新会话', argumentHint: '', source: 'builtin' },
    { name: 'model', description: '查看或切换模型：/model <模型名>；不带参数打开设置', argumentHint: '', source: 'builtin' },
    { name: 'export', description: '导出当前会话为 Markdown 文件并复制到剪贴板', argumentHint: '', source: 'builtin' },
    ...fakeSkills.map((skill) => ({
      name: skill.name,
      description: skill.description,
      argumentHint: '',
      source: 'skill',
    })),
  ],
  CompactSession: async (sessionId: string) => {
    const view = views.get(sessionId)
    if (!view || view.messages.length === 0) {
      throw new Error('app: compact session: context: nothing to compact')
    }
    emitEvent({ type: 'compact_start', sessionId })
    const summary = {
      id: `msg_harness_${(msgSeq += 1)}_summary`,
      role: 'user' as const,
      blocks: [
        {
          type: 'text',
          text: 'This session is being continued from a previous conversation that ran out of context. The summary below covers the earlier portion of the conversation:\n\n（此前对话的摘要：工作台会话进行中，已确认 @ 引用与 / 命令可用。）',
        },
      ],
      timeMs: Date.now(),
    }
    view.messages = [summary, ...view.messages.slice(-2)]
    const result = { preTokens: 12800, postTokens: 4200 }
    emitEvent({
      type: 'compact_end',
      sessionId,
      compact: { trigger: 'manual', ...result },
    })
    return { ...result }
  },

  ListRewindPoints: async (sessionId: string) => {
    const view = views.get(sessionId)
    if (!view) return []
    const points: { messageId: string; preview: string; imageCount: number; timeMs: number }[] = []
    for (const message of view.messages) {
      if (message.role !== 'user') continue
      const text = message.blocks
        .filter((block) => block.type === 'text')
        .map((block) => ((block as { text?: string }).text ?? ''))
        .filter((value) => !value.startsWith('<system-reminder>'))
        .join('\n')
      if (!text.trim()) continue
      points.push({
        messageId: message.id,
        preview: Array.from(text).slice(0, 160).join(''),
        imageCount: 0,
        timeMs: message.timeMs,
      })
    }
    return points
  },

  RewindSession: async (sessionId: string, messageId: string) => {
    const view = views.get(sessionId)
    if (!view) throw new Error('app: session not found')
    const index = view.messages.findIndex((message) => message.id === messageId)
    if (index < 0) throw new Error(`app: rewind session: user message not found: ${messageId}`)
    const target = view.messages[index]
    const text = target.blocks
      .filter((block) => block.type === 'text')
      .map((block) => ((block as { text?: string }).text ?? ''))
      .filter((value) => !value.startsWith('<system-reminder>'))
      .join('\n')
    view.messages = view.messages.slice(0, index)
    return { messageId, text, images: [] }
  },

  ExportSession: async (sessionId: string, _path: string) => {
    const view = views.get(sessionId)
    if (!view || view.messages.length === 0) {
      throw new Error('app: export session: nothing to export')
    }
    return {
      path: `/tmp/bdesk-harness/exports/${sessionId}.md`,
      messageCount: view.messages.length,
      bytes: 2048,
    }
  },

  CreateTerminal: async (sessionId: string) => ({
    id: `tty_harness_${sessionId}`,
    sessionId,
    workDir: '/tmp/bdesk-harness',
  }),
  TerminalWrite: async () => undefined,
  TerminalResize: async () => undefined,
  CloseTerminal: async () => undefined,

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

  /** 注入 memory:changed 事件（模拟轮末自动抽取完成的通知）。 */
  emitMemoryChanged: (payload: Record<string, unknown>) => emitRaw('memory:changed', payload),

  /** 读取 harness 假记忆库（含归档条目），供断言使用。 */
  memories: () => clone(fakeMemories),

  /** 读取当前自动抽取开关。 */
  memoryAutoExtract: () => providerConfig.memoryAutoExtract === true,

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
  openFile: (path: string) => usePreviewStore.getState().openFile(path),
  closePreview: () => usePreviewStore.setState({ active: null }),
  openChanges: () => usePreviewStore.getState().openChanges(),
  setTheme: (mode: ThemeMode) => useSettingsStore.getState().setTheme(mode),
  setCharacter: (id: CharacterId) => useSettingsStore.getState().setCharacter(id),
  setPermissionMode: (mode: PermissionMode) => useWorkbenchStore.getState().setPermissionMode(mode),
  setAgent: (agentId: string) => useWorkbenchStore.getState().setAgent(agentId),

  /** 一次取回所有可断言的 DOM/状态指标。 */
  metrics: () => {
    const markdown = document.querySelector('[data-testid="markdown"]')
    return {
      theme: document.documentElement.dataset.theme ?? null,
      character: document.documentElement.dataset.character ?? null,
      dockIconCalls: [...dockIconCalls],
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
      toolMascots: document.querySelectorAll('[data-testid="tool-mascot"]').length,
      toolMascotPoses: [
        ...document.querySelectorAll('[data-testid="tool-mascot"] [data-mascot-pose]'),
      ].map((element) => element.getAttribute('data-mascot-pose')),
      agentKind: useWorkbenchStore.getState().agentKind,
      agentOptions: [...document.querySelectorAll('[data-testid="agent-menu"] [role="menuitemradio"]')]
        .map((element) => element.getAttribute('data-testid')?.replace('agent-option-', '') ?? null),
      agentSelectLabel: textOf('[data-testid="agent-select"]'),
      subagentThreads: document.querySelectorAll('[data-testid^="subagent-thread-"]').length,
      thinkingToggle: document.querySelector('[data-testid="thinking-toggle"]') !== null,
      askMascot: document.querySelector('[data-testid="ask-mascot"]') !== null,
      sessionMascots: document.querySelectorAll('[data-testid="session-mascot"]').length,
      sessionMascotPoses: [
        ...document.querySelectorAll('[data-testid="session-mascot"] [data-mascot-pose]'),
      ].map((element) => element.getAttribute('data-mascot-pose')),
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
      filePreview: document.querySelector('[data-testid="preview-pane"]') !== null,
      filePreviewImage: document.querySelector('[data-testid="file-preview-image"]') !== null,
      filePreviewPath: usePreviewStore.getState().active,
      filePreviewMarkdown: document.querySelector('[data-testid="file-preview-markdown"]') !== null,
      markdownViewMode:
        document
          .querySelector('[data-testid="markdown-view-toggle"]')
          ?.getAttribute('data-mode') ?? null,
      filePreviewLines:
        (document.querySelector('[data-testid="file-preview-content"]')?.textContent ?? '').split(
          '\n',
        ).length,
      previewPaneWidth:
        (document.querySelector('[data-testid="preview-pane"]') as HTMLElement | null)
          ?.offsetWidth ?? 0,
      previewTabs: document.querySelectorAll('[data-testid="preview-tab"]').length,
      previewTokens: document.querySelectorAll(
        '[data-testid="file-preview-content"] .hljs-keyword, [data-testid="file-preview-content"] .hljs-string, [data-testid="file-preview-content"] .hljs-title, [data-testid="file-preview-content"] .hljs-comment',
      ).length,
      changesPanel: document.querySelector('[data-testid="changes-panel"]') !== null,
      changeRows: document.querySelectorAll('[data-testid="change-row"]').length,
      reviewDiff: document.querySelector('[data-testid="review-diff"]') !== null,
      diffTokens: document.querySelectorAll('[data-testid="review-diff"] .hljs-keyword').length,
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
