import { useEffect, useState } from 'react'
import { GetProviderConfig, SetProviderConfig } from '../../wailsjs/go/app/App'
import type { app } from '../../wailsjs/go/models'
import type { ThemeMode } from '../lib/theme'
import { useSettingsStore } from '../stores/settings'
import { errorMessage, useWorkbenchStore } from '../stores/workbench'

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

interface ModelForm {
  provider: string
  baseUrl: string
  model: string
  apiKey: string
  contextWindow: string
  maxOutputTokens: string
}

function formFromConfig(config: app.ProviderConfigDTO): ModelForm {
  return {
    provider: config.provider || 'anthropic',
    baseUrl: config.baseUrl ?? '',
    model: config.model ?? '',
    apiKey: '',
    contextWindow: String(config.contextWindow || 0),
    maxOutputTokens: String(config.maxOutputTokens || 0),
  }
}

function ThemePreview({ option }: { option: ThemeOption }) {
  const { shell, panel, line, text } = option.preview
  return (
    <span
      aria-hidden="true"
      className="flex h-14 w-20 shrink-0 flex-col gap-1 rounded-md border p-1.5"
      style={{ background: shell, borderColor: line }}
    >
      <span className="h-2 w-2/3 rounded-sm" style={{ background: text }} />
      <span className="h-2 w-full rounded-sm" style={{ background: panel }} />
      <span className="h-2 w-1/2 rounded-sm" style={{ background: text, opacity: 0.5 }} />
    </span>
  )
}

const fieldClass =
  'w-full rounded-lg border border-line bg-surface px-3 py-2 text-sm text-fg outline-none transition placeholder:text-subtle focus:border-accent disabled:cursor-not-allowed disabled:opacity-60'

// SettingsDialog 是应用设置页：外观 + 模型配置。
export default function SettingsDialog() {
  const open = useSettingsStore((state) => state.open)
  const theme = useSettingsStore((state) => state.theme)
  const setTheme = useSettingsStore((state) => state.setTheme)
  const closeSettings = useSettingsStore((state) => state.closeSettings)

  const [config, setConfig] = useState<app.ProviderConfigDTO | null>(null)
  const [form, setForm] = useState<ModelForm>({
    provider: 'anthropic',
    baseUrl: '',
    model: '',
    apiKey: '',
    contextWindow: '',
    maxOutputTokens: '',
  })
  const [loadingConfig, setLoadingConfig] = useState(false)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)

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
    void GetProviderConfig()
      .then((next) => {
        if (cancelled) return
        setConfig(next)
        setForm(formFromConfig(next))
      })
      .catch((err: unknown) => {
        if (!cancelled) setSaveError(errorMessage(err))
      })
      .finally(() => {
        if (!cancelled) setLoadingConfig(false)
      })
    return () => {
      cancelled = true
    }
  }, [open])

  if (!open) return null

  const contextWindow = Number(form.contextWindow)
  const maxOutputTokens = Number(form.maxOutputTokens)
  const invalid =
    form.provider === '' ||
    form.model.trim() === '' ||
    !Number.isFinite(contextWindow) ||
    contextWindow <= 0 ||
    !Number.isFinite(maxOutputTokens) ||
    maxOutputTokens <= 0

  const update = (patch: Partial<ModelForm>) => {
    setForm((current) => ({ ...current, ...patch }))
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
      })
      const next = await GetProviderConfig()
      setConfig(next)
      setForm(formFromConfig(next))
      useWorkbenchStore.getState().applyProviderConfig(next)
      setSaved(true)
    } catch (err) {
      setSaveError(errorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-overlay p-6"
      onClick={closeSettings}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="settings-title"
        onClick={(event) => event.stopPropagation()}
        className="flex max-h-[86vh] w-full max-w-xl flex-col overflow-hidden rounded-xl border border-line-strong bg-panel shadow-2xl"
      >
        <div className="flex items-start justify-between border-b border-line px-5 py-4">
          <div>
            <h2 id="settings-title" className="text-sm font-semibold text-fg">
              设置
            </h2>
            <p className="mt-1 text-[11px] text-subtle">
              模型配置写入 ~/.anc_dsk/config.json；API Key 不会下发到前端。
            </p>
          </div>
          <button
            type="button"
            onClick={closeSettings}
            aria-label="关闭设置"
            className="cursor-pointer rounded-md p-1 text-subtle transition hover:bg-hover hover:text-fg"
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
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto px-5 py-4">
          <section>
            <h3 className="text-xs font-medium uppercase tracking-wide text-subtle">外观</h3>
            <div className="mt-3 space-y-2">
              {THEME_OPTIONS.map((option) => {
                const selected = option.id === theme
                return (
                  <button
                    key={option.id}
                    type="button"
                    role="radio"
                    aria-checked={selected}
                    onClick={() => setTheme(option.id)}
                    className={`flex w-full cursor-pointer items-center gap-3 rounded-lg border px-3 py-2.5 text-left transition ${
                      selected
                        ? 'border-accent bg-accent-soft'
                        : 'border-line hover:border-line-strong hover:bg-hover'
                    }`}
                  >
                    <ThemePreview option={option} />
                    <span className="min-w-0 flex-1">
                      <span className="flex items-center gap-2 text-sm font-medium text-fg">
                        {option.label}
                        {selected && <span className="text-[10px] text-accent-text">当前</span>}
                      </span>
                      <span className="mt-0.5 block text-[11px] text-subtle">{option.hint}</span>
                    </span>
                    <span
                      aria-hidden="true"
                      className={`grid size-4 shrink-0 place-items-center rounded-full border ${
                        selected ? 'border-accent bg-accent' : 'border-line-strong'
                      }`}
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

          <section className="mt-6 border-t border-line pt-5">
            <div className="flex items-center justify-between">
              <h3 className="text-xs font-medium uppercase tracking-wide text-subtle">模型</h3>
              {config && !config.hasApiKey && (
                <span className="text-[11px] text-warn-text">尚未配置 API Key</span>
              )}
            </div>

            {config?.initError && (
              <p className="mt-3 rounded-md border border-warn-line bg-warn-soft px-3 py-2 text-[11px] text-warn-text">
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

            {saveError && (
              <p
                role="alert"
                className="mt-3 rounded-md border border-danger-line bg-danger-soft px-3 py-2 text-[11px] text-danger-text"
              >
                {saveError}
              </p>
            )}
          </section>
        </div>

        <div className="flex items-center justify-end gap-3 border-t border-line px-5 py-3">
          {saved && <span className="text-[11px] text-success-text">已保存，下一轮生效</span>}
          <button
            type="button"
            data-testid="settings-save"
            onClick={() => void save()}
            disabled={invalid || saving || loadingConfig}
            className="cursor-pointer rounded-lg bg-accent px-4 py-1.5 text-xs font-medium text-on-accent hover:bg-accent-hover disabled:cursor-not-allowed disabled:opacity-50"
          >
            {saving ? '保存中…' : '保存模型配置'}
          </button>
          <button
            type="button"
            onClick={closeSettings}
            className="cursor-pointer rounded-lg border border-line-strong px-4 py-1.5 text-xs text-fg hover:bg-hover"
          >
            完成
          </button>
        </div>
      </div>
    </div>
  )
}
