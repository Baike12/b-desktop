import { useEffect, useState } from 'react'
import {
  AddPermissionRule,
  GetProviderConfig,
  ListModelPresets,
  ListPermissionRules,
  RemovePermissionRule,
  SetProviderConfig,
} from '../../wailsjs/go/app/App'
import type { app } from '../../wailsjs/go/models'
import { CHARACTERS } from '../lib/character'
import { MASCOTS } from '../lib/mascots'
import type { ThemeMode } from '../lib/theme'
import { useSettingsStore } from '../stores/settings'
import { errorMessage, useWorkbenchStore } from '../stores/workbench'
import ModelPresetPicker from './ModelPresetPicker'
import McpSettings from './McpSettings'
import MemorySettings from './MemorySettings'
import SkillSettings from './SkillSettings'
import SubagentSettings from './SubagentSettings'
import HookSettings from './HookSettings'
import UsagePanel from './UsagePanel'

interface ThemeOption {
  id: ThemeMode
  label: string
  hint: string
  // preview 是设置页里的明暗缩略图配色。
  preview: { shell: string; panel: string; line: string; text: string }
}

const THEME_OPTIONS: readonly ThemeOption[] = [
  {
    id: 'light',
    label: '浅色',
    hint: '白天使用，白底深色文字',
    preview: { shell: '#ffffff', panel: '#f1f5f9', line: '#e2e8f0', text: '#0f172a' },
  },
  {
    id: 'dark',
    label: '深色',
    hint: '暗光环境，低亮度背景',
    preview: { shell: '#020617', panel: '#0f172a', line: '#1e293b', text: '#f1f5f9' },
  },
  {
    id: 'system',
    label: '跟随系统',
    hint: '随 macOS 外观自动切换',
    preview: { shell: '#64748b', panel: '#94a3b8', line: '#cbd5e1', text: '#0f172a' },
  },
]

const PROVIDERS = [
  { id: 'anthropic', label: 'Anthropic' },
  { id: 'openai', label: 'OpenAI 兼容' },
] as const

interface NavItem {
  id: 'appearance' | 'model' | 'memory' | 'mcp' | 'skills' | 'subagents' | 'hooks' | 'usage' | 'rules'
  label: string
  description: string
}

interface NavSection {
  title: string
  items: readonly NavItem[]
}

const NAV_SECTIONS: readonly NavSection[] = [
  {
    title: '基础设置',
    items: [
      { id: 'appearance', label: '外观', description: '角色、主题与界面外观' },
      { id: 'model', label: '模型设置', description: '供应商、模型与上下文窗口' },
    ],
  },
  {
    title: 'Agent 能力',
    items: [
      { id: 'mcp', label: 'MCP 服务器', description: '配置 stdio 服务器并加载远端工具' },
      { id: 'skills', label: '技能', description: '管理 SKILL.md 与项目级技能' },
      { id: 'memory', label: '记忆', description: '长期记忆库与轮末自动沉淀' },
      { id: 'subagents', label: '子 Agent', description: '子 Agent 类型与后台任务' },
      { id: 'hooks', label: '钩子', description: '在 Agent 生命周期事件中运行命令' },
    ],
  },
  {
    title: '数据与安全',
    items: [
      { id: 'usage', label: '用量统计', description: '本地模型请求与 token 消耗' },
      { id: 'rules', label: '权限规则', description: '当前会话的工具放行、询问与拒绝规则' },
    ],
  },
]

type TabId = NavItem['id']

const NAV_ITEMS: readonly NavItem[] = NAV_SECTIONS.flatMap((section) => section.items)

type RuleMatchMode = 'exact' | 'prefix' | 'glob' | 'json'
type RuleEffect = 'allow' | 'ask' | 'deny'

const MATCH_MODES: readonly { id: RuleMatchMode; label: string; hint: string }[] = [
  { id: 'exact', label: '精确匹配', hint: '输入与原串完全相等时命中' },
  { id: 'prefix', label: '前缀匹配', hint: '命令以该前缀开头时命中（含 shell 元字符时不判定）' },
  { id: 'glob', label: '路径 Glob', hint: '工作目录内路径全部命中 glob 时生效' },
  { id: 'json', label: 'JSON 子串', hint: '输入 JSON 原文包含该子串时命中' },
]

const EFFECTS: readonly { id: RuleEffect; label: string; hint: string }[] = [
  { id: 'allow', label: '允许', hint: '直接执行，不再询问' },
  { id: 'ask', label: '询问', hint: '交给前端审批，等待用户选择' },
  { id: 'deny', label: '拒绝', hint: '直接拒绝并回灌错误结果' },
]

const EFFECT_BADGE: Record<RuleEffect, string> = {
  allow: 'rounded-full border-success-line bg-success-soft text-success-text',
  ask: 'rounded-full border-warn-line bg-warn-soft text-warn-text',
  deny: 'rounded-full border-danger-line bg-danger-soft text-danger-text',
}

