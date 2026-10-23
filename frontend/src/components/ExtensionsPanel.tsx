import { useCallback, useEffect, useRef, useState } from 'react'
import {
  CancelSubagentTask,
  GetSkillDetail,
  ListMcpServers,
  ListMcpTools,
  ListSkills,
  ListSubagentTasks,
  ListSubagentTypes,
  ReloadMcp,
  ReloadSkills,
} from '../../wailsjs/go/app/App'
import type { app } from '../../wailsjs/go/models'
import { formatRelative, formatTokens } from '../lib/format'
import { errorMessage } from '../stores/workbench'
import { MarkdownText } from './MarkdownText'

// 扩展管理面板：MCP / Skills / SubAgents 三个标签页，数据全部来自 Wails 绑定。

export type ExtensionsTabId = 'mcp' | 'skills' | 'subagents'

export interface ExtensionsPanelProps {
  // initialTab 只在首次挂载时生效。
  initialTab?: ExtensionsTabId
  // onClose 存在时在标题栏显示关闭按钮，由外部决定关闭行为。
  onClose?: () => void
  className?: string
}

const TABS: ReadonlyArray<{ id: ExtensionsTabId; label: string }> = [
  { id: 'mcp', label: 'MCP' },
  { id: 'skills', label: 'Skills' },
  { id: 'subagents', label: 'SubAgents' },
]

const SOURCE_LABEL: Record<string, string> = {
  user: '用户级',
  project: '项目级',
}

// mcp.ServerStatus.State 只有 stopped / ready / error 三种取值。
const MCP_STATE: Record<string, { label: string; dot: string; text: string }> = {
  ready: { label: '已连接', dot: 'bg-emerald-500', text: 'text-success-text' },
  error: { label: '连接失败', dot: 'bg-rose-500', text: 'text-danger-text' },
  stopped: { label: '未启动', dot: 'bg-amber-500', text: 'text-warn-text' },
}

const MCP_STATE_UNKNOWN = { label: '未知', dot: 'bg-slate-400', text: 'text-subtle' }

// subagent.TaskStatus 取值：running / completed / failed / canceled。
const TASK_STATUS: Record<string, { label: string; dot: string; chip: string }> = {
  running: {
    label: '运行中',
    dot: 'bg-sky-400',
    chip: 'border-line bg-raised text-muted',
  },
  completed: {
    label: '已完成',
    dot: 'bg-emerald-500',
    chip: 'border-success-line bg-success-soft text-success-text',
  },
  failed: {
    label: '失败',
    dot: 'bg-rose-500',
    chip: 'border-danger-line bg-danger-soft text-danger-text',
  },
  canceled: {
    label: '已取消',
    dot: 'bg-slate-400',
    chip: 'border-line bg-raised text-subtle',
  },
}

const TASK_STATUS_UNKNOWN = {
  label: '未知',
  dot: 'bg-slate-400',
  chip: 'border-line bg-raised text-subtle',
}

function sourceLabel(source: string): string {
  return SOURCE_LABEL[source] ?? (source || '未知')
}

// parseTime 把后端 RFC3339 字符串转成毫秒；非法值返回 0（交给 formatRelative 忽略）。
function parseTime(value: string): number {
  if (!value) return 0
  const ms = Date.parse(value)
  return Number.isNaN(ms) ? 0 : ms
}

function RefreshIcon({ spinning = false }: { spinning?: boolean }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className={`size-3 shrink-0 ${spinning ? 'animate-spin' : ''}`}
      aria-hidden="true"
    >
      <path d="M21 12a9 9 0 1 1-2.6-6.4" />
      <path d="M21 3v6h-6" />
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

function CloseIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      className="size-4"
      aria-hidden="true"
    >
      <path d="M6 6l12 12M18 6 6 18" />
    </svg>
  )
}

function RefreshButton({
  busy,
  onClick,
  label = '刷新',
}: {
  busy: boolean
  onClick: () => void
  label?: string
}) {
  return (
    <button
      type="button"
      disabled={busy}
      onClick={onClick}
      className="flex shrink-0 cursor-pointer items-center gap-1 rounded-md border border-line bg-raised px-2 py-1 text-[11px] text-muted transition hover:bg-hover hover:text-fg disabled:cursor-not-allowed disabled:opacity-50"
    >
      <RefreshIcon spinning={busy} />
      {busy ? '刷新中…' : label}
    </button>
  )
}

function ErrorNote({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <div className="mb-3 flex items-start gap-2 rounded-lg border border-danger-line bg-danger-soft px-3 py-2 text-[11px] leading-relaxed text-danger-text">
      <span className="min-w-0 flex-1 whitespace-pre-wrap break-words">{message}</span>
      {onRetry && (
        <button
          type="button"
          onClick={onRetry}
          className="shrink-0 cursor-pointer rounded border border-danger-line px-1.5 py-0.5 transition hover:bg-hover"
        >
          重试
        </button>
      )}
    </div>
  )
}

function LoadingRow({ text }: { text: string }) {
  return <p className="py-10 text-center text-xs text-subtle">{text}</p>
}

function EmptyHint({ title, hint }: { title: string; hint: string }) {
  return (
    <div className="px-4 py-10 text-center">
      <p className="text-xs text-muted">{title}</p>
      <p className="mt-1 text-[11px] leading-relaxed text-subtle">{hint}</p>
    </div>
  )
}

function SourceBadge({ source }: { source: string }) {
  return (
    <span className="shrink-0 rounded-full border border-line bg-raised px-1.5 py-0.5 text-[10px] text-muted">
      {sourceLabel(source)}
    </span>
  )
}

function groupToolsByServer(tools: app.MCPToolDTO[]): Map<string, app.MCPToolDTO[]> {
  const grouped = new Map<string, app.MCPToolDTO[]>()
  for (const tool of tools) {
    const key = tool.server || '未知服务器'
    const bucket = grouped.get(key)
    if (bucket) bucket.push(tool)
    else grouped.set(key, [tool])
  }
  return grouped
}

interface McpServerItemProps {
  server: app.MCPServerDTO
  tools: app.MCPToolDTO[]
  expanded: boolean
  onToggle: (name: string) => void
}