const SOURCE_LABELS: Record<string, string> = {
  builtin: '内置',
  user: '用户',
  project: '项目',
  session: '会话',
}

const PATTERN_PLACEHOLDER: Record<RuleMatchMode, string> = {
  exact: '例如 npm test',
  prefix: '例如 git status',
  glob: '例如 src/**/*.ts',
  json: '例如 "path":"src/',
}

interface ModelForm {
  provider: string
  baseUrl: string
  model: string
  apiKey: string
  contextWindow: string
  maxOutputTokens: string
  thinkingEnabled: boolean
  thinkingBudget: string
}

interface RuleFormState {
  tool: string
  pattern: string
  matchMode: RuleMatchMode
  effect: RuleEffect
}

const EMPTY_RULE_FORM: RuleFormState = {
  tool: '',
  pattern: '',
  matchMode: 'exact',
  effect: 'ask',
}

function formFromConfig(config: app.ProviderConfigDTO): ModelForm {
  return {
    provider: config.provider || 'anthropic',
    baseUrl: config.baseUrl ?? '',
    model: config.model ?? '',
    apiKey: '',
    contextWindow: String(config.contextWindow || 0),
    maxOutputTokens: String(config.maxOutputTokens || 0),
    thinkingEnabled: Boolean(config.thinkingEnabled),
    // 预算为 0 表示后端使用默认值，输入框留空并由 placeholder 提示。
    thinkingBudget: config.thinkingBudget ? String(config.thinkingBudget) : '',
  }
}

function matchModeLabel(mode: string): string {
  return MATCH_MODES.find((item) => item.id === mode)?.label ?? mode
}

function effectLabel(effect: string): string {
  return EFFECTS.find((item) => item.id === effect)?.label ?? effect
}

function effectBadge(effect: string): string {
  return EFFECT_BADGE[effect as RuleEffect] ?? 'rounded-full border-line bg-raised text-muted'
}

function sourceLabel(source: string): string {
  return SOURCE_LABELS[source] ?? source
}

function ThemePreview({ option }: { option: ThemeOption }) {
  const { shell, panel, line, text } = option.preview
  return (
    <span
      aria-hidden="true"
      className="flex h-14 w-20 shrink-0 flex-col gap-1 rounded-lg border p-1.5"
      style={{ background: shell, borderColor: line }}
    >
      <span className="h-2 w-2/3 rounded-xs" style={{ background: text }} />
      <span className="h-2 w-full rounded-xs" style={{ background: panel }} />
      <span className="h-2 w-1/2 rounded-xs" style={{ background: text, opacity: 0.5 }} />
    </span>
  )
}

const fieldClass =
  'w-full rounded-lg border border-line bg-surface px-3 py-2 text-[13px] text-fg outline-none transition placeholder:text-subtle focus:border-accent disabled:cursor-not-allowed disabled:opacity-60'

// MIN_THINKING_BUDGET 是扩展思考预算的协议下限，与后端 llm.MinThinkingBudget 一致。
const MIN_THINKING_BUDGET = 1024

// NavIcon 是设置侧边栏的分类图标，统一 16px 描边风格。
function NavIcon({ id }: { id: TabId }) {
  const common = {
    viewBox: '0 0 24 24',
    fill: 'none',
    stroke: 'currentColor',
    strokeWidth: 1.7,
    strokeLinecap: 'round',
    strokeLinejoin: 'round',
    className: 'size-4 shrink-0',
    'aria-hidden': true,
  } as const
  switch (id) {
    case 'appearance':
      return (
        <svg {...common}>
          <circle cx="12" cy="12" r="4" />
          <path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" />
        </svg>
      )
    case 'model':
      return (
        <svg {...common}>
          <rect x="4" y="4" width="16" height="16" rx="2" />
          <rect x="9" y="9" width="6" height="6" rx="1" />
          <path d="M9 2v2M15 2v2M9 20v2M15 20v2M2 9h2M2 15h2M20 9h2M20 15h2" />
        </svg>
      )
    case 'mcp':
      return (
        <svg {...common}>
          <path d="M9 7V3M15 7V3M7 7h10v4a5 5 0 0 1-10 0V7ZM12 16v5" />
        </svg>
      )
    case 'skills':
      return (
        <svg {...common}>
          <path d="m12 3 2.2 4.5L19 8.2l-3.5 3.4.8 4.8L12 14.2 7.7 16.4l.8-4.8L5 8.2l4.8-.7L12 3Z" />
        </svg>
      )
    case 'hooks':
      return (
        <svg {...common}>
          <path d="M10 14a5 5 0 0 1 0-7l1-1a5 5 0 0 1 7 7l-1 1M14 10a5 5 0 0 1 0 7l-1 1a5 5 0 0 1-7-7l1-1" />
        </svg>
      )
    case 'subagents':
      return (
        <svg {...common}>
          <circle cx="9" cy="8" r="3" />
          <path d="M3 20a6 6 0 0 1 12 0" />
          <path d="M16 5a3 3 0 0 1 0 6M18 14a5 5 0 0 1 4 5" />
        </svg>
      )
    case 'usage':
      return (
        <svg {...common}>
          <path d="M4 20V4M4 20h16" />
          <path d="M8 16v-4M12 16V8M16 16v-6M20 16v-9" />
        </svg>
      )
    case 'rules':
      return (
        <svg {...common}>
          <path d="M12 3 4.5 6v5.5c0 4.6 3.2 7.6 7.5 9.5 4.3-1.9 7.5-4.9 7.5-9.5V6L12 3Z" />
          <path d="m9 12 2 2 4-4" />
        </svg>
      )
    default:
      return null
  }
}