function McpServerItem({ server, tools, expanded, onToggle }: McpServerItemProps) {
  const state = MCP_STATE[server.state] ?? MCP_STATE_UNKNOWN
  const commandLine = [server.command, ...server.args].filter(Boolean).join(' ')
  return (
    <li className="overflow-hidden rounded-lg border border-line bg-surface">
      <button
        type="button"
        aria-expanded={expanded}
        onClick={() => onToggle(server.name)}
        className="flex w-full cursor-pointer items-start gap-2 px-3 py-2 text-left transition hover:bg-hover"
      >
        <span className="mt-1">
          <ChevronIcon open={expanded} />
        </span>
        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="flex min-w-0 items-center gap-1.5">
            <span className="truncate text-xs font-medium text-fg">{server.name}</span>
            <span className={`flex shrink-0 items-center gap-1 text-[10px] ${state.text}`}>
              <span className={`size-1.5 rounded-full ${state.dot}`} />
              {state.label}
            </span>
          </span>
          <span className="truncate font-mono text-[10px] text-subtle" title={commandLine}>
            {commandLine || '(无启动命令)'}
          </span>
        </span>
        <span className="shrink-0 rounded-full border border-line bg-raised px-1.5 py-0.5 text-[10px] tabular-nums text-muted">
          {server.toolCount} 工具
        </span>
      </button>

      {expanded && (
        <div className="border-t border-line bg-panel px-3 py-2">
          {(server.serverName || server.protocolVersion) && (
            <p className="mb-2 text-[10px] text-subtle">
              {server.serverName || server.name}
              {server.serverVersion ? ` v${server.serverVersion}` : ''}
              {server.protocolVersion ? ` · MCP ${server.protocolVersion}` : ''}
            </p>
          )}
          {server.error && (
            <p className="mb-2 whitespace-pre-wrap break-words text-[11px] text-danger-text">
              {server.error}
            </p>
          )}
          {tools.length === 0 ? (
            <p className="text-[11px] text-subtle">暂无可用工具</p>
          ) : (
            <ul className="space-y-1.5">
              {tools.map((tool) => (
                <li key={tool.name} className="min-w-0">
                  <span className="flex items-center gap-1.5">
                    <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-fg">
                      {tool.name}
                    </span>
                    {tool.readOnly && (
                      <span className="shrink-0 rounded bg-raised px-1 text-[9px] text-subtle">
                        只读
                      </span>
                    )}
                  </span>
                  {tool.description && (
                    <span className="mt-0.5 line-clamp-2 text-[10px] leading-relaxed text-subtle">
                      {tool.description}
                    </span>
                  )}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </li>
  )
}

function McpTab() {
  const [servers, setServers] = useState<app.MCPServerDTO[]>([])
  const [tools, setTools] = useState<app.MCPToolDTO[]>([])
  const [loaded, setLoaded] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(new Set())

  // load 同时取服务器状态与工具列表；任一失败不影响另一部分展示。
  const load = useCallback(async (isAlive: () => boolean = () => true, extraError?: string) => {
    setBusy(true)
    const [serverResult, toolResult] = await Promise.allSettled([
      ListMcpServers(),
      ListMcpTools(),
    ])
    if (!isAlive()) return

    const problems: string[] = []
    if (extraError) problems.push(extraError)
    if (serverResult.status === 'fulfilled') {
      setServers(serverResult.value)
    } else {
      problems.push(`服务器状态：${errorMessage(serverResult.reason)}`)
    }
    if (toolResult.status === 'fulfilled') {
      setTools(toolResult.value)
    } else {
      problems.push(`工具列表：${errorMessage(toolResult.reason)}`)
    }
    setError(problems.length > 0 ? problems.join('\n') : null)
    setLoaded(true)
    setBusy(false)
  }, [])

  useEffect(() => {
    let alive = true
    void load(() => alive)
    return () => {
      alive = false
    }
  }, [load])

  // ReloadMcp 会重读 mcp.json 并重连；失败（含单服务器连接错误）后仍刷新列表以展示最新状态。
  const reload = useCallback(async () => {
    setBusy(true)
    let reloadError: string | undefined
    try {
      await ReloadMcp()
    } catch (err) {
      reloadError = `重载 MCP：${errorMessage(err)}`
    }
    await load(() => true, reloadError)
  }, [load])

  const toggleServer = useCallback((name: string) => {
    setExpanded((current) => {
      const next = new Set(current)
      if (next.has(name)) next.delete(name)
      else next.add(name)
      return next
    })
  }, [])

  const groupedTools = groupToolsByServer(tools)

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex items-center justify-between gap-3 border-b border-line px-4 py-2.5">
        <p className="text-[11px] text-subtle">
          {servers.length} 个服务器 · {tools.length} 个工具
        </p>
        <RefreshButton busy={busy} onClick={() => void reload()} label="重载" />
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto px-3 py-3">
        {error && <ErrorNote message={error} onRetry={() => void load()} />}
        {!loaded ? (
          <LoadingRow text="正在连接 MCP 服务器…" />
        ) : servers.length === 0 ? (
          <EmptyHint
            title="没有 MCP 服务器"
            hint="在 ~/.b-desktop/mcp.json 中配置服务器后点击“重载”"
          />
        ) : (
          <ul className="space-y-1.5">
            {servers.map((server) => (
              <McpServerItem
                key={server.name}
                server={server}
                tools={groupedTools.get(server.name) ?? []}
                expanded={expanded.has(server.name)}
                onToggle={toggleServer}
              />
            ))}
          </ul>
        )}
      </div>
    </div>
  )
}

interface SkillRowProps {
  skill: app.SkillDTO
  active: boolean
  onSelect: (name: string) => void
}

function SkillRow({ skill, active, onSelect }: SkillRowProps) {
  return (
    <button
      type="button"
      onClick={() => onSelect(skill.name)}
      className={`w-full cursor-pointer rounded-md border px-2 py-1.5 text-left transition ${
        active
          ? 'border-line-strong bg-hover'
          : 'border-transparent hover:bg-hover'
      }`}
    >
      <span className="flex min-w-0 items-center gap-1.5">
        <span className="min-w-0 flex-1 truncate text-xs font-medium text-fg">{skill.name}</span>
        <SourceBadge source={skill.source} />
      </span>
      <span className="mt-0.5 line-clamp-2 text-[10px] leading-relaxed text-subtle">
        {skill.description || '无描述'}
      </span>
    </button>
  )
}

function SkillsTab() {
  const [skills, setSkills] = useState<app.SkillDTO[]>([])
  const [loaded, setLoaded] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [selected, setSelected] = useState<string | null>(null)
  const [detail, setDetail] = useState<app.SkillDetailDTO | null>(null)
  const [detailBusy, setDetailBusy] = useState(false)
  const [detailError, setDetailError] = useState<string | null>(null)
  // detailSeq 丢弃快速切换时先返回的旧详情。
  const detailSeq = useRef(0)

  const load = useCallback(async (isAlive: () => boolean = () => true, extraError?: string) => {
    setBusy(true)
    try {
      const list = await ListSkills()
      if (!isAlive()) return
      setSkills(list)
      setError(extraError ?? null)
    } catch (err) {
      if (!isAlive()) return
      setError(
        [extraError, errorMessage(err)].filter((item): item is string => Boolean(item)).join('；'),
      )
    } finally {
      if (isAlive()) {
        setLoaded(true)
        setBusy(false)
      }
    }
  }, [])

  useEffect(() => {
    let alive = true
    void load(() => alive)
    return () => {
      alive = false
    }
  }, [load])

  const openSkill = useCallback(async (name: string) => {
    detailSeq.current += 1
    const seq = detailSeq.current
    setSelected(name)
    // 先清空旧正文再加载，避免短暂显示上一个技能的内容。
    setDetail(null)
    setDetailError(null)
    setDetailBusy(true)
    try {
      const next = await GetSkillDetail(name)
      if (detailSeq.current !== seq) return
      setDetail(next)
      setDetailBusy(false)
    } catch (err) {
      if (detailSeq.current !== seq) return
      setDetail(null)
      setDetailError(errorMessage(err))
      setDetailBusy(false)
    }
  }, [])

  // ReloadSkills 重新扫描目录并返回新清单，直接用返回值更新列表。
  const reload = useCallback(async () => {
    setBusy(true)
    let reloadError: string | undefined
    try {
      const list = await ReloadSkills()
      setSkills(list)
      if (selected && !list.some((skill) => skill.name === selected)) {
        // 选中的技能已被删除：丢弃可能仍在途的详情请求。
        detailSeq.current += 1
        setSelected(null)
        setDetail(null)
        setDetailError(null)
      }
    } catch (err) {
      reloadError = `重载 Skills：${errorMessage(err)}`
    }
    setError(reloadError ?? null)
    setLoaded(true)
    setBusy(false)
  }, [selected])

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex items-center justify-between gap-3 border-b border-line px-4 py-2.5">
        <p className="text-[11px] text-subtle">{skills.length} 个技能</p>
        <RefreshButton busy={busy} onClick={() => void reload()} label="重载" />
      </div>
      <div className="flex min-h-0 flex-1">
        <div className="min-h-0 w-60 shrink-0 overflow-y-auto border-r border-line px-2 py-2">
          {error && <ErrorNote message={error} onRetry={() => void load()} />}
          {!loaded ? (
            <LoadingRow text="正在读取技能…" />
          ) : skills.length === 0 ? (
            <EmptyHint
              title="还没有技能"
              hint="把 SKILL.md 放进 ~/.b-desktop/skills 或 <项目>/.b-desktop/skills，然后重载"
            />
          ) : (
            <div className="space-y-0.5">
              {skills.map((skill) => (
                <SkillRow
                  key={skill.name}
                  skill={skill}
                  active={skill.name === selected}
                  onSelect={(name) => void openSkill(name)}
                />
              ))}
            </div>
          )}
        </div>

        <div className="flex min-h-0 min-w-0 flex-1 flex-col">
          {selected === null ? (
            <EmptyHint title="选择左侧技能查看正文" hint="点击技能名加载完整 SKILL.md" />
          ) : detailBusy ? (
            <LoadingRow text={`正在读取 ${selected}…`} />
          ) : detailError ? (
            <div className="px-4 py-3">
              <ErrorNote message={detailError} onRetry={() => void openSkill(selected)} />
            </div>
          ) : detail ? (
            <>
              <div className="border-b border-line px-4 py-3">
                <div className="flex min-w-0 flex-wrap items-center gap-2">
                  <h3 className="min-w-0 truncate text-sm font-semibold text-fg">{detail.name}</h3>
                  <SourceBadge source={detail.source} />
                </div>
                {detail.description && (
                  <p className="mt-1 text-[11px] leading-relaxed text-muted">{detail.description}</p>
                )}
                <p className="mt-1 break-all font-mono text-[10px] text-subtle">{detail.path}</p>
              </div>
              <div className="min-h-0 flex-1 overflow-y-auto px-4 py-3">
                <MarkdownText text={detail.content} />
              </div>
            </>
          ) : (
            <LoadingRow text={`正在读取 ${selected}…`} />
          )}
        </div>
      </div>
    </div>
  )
}

interface SubagentTaskRowProps {
  task: app.SubagentTaskDTO
  cancelling: boolean
  cancelError?: string
  onCancel: (id: string) => void
}

function SubagentTaskRow({ task, cancelling, cancelError, onCancel }: SubagentTaskRowProps) {
  const status = TASK_STATUS[task.status] ?? TASK_STATUS_UNKNOWN
  const startedAt = parseTime(task.startedAt)
  return (
    <li className="rounded-lg border border-line bg-surface px-3 py-2">
      <div className="flex items-start gap-2">
        <span
          aria-hidden="true"
          className={`mt-1.5 size-1.5 shrink-0 rounded-full ${status.dot} ${
            task.status === 'running' ? 'animate-pulse' : ''
          }`}
        />
        <div className="min-w-0 flex-1">
          <div className="flex min-w-0 flex-wrap items-center gap-1.5">
            <span className="min-w-0 flex-1 truncate text-xs text-fg">
              {task.description || '(无描述)'}
            </span>
            <span
              className={`shrink-0 rounded-full border px-1.5 py-0.5 text-[10px] ${status.chip}`}
            >
              {status.label}
            </span>
          </div>
          <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[10px] text-subtle">
            {task.agentType && <span className="font-mono">{task.agentType}</span>}
            {task.background && <span className="rounded bg-raised px-1">后台</span>}
            <span>{task.turns} 轮</span>
            <span className="tabular-nums">
              {formatTokens(task.inputTokens + task.outputTokens)} tokens
            </span>
            {startedAt > 0 && <span>{formatRelative(startedAt)}</span>}
          </div>
          {task.error && (
            <p className="mt-1 whitespace-pre-wrap break-words text-[10px] text-danger-text">
              {task.error}
            </p>
          )}
          {cancelError && (
            <p className="mt-1 whitespace-pre-wrap break-words text-[10px] text-danger-text">
              取消失败：{cancelError}
            </p>
          )}
        </div>
        {task.status === 'running' && (
          <button
            type="button"
            disabled={cancelling}
            onClick={() => onCancel(task.id)}
            className="shrink-0 cursor-pointer rounded-md border border-line bg-raised px-2 py-1 text-[10px] text-muted transition hover:border-danger-line hover:bg-danger-soft hover:text-danger-text disabled:cursor-not-allowed disabled:opacity-50"
          >
            {cancelling ? '取消中…' : '取消'}
          </button>
        )}
      </div>
    </li>
  )
}

function SubAgentsTab() {
  const [types, setTypes] = useState<app.SubagentTypeDTO[]>([])
  const [tasks, setTasks] = useState<app.SubagentTaskDTO[]>([])
  const [loaded, setLoaded] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [cancelling, setCancelling] = useState<ReadonlySet<string>>(new Set())
  const [cancelErrors, setCancelErrors] = useState<Record<string, string>>({})

  const load = useCallback(async (isAlive: () => boolean = () => true) => {
    setBusy(true)
    const [typeResult, taskResult] = await Promise.allSettled([
      ListSubagentTypes(),
      ListSubagentTasks(),
    ])
    if (!isAlive()) return

    const problems: string[] = []
    if (typeResult.status === 'fulfilled') {
      setTypes(typeResult.value)
    } else {
      problems.push(`类型列表：${errorMessage(typeResult.reason)}`)
    }
    if (taskResult.status === 'fulfilled') {
      setTasks(taskResult.value)
    } else {
      problems.push(`任务列表：${errorMessage(taskResult.reason)}`)
    }
    setError(problems.length > 0 ? problems.join('\n') : null)
    setLoaded(true)
    setBusy(false)
  }, [])

  useEffect(() => {
    let alive = true
    void load(() => alive)
    return () => {
      alive = false
    }
  }, [load])

  const cancel = useCallback(async (id: string) => {
    setCancelling((current) => new Set(current).add(id))
    setCancelErrors((current) => {
      if (!(id in current)) return current
      const next = { ...current }
      delete next[id]
      return next
    })
    try {
      await CancelSubagentTask(id)
      // 取消成功后重新拉取任务，让状态与结束时间以服务端为准。
      const list = await ListSubagentTasks()
      setTasks(list)
    } catch (err) {
      setCancelErrors((current) => ({ ...current, [id]: errorMessage(err) }))
    } finally {
      setCancelling((current) => {
        const next = new Set(current)
        next.delete(id)
        return next
      })
    }
  }, [])

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex items-center justify-between gap-3 border-b border-line px-4 py-2.5">
        <p className="text-[11px] text-subtle">
          {types.length} 个类型 · {tasks.length} 个任务
        </p>
        <RefreshButton busy={busy} onClick={() => void load()} />
      </div>
      <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-4 py-3">
        {error && <ErrorNote message={error} onRetry={() => void load()} />}
        {!loaded ? (
          <LoadingRow text="正在读取子 Agent…" />
        ) : (
          <>
            <section>
              <h3 className="text-[10px] font-medium uppercase tracking-wider text-subtle">
                可用类型
              </h3>
              {types.length === 0 ? (
                <EmptyHint
                  title="没有子 Agent 类型"
                  hint="在 ~/.b-desktop/agents 下添加 *.md 定义"
                />
              ) : (
                <ul className="mt-2 space-y-1.5">
                  {types.map((type) => (
                    <li
                      key={type.name}
                      className="rounded-lg border border-line bg-surface px-3 py-2"
                    >
                      <p className="font-mono text-xs font-medium text-fg">{type.name}</p>
                      <p className="mt-0.5 text-[11px] leading-relaxed text-muted">
                        {type.description || '无描述'}
                      </p>
                      {type.path && (
                        <p className="mt-1 break-all font-mono text-[10px] text-subtle">
                          {type.path}
                        </p>
                      )}
                    </li>
                  ))}
                </ul>
              )}
            </section>

            <section>
              <h3 className="flex items-center gap-1.5 text-[10px] font-medium uppercase tracking-wider text-subtle">
                后台任务
                <span className="tabular-nums normal-case">{tasks.length}</span>
              </h3>
              {tasks.length === 0 ? (
                <EmptyHint title="还没有子 Agent 任务" hint="父 Agent 通过 Agent 工具派发任务后会出现在这里" />
              ) : (
                <ul className="mt-2 space-y-1.5">
                  {tasks.map((task) => (
                    <SubagentTaskRow
                      key={task.id}
                      task={task}
                      cancelling={cancelling.has(task.id)}
                      cancelError={cancelErrors[task.id]}
                      onCancel={(id) => void cancel(id)}
                    />
                  ))}
                </ul>
              )}
            </section>
          </>
        )}
      </div>
    </div>
  )
}

// ExtensionsPanel 是扩展管理面板；外部可用 className 控制尺寸、用 onClose 提供关闭入口。
export default function ExtensionsPanel({
  initialTab = 'mcp',
  onClose,
  className = '',
}: ExtensionsPanelProps) {
  const [tab, setTab] = useState<ExtensionsTabId>(initialTab)

  return (
    <section
      data-testid="extensions-panel"
      aria-label="扩展管理"
      className={`flex h-full min-h-0 flex-col overflow-hidden rounded-xl border border-line bg-panel ${className}`}
    >
      <header className="flex items-start justify-between gap-3 border-b border-line px-4 py-3">
        <div>
          <h2 className="text-sm font-semibold text-fg">扩展</h2>
          <p className="mt-0.5 text-[11px] text-subtle">
            管理 MCP 服务器、Skills 与 SubAgent
          </p>
        </div>
        {onClose && (
          <button
            type="button"
            onClick={onClose}
            aria-label="关闭扩展面板"
            className="cursor-pointer rounded-md p-1 text-subtle transition hover:bg-hover hover:text-fg"
          >
            <CloseIcon />
          </button>
        )}
      </header>

      <div
        role="tablist"
        aria-label="扩展类型"
        className="flex gap-1 border-b border-line px-3"
      >
        {TABS.map(({ id, label }) => {
          const active = id === tab
          return (
            <button
              key={id}
              type="button"
              role="tab"
              id={`extensions-tab-${id}`}
              aria-selected={active}
              aria-controls="extensions-tabpanel"
              onClick={() => setTab(id)}
              className={`-mb-px cursor-pointer border-b-2 px-3 py-2 text-xs transition ${
                active
                  ? 'border-accent text-fg'
                  : 'border-transparent text-subtle hover:text-fg'
              }`}
            >
              {label}
            </button>
          )
        })}
      </div>

      <div
        id="extensions-tabpanel"
        role="tabpanel"
        aria-labelledby={`extensions-tab-${tab}`}
        className="flex min-h-0 flex-1 flex-col"
      >
        {tab === 'mcp' && <McpTab />}
        {tab === 'skills' && <SkillsTab />}
        {tab === 'subagents' && <SubAgentsTab />}
      </div>
    </section>
  )
}