// SettingsDialog 是应用设置页：外观、模型、扩展能力、用量与权限规则。
export default function SettingsDialog() {
  const open = useSettingsStore((state) => state.open)
  const character = useSettingsStore((state) => state.character)
  const setCharacter = useSettingsStore((state) => state.setCharacter)
  const theme = useSettingsStore((state) => state.theme)
  const setTheme = useSettingsStore((state) => state.setTheme)
  const closeSettings = useSettingsStore((state) => state.closeSettings)
  const sessionId = useWorkbenchStore((state) => state.activeId)

  const [tab, setTab] = useState<TabId>('appearance')

  const [config, setConfig] = useState<app.ProviderConfigDTO | null>(null)
  const [form, setForm] = useState<ModelForm>({
    provider: 'anthropic',
    baseUrl: '',
    model: '',
    apiKey: '',
    contextWindow: '',
    maxOutputTokens: '',
    thinkingEnabled: false,
    thinkingBudget: '',
  })
  const [loadingConfig, setLoadingConfig] = useState(false)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)

  const [presets, setPresets] = useState<app.ModelPresetDTO[]>([])
  const [presetsLoading, setPresetsLoading] = useState(false)
  const [presetsError, setPresetsError] = useState<string | null>(null)
  const [presetsLoaded, setPresetsLoaded] = useState(false)
  const [presetsAttempt, setPresetsAttempt] = useState(0)
  const [selectedPresetId, setSelectedPresetId] = useState<string | null>(null)

  const [rules, setRules] = useState<app.RuleDTO[]>([])
  const [rulesLoading, setRulesLoading] = useState(false)
  const [rulesError, setRulesError] = useState<string | null>(null)
  const [ruleForm, setRuleForm] = useState<RuleFormState>(EMPTY_RULE_FORM)
  const [adding, setAdding] = useState(false)
  const [removingId, setRemovingId] = useState<string | null>(null)

  useEffect(() => {
    if (!open) return
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') closeSettings()
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [open, closeSettings])

  // 每次打开都重新读取后端配置；API Key 只返回掩码。
  useEffect(() => {
    if (!open) return
    let cancelled = false
    setLoadingConfig(true)
    setSaveError(null)
    setSaved(false)
    void (async () => {
      try {
        const next = await GetProviderConfig()
        if (cancelled) return
        setConfig(next)
        setForm(formFromConfig(next))
      } catch (err) {
        if (!cancelled) setSaveError(errorMessage(err))
      } finally {
        if (!cancelled) setLoadingConfig(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [open])

  // 预设是静态数据，首次切到模型页时读取一次，失败后由重试按钮重跑。
  useEffect(() => {
    if (!open || tab !== 'model' || presetsLoaded) return
    let cancelled = false
    setPresetsLoading(true)
    setPresetsError(null)
    void (async () => {
      try {
        const next = await ListModelPresets()
        if (cancelled) return
        setPresets(next)
        setPresetsLoaded(true)
      } catch (err) {
        if (!cancelled) setPresetsError(errorMessage(err))
      } finally {
        if (!cancelled) setPresetsLoading(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [open, tab, presetsLoaded, presetsAttempt])

  // 规则属于会话：没有会话时清空列表，切换会话后重新读取。
  useEffect(() => {
    if (!open || tab !== 'rules' || !sessionId) {
      setRules([])
      setRulesError(null)
      return
    }
    let cancelled = false
    setRulesLoading(true)
    setRulesError(null)
    void (async () => {
      try {
        const next = await ListPermissionRules(sessionId)
        if (!cancelled) setRules(next)
      } catch (err) {
        if (!cancelled) setRulesError(errorMessage(err))
      } finally {
        if (!cancelled) setRulesLoading(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [open, tab, sessionId])

  if (!open) return null

  const contextWindow = Number(form.contextWindow)
  const maxOutputTokens = Number(form.maxOutputTokens)
  const thinkingBudget = Number(form.thinkingBudget)
  const invalid =
    form.provider === '' ||
    form.model.trim() === '' ||
    !Number.isFinite(contextWindow) ||
    contextWindow <= 0 ||
    !Number.isFinite(maxOutputTokens) ||
    maxOutputTokens <= 0 ||
    (form.thinkingEnabled &&
      (!Number.isFinite(thinkingBudget) || thinkingBudget < MIN_THINKING_BUDGET))

  const update = (patch: Partial<ModelForm>) => {
    // 手动改供应商或地址就不再属于所选预设。
    if (patch.provider !== undefined || patch.baseUrl !== undefined) {
      setSelectedPresetId(null)
    }
    setForm((current) => ({ ...current, ...patch }))
    setSaved(false)
  }

  const applyPreset = (preset: app.ModelPresetDTO) => {
    setForm((current) => ({
      ...current,
      provider: preset.provider,
      baseUrl: preset.baseUrl,
      model: preset.defaultModel,
    }))
    setSelectedPresetId(preset.id)
    setSaved(false)
  }

  const save = async () => {
    if (invalid || saving) return
    setSaving(true)
    setSaveError(null)
    setSaved(false)
    try {
      await SetProviderConfig({
        provider: form.provider,
        baseUrl: form.baseUrl.trim(),
        model: form.model.trim(),
        apiKey: form.apiKey.trim() || undefined,
        contextWindow,
        maxOutputTokens,
        thinkingEnabled: form.thinkingEnabled,
        thinkingBudget: form.thinkingEnabled ? thinkingBudget : undefined,
      })
      const next = await GetProviderConfig()
      setConfig(next)
      setForm(formFromConfig(next))
      // 保存后按规范化后的值重新匹配预设，避免高亮与落盘配置不一致。
      const matched = presets.find(
        (preset) =>
          preset.provider === next.provider &&
          preset.baseUrl === next.baseUrl &&
          preset.defaultModel === next.model,
      )
      setSelectedPresetId(matched?.id ?? null)
      useWorkbenchStore.getState().applyProviderConfig(next)
      setSaved(true)
    } catch (err) {
      setSaveError(errorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  const addRule = async () => {
    if (!sessionId || adding) return
    const tool = ruleForm.tool.trim()
    const pattern = ruleForm.pattern.trim()
    if (tool === '') return
    if (ruleForm.matchMode === 'glob' && pattern === '') return
    setAdding(true)
    setRulesError(null)
    try {
      const created = await AddPermissionRule(sessionId, {
        tool,
        pattern: pattern || undefined,
        matchMode: ruleForm.matchMode,
        effect: ruleForm.effect,
      })
      setRules((current) => [...current, created])
      setRuleForm(EMPTY_RULE_FORM)
    } catch (err) {
      setRulesError(errorMessage(err))
    } finally {
      setAdding(false)
    }
  }

  const removeRule = async (ruleId: string) => {
    if (!sessionId || removingId) return
    setRemovingId(ruleId)
    setRulesError(null)
    try {
      await RemovePermissionRule(sessionId, ruleId)
      setRules((current) => current.filter((rule) => rule.id !== ruleId))
    } catch (err) {
      setRulesError(errorMessage(err))
    } finally {
      setRemovingId(null)
    }
  }

  const selectedPreset = presets.find((preset) => preset.id === selectedPresetId)

  const ruleValid =
    ruleForm.tool.trim() !== '' &&
    (ruleForm.matchMode !== 'glob' || ruleForm.pattern.trim() !== '')

  const activeItem = NAV_ITEMS.find((item) => item.id === tab) ?? NAV_ITEMS[0]

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-overlay p-4 backdrop-blur-[2px] sm:p-6">
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="settings-title"
        data-testid="settings-dialog"
        className="flex h-[min(780px,100%)] w-[min(1100px,100%)] overflow-hidden rounded-2xl border border-line-strong bg-panel shadow-pop"
      >
        <aside className="flex w-60 shrink-0 flex-col border-r border-line/70 bg-raised/50">
          <div className="px-3 py-3">
            <button
              type="button"
              data-testid="settings-back"
              onClick={closeSettings}
              className="flex w-full cursor-pointer items-center gap-1.5 rounded-md px-2 py-1.5 text-[12px] text-muted transition hover:bg-hover hover:text-fg"
            >
              <svg
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth="2"
                strokeLinecap="round"
                strokeLinejoin="round"
                className="size-3.5 shrink-0"
                aria-hidden="true"
              >
                <path d="M15 18l-6-6 6-6" />
              </svg>
              返回工作区
            </button>
          </div>

          <nav className="min-h-0 flex-1 overflow-y-auto px-2 pb-4">
            {NAV_SECTIONS.map((section) => (
              <div key={section.title} className="mt-4 first:mt-0">
                <p className="px-2 pb-1 font-mono text-[10px] uppercase tracking-[0.16em] text-subtle">
                  {section.title}
                </p>
                <div role="tablist" aria-label={section.title} className="space-y-0.5">
                  {section.items.map((item) => {
                    const active = item.id === tab
                    return (
                      <button
                        key={item.id}
                        type="button"
                        role="tab"
                        aria-selected={active}
                        data-testid={`settings-tab-${item.id}`}
                        onClick={() => setTab(item.id)}
                        className={`flex w-full cursor-pointer items-center gap-2 rounded-md py-1.5 pl-2.5 pr-2 text-left text-[12.5px] transition ${ active ? 'bg-surface font-medium text-fg shadow-card ring-1 ring-accent/35' : 'text-muted hover:bg-hover hover:text-fg' }`}
                      >
                        <NavIcon id={item.id} />
                        <span className="truncate">{item.label}</span>
                      </button>
                    )
                  })}
                </div>
              </div>
            ))}
          </nav>
        </aside>

        <div className="flex min-w-0 flex-1 flex-col">
          <header className="flex items-start justify-between border-b border-line/70 px-8 py-5">
            <div>
              <h2 id="settings-title" className="text-[17px] font-semibold tracking-[-0.02em] text-fg">
                {activeItem.label}
              </h2>
              <p className="mt-1 text-[11px] text-subtle">{activeItem.description}</p>
            </div>
            <button
              type="button"
              onClick={closeSettings}
              aria-label="关闭设置"
              className="cursor-pointer rounded-md p-1.5 text-subtle transition hover:bg-hover hover:text-fg"
            >
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
            </button>
          </header>

          <div className="min-h-0 flex-1 overflow-y-auto px-8 py-6">
          <div className="mx-auto w-full max-w-4xl">
          {tab === 'appearance' && (
            <section role="tabpanel" aria-label="外观" data-testid="settings-appearance">
              <p className="mb-2 font-mono text-[10px] uppercase tracking-[0.16em] text-subtle">
                角色
              </p>
              <div className="grid gap-2 sm:grid-cols-2">
                {CHARACTERS.map((item) => {
                  const selected = item.id === character
                  return (
                    <button
                      key={item.id}
                      type="button"
                      role="radio"
                      aria-checked={selected}
                      data-testid={`settings-character-${item.id}`}
                      onClick={() => setCharacter(item.id)}
                      className={`flex w-full cursor-pointer items-center gap-3 rounded-xl border px-3.5 py-3 text-left transition ${ selected ? 'border-accent bg-accent-soft shadow-card' : 'border-line hover:border-line-strong hover:bg-hover' }`}
                    >
                      <span className="grid size-12 shrink-0 place-items-center rounded-lg border border-line bg-surface">
                        <img
                          src={MASCOTS[item.id].avatar}
                          alt=""
                          draggable={false}
                          className="size-11 select-none object-contain"
                        />
                      </span>
                      <span className="min-w-0 flex-1">
                        <span className="flex items-center gap-2 text-[13px] font-medium text-fg">
                          {item.name}
                          {selected && (
                            <span className="font-mono text-[10px] uppercase tracking-[0.14em] text-accent-text">
                              当前
                            </span>
                          )}
                        </span>
                        <span className="mt-0.5 block text-[11px] text-subtle">{item.hint}</span>
                      </span>
                      <span
                        aria-hidden="true"
                        className={`grid size-4 shrink-0 place-items-center rounded-full border ${ selected ? 'border-accent bg-accent' : 'border-line-strong' }`}
                      >
                        {selected && (
                          <svg
                            viewBox="0 0 24 24"
                            fill="none"
                            stroke="currentColor"
                            strokeWidth="3"
                            strokeLinecap="round"
                            strokeLinejoin="round"
                            className="size-2.5 text-on-accent"
                          >
                            <path d="m5 13 4 4L19 7" />
                          </svg>
                        )}
                      </span>
                    </button>
                  )
                })}
              </div>

              <div className="mb-2 mt-6 flex items-center justify-between">
                <p className="font-mono text-[10px] uppercase tracking-[0.16em] text-subtle">
                  主题
                </p>
                <p className="text-[11px] text-subtle">角色与主题切换后立即生效</p>
              </div>
              <div className="space-y-2">
                {THEME_OPTIONS.map((option) => {
                  const selected = option.id === theme
                  return (
                    <button
                      key={option.id}
                      type="button"
                      role="radio"
                      aria-checked={selected}
                      data-testid={`settings-theme-${option.id}`}
                      onClick={() => setTheme(option.id)}
                      className={`flex w-full cursor-pointer items-center gap-3 rounded-xl border px-3.5 py-3 text-left transition ${ selected ? 'border-accent bg-accent-soft shadow-card' : 'border-line hover:border-line-strong hover:bg-hover' }`}
                    >
                      <ThemePreview option={option} />
                      <span className="min-w-0 flex-1">
                        <span className="flex items-center gap-2 text-[13px] font-medium text-fg">
                          {option.label}
                          {selected && (
                            <span className="font-mono text-[10px] uppercase tracking-[0.14em] text-accent-text">
                              当前
                            </span>
                          )}
                        </span>
                        <span className="mt-0.5 block text-[11px] text-subtle">{option.hint}</span>
                      </span>
                      <span
                        aria-hidden="true"
                        className={`grid size-4 shrink-0 place-items-center rounded-full border ${ selected ? 'border-accent bg-accent' : 'border-line-strong' }`}
                      >
                        {selected && (
                          <svg
                            viewBox="0 0 24 24"
                            fill="none"
                            stroke="currentColor"
                            strokeWidth="3"
                            strokeLinecap="round"
                            strokeLinejoin="round"
                            className="size-2.5 text-on-accent"
                          >
                            <path d="m5 13 4 4L19 7" />
                          </svg>
                        )}
                      </span>
                    </button>
                  )
                })}
              </div>
            </section>
          )}

          {tab === 'model' && (
            <section role="tabpanel" aria-label="模型" data-testid="settings-model-panel">
              {config && !config.hasApiKey && (
                <p className="text-right text-[11px] text-warn-text">尚未配置 API Key</p>
              )}

              <div className="mt-3">
                <p className="mb-2 text-[11px] text-subtle">
                  这里只显示当前生效的模型配置；修改下方字段后保存，下一轮生效。
                </p>
                <ModelPresetPicker
                  presets={presets}
                  selectedId={selectedPresetId}
                  loading={presetsLoading}
                  disabled={loadingConfig || saving}
                  onSelect={applyPreset}
                />
                {presetsError && (
                  <div className="mt-2 flex items-center gap-2">
                    <span role="alert" className="text-[11px] text-danger-text">
                      {presetsError}
                    </span>
                    <button
                      type="button"
                      data-testid="model-presets-retry"
                      onClick={() => {
                        setPresetsError(null)
                        setPresetsLoaded(false)
                        setPresetsAttempt((value) => value + 1)
                      }}
                      className="cursor-pointer text-[11px] text-accent-text underline underline-offset-2"
                    >
                      重试
                    </button>
                  </div>
                )}
                {selectedPreset?.local && (
                  <p className="mt-2 text-[11px] text-muted">
                    本地端点（{selectedPreset.baseUrl}）无需 API Key。
                  </p>
                )}
                {selectedPreset && selectedPreset.needsKey && !config?.hasApiKey && (
                  <p className="mt-2 text-[11px] text-warn-text">
                    {selectedPreset.name} 需要 API Key，请在下方填入。
                  </p>
                )}
              </div>

              {config?.initError && (
                <p className="mt-3 rounded-lg border border-warn-line bg-warn-soft px-3 py-2 text-[11px] text-warn-text">
                  {config.initError}
                </p>
              )}

              <div className="mt-3 grid grid-cols-2 gap-3">
                <label className="block">
                  <span className="mb-1 block text-[11px] text-muted">供应商</span>
                  <select
                    data-testid="settings-provider"
                    value={form.provider}
                    disabled={loadingConfig || saving}
                    onChange={(event) => update({ provider: event.target.value })}
                    className={`${fieldClass} cursor-pointer`}
                  >
                    {PROVIDERS.map((provider) => (
                      <option key={provider.id} value={provider.id}>
                        {provider.label}
                      </option>
                    ))}
                  </select>
                </label>
                <label className="block">
                  <span className="mb-1 block text-[11px] text-muted">模型名称</span>
                  <input
                    data-testid="settings-model"
                    type="text"
                    value={form.model}
                    disabled={loadingConfig || saving}
                    onChange={(event) => update({ model: event.target.value })}
                    placeholder="例如 deepseek-chat"
                    className={fieldClass}
                  />
                </label>
              </div>

              <label className="mt-3 block">
                <span className="mb-1 block text-[11px] text-muted">Base URL</span>
                <input
                  data-testid="settings-base-url"
                  type="text"
                  value={form.baseUrl}
                  disabled={loadingConfig || saving}
                  onChange={(event) => update({ baseUrl: event.target.value })}
                  placeholder="留空使用供应商默认地址"
                  className={fieldClass}
                />
              </label>

              <label className="mt-3 block">
                <span className="mb-1 block text-[11px] text-muted">API Key</span>
                <input
                  data-testid="settings-api-key"
                  type="password"
                  autoComplete="off"
                  value={form.apiKey}
                  disabled={loadingConfig || saving}
                  onChange={(event) => update({ apiKey: event.target.value })}
                  placeholder={
                    config?.hasApiKey
                      ? `已配置 ${config.apiKeyMasked ?? ''}，留空保持不变`
                      : '输入 API Key'
                  }
                  className={fieldClass}
                />
              </label>

              <div className="mt-3 grid grid-cols-2 gap-3">
                <label className="block">
                  <span className="mb-1 block text-[11px] text-muted">上下文长度（token）</span>
                  <input
                    data-testid="settings-context-window"
                    type="number"
                    min={1}
                    step={1000}
                    value={form.contextWindow}
                    disabled={loadingConfig || saving}
                    onChange={(event) => update({ contextWindow: event.target.value })}
                    className={fieldClass}
                  />
                </label>
                <label className="block">
                  <span className="mb-1 block text-[11px] text-muted">单次最大输出（token）</span>
                  <input
                    data-testid="settings-max-output"
                    type="number"
                    min={1}
                    step={256}
                    value={form.maxOutputTokens}
                    disabled={loadingConfig || saving}
                    onChange={(event) => update({ maxOutputTokens: event.target.value })}
                    className={fieldClass}
                  />
                </label>
              </div>

              <p className="mt-2 text-[11px] text-subtle">
                上下文长度用于压缩阈值与用量显示；单次最大输出是每轮请求的 max_tokens。
              </p>

              <div className="mt-4 border-t border-line pt-4">
                <label className="flex items-center gap-2">
                  <input
                    data-testid="settings-thinking-enabled"
                    type="checkbox"
                    checked={form.thinkingEnabled}
                    disabled={loadingConfig || saving}
                    onChange={(event) => update({ thinkingEnabled: event.target.checked })}
                    className="size-3.5 accent-[var(--accent)]"
                  />
                  <span className="text-[11px] text-muted">启用扩展思考</span>
                </label>
                <label className="mt-3 block">
                  <span className="mb-1 block text-[11px] text-muted">思考预算（token）</span>
                  <input
                    data-testid="settings-thinking-budget"
                    type="number"
                    min={MIN_THINKING_BUDGET}
                    step={1024}
                    value={form.thinkingBudget}
                    disabled={loadingConfig || saving || !form.thinkingEnabled}
                    onChange={(event) => update({ thinkingBudget: event.target.value })}
                    placeholder="8192"
                    className={fieldClass}
                  />
                </label>
                <p className="mt-2 text-[11px] text-subtle">
                  扩展思考让模型先推理再回答，思考过程会在回复中实时显示。预算须不小于{' '}
                  {MIN_THINKING_BUDGET}，且小于单次最大输出。
                </p>
              </div>

              {saveError && (
                <p
                  role="alert"
                  className="mt-3 rounded-lg border border-danger-line bg-danger-soft px-3 py-2 text-[11px] text-danger-text"
                >
                  {saveError}
                </p>
              )}
            </section>
          )}

          {tab === 'rules' && (
            <section role="tabpanel" aria-label="权限规则" data-testid="settings-rules-panel">
              <div className="flex items-center justify-end gap-3">
                {sessionId && (
                  <span className="truncate font-mono text-[10px] text-subtle" title={sessionId}>
                    会话 {sessionId}
                  </span>
                )}
              </div>

              {!sessionId ? (
                <p className="mt-3 rounded-lg border border-line bg-raised px-3 py-2 text-[11px] text-muted">
                  先新建或选择一个会话，规则按会话生效。
                </p>
              ) : (
                <>
                  <p className="mt-1 text-[11px] text-subtle">
                    新增规则立即对后续工具调用生效；未填匹配模式串时表示该工具的全部调用（Glob
                    除外）。
                  </p>

                  <div className="mt-3 rounded-xl border border-line/80 bg-raised/60 p-3.5">
                    <div className="grid grid-cols-2 gap-3">
                      <label className="block">
                        <span className="mb-1 block text-[11px] text-muted">工具名</span>
                        <input
                          data-testid="settings-rule-tool"
                          type="text"
                          value={ruleForm.tool}
                          disabled={adding}
                          onChange={(event) =>
                            setRuleForm((current) => ({ ...current, tool: event.target.value }))
                          }
                          placeholder='例如 Bash 或 *'
                          className={fieldClass}
                        />
                      </label>
                      <label className="block">
                        <span className="mb-1 block text-[11px] text-muted">匹配模式串</span>
                        <input
                          data-testid="settings-rule-pattern"
                          type="text"
                          value={ruleForm.pattern}
                          disabled={adding}
                          onChange={(event) =>
                            setRuleForm((current) => ({ ...current, pattern: event.target.value }))
                          }
                          placeholder={PATTERN_PLACEHOLDER[ruleForm.matchMode]}
                          className={`${fieldClass} font-mono text-xs`}
                        />
                      </label>
                      <label className="block">
                        <span className="mb-1 block text-[11px] text-muted">匹配方式</span>
                        <select
                          data-testid="settings-rule-match-mode"
                          value={ruleForm.matchMode}
                          disabled={adding}
                          onChange={(event) =>
                            setRuleForm((current) => ({
                              ...current,
                              matchMode: event.target.value as RuleMatchMode,
                            }))
                          }
                          className={`${fieldClass} cursor-pointer`}
                        >
                          {MATCH_MODES.map((mode) => (
                            <option key={mode.id} value={mode.id}>
                              {mode.label}
                            </option>
                          ))}
                        </select>
                      </label>
                      <label className="block">
                        <span className="mb-1 block text-[11px] text-muted">效果</span>
                        <select
                          data-testid="settings-rule-effect"
                          value={ruleForm.effect}
                          disabled={adding}
                          onChange={(event) =>
                            setRuleForm((current) => ({
                              ...current,
                              effect: event.target.value as RuleEffect,
                            }))
                          }
                          className={`${fieldClass} cursor-pointer`}
                        >
                          {EFFECTS.map((effect) => (
                            <option key={effect.id} value={effect.id}>
                              {effect.label}
                            </option>
                          ))}
                        </select>
                      </label>
                    </div>
                    <div className="mt-2 flex items-center justify-between gap-3">
                      <p className="text-[11px] text-subtle">
                        {MATCH_MODES.find((mode) => mode.id === ruleForm.matchMode)?.hint}
                        {' · '}
                        {EFFECTS.find((effect) => effect.id === ruleForm.effect)?.hint}
                      </p>
                      <button
                        type="button"
                        data-testid="settings-rule-add"
                        onClick={() => void addRule()}
                        disabled={!ruleValid || adding}
                        className="shrink-0 cursor-pointer rounded-full bg-accent px-3.5 py-1.5 text-xs font-medium text-on-accent shadow-card transition hover:bg-accent-hover active:translate-y-px disabled:cursor-not-allowed disabled:opacity-50 disabled:shadow-none"
                      >
                        {adding ? '添加中…' : '添加规则'}
                      </button>
                    </div>
                  </div>

                  {rulesError && (
                    <p
                      role="alert"
                  className="mt-3 rounded-lg border border-danger-line bg-danger-soft px-3 py-2 text-[11px] text-danger-text"
                    >
                      {rulesError}
                    </p>
                  )}

                  <div className="mt-3">
                    <div className="flex items-center justify-between">
                      <span className="text-[11px] text-muted">当前生效规则</span>
                      <span className="text-[11px] text-subtle">{rules.length} 条</span>
                    </div>
                    {rulesLoading ? (
                      <p className="mt-2 text-[11px] text-subtle">正在读取规则…</p>
                    ) : rules.length === 0 ? (
                      <p
                        data-testid="settings-rules-empty"
                        className="mt-2 rounded-lg border border-line px-3 py-2 text-[11px] text-subtle"
                      >
                        该会话暂无规则。
                      </p>
                    ) : (
                      <ul data-testid="settings-rules-list" className="mt-2 space-y-1.5">
                        {rules.map((rule) => (
                          <li
                            key={rule.id}
                            data-testid={`settings-rule-${rule.id}`}
                            className="flex items-center gap-3 rounded-lg border border-line bg-surface/60 px-3 py-2"
                          >
                            <div className="min-w-0 flex-1">
                              <div className="flex items-center gap-2">
                                <span className="truncate font-mono text-xs text-fg">
                                  {rule.tool}
                                </span>
                                <span
                                  className={`shrink-0 border px-1.5 py-0.5 text-[10px] ${effectBadge( rule.effect, )}`}
                                >
                                  {effectLabel(rule.effect)}
                                </span>
                                <span className="shrink-0 text-[10px] text-subtle">
                                  {matchModeLabel(rule.matchMode)} · {sourceLabel(rule.source)}
                                </span>
                              </div>
                              {rule.pattern && (
                                <p className="mt-0.5 truncate font-mono text-[11px] text-muted">
                                  {rule.pattern}
                                </p>
                              )}
                              {rule.note && (
                                <p className="mt-0.5 truncate text-[10px] text-subtle">
                                  {rule.note}
                                </p>
                              )}
                            </div>
                            <button
                              type="button"
                              data-testid={`settings-rule-remove-${rule.id}`}
                              aria-label={`删除规则 ${rule.tool}`}
                              disabled={removingId !== null}
                              onClick={() => void removeRule(rule.id)}
                              className="shrink-0 cursor-pointer rounded-md border border-line-strong px-2.5 py-1 text-[11px] text-muted transition hover:border-danger-line hover:bg-danger-soft hover:text-danger-text disabled:cursor-not-allowed disabled:opacity-50"
                            >
                              {removingId === rule.id ? '删除中…' : '删除'}
                            </button>
                          </li>
                        ))}
                      </ul>
                    )}
                  </div>
                </>
              )}
            </section>
          )}

          {tab === 'mcp' && <McpSettings />}
          {tab === 'skills' && <SkillSettings />}
          {tab === 'memory' && <MemorySettings />}
          {tab === 'subagents' && <SubagentSettings />}
          {tab === 'hooks' && <HookSettings />}
          {tab === 'usage' && <UsagePanel showHeader={false} />}
          </div>
        </div>

          {tab === 'model' && (
            <div className="flex items-center justify-end gap-3 border-t border-line/70 px-8 py-4">
              {saved && <span className="text-[11px] text-success-text">已保存，下一轮生效</span>}
              <button
                type="button"
                data-testid="settings-save"
                onClick={() => void save()}
                disabled={invalid || saving || loadingConfig}
                className="cursor-pointer rounded-full bg-accent px-4 py-1.5 text-xs font-medium text-on-accent shadow-card transition hover:bg-accent-hover active:translate-y-px disabled:cursor-not-allowed disabled:opacity-50 disabled:shadow-none"
              >
                {saving ? '保存中…' : '保存模型配置'}
              </button>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
