import type { app } from '../../wailsjs/go/models'

// providerLabel 把协议名转成中文展示文本。
export function providerLabel(provider: string): string {
  if (provider === 'anthropic') return 'Anthropic 协议'
  if (provider === 'openai') return 'OpenAI 兼容'
  return provider
}

interface ModelPresetPickerProps {
  presets: readonly app.ModelPresetDTO[]
  // selectedId 是当前选中的预设；为空时不高亮任何一项。
  selectedId: string | null
  loading?: boolean
  disabled?: boolean
  onSelect: (preset: app.ModelPresetDTO) => void
}

// ModelPresetPicker 展示当前生效的模型配置。项目不内置预设清单，
// 列表里最多只有当前这一条；点击它会把配置回填到表单。
export default function ModelPresetPicker({
  presets,
  selectedId,
  loading = false,
  disabled = false,
  onSelect,
}: ModelPresetPickerProps) {
  if (loading) {
    return (
      <p data-testid="model-presets-loading" className="text-[11px] text-subtle">
        正在读取预设…
      </p>
    )
  }

  if (presets.length === 0) {
    return (
      <p data-testid="model-presets-empty" className="text-[11px] text-subtle">
        当前还没有模型配置，可直接填写下面的字段。
      </p>
    )
  }

  return (
    <div
      data-testid="model-presets"
      role="radiogroup"
      aria-label="当前模型配置"
      className="grid gap-2"
    >
      {presets.map((preset) => {
        const selected = preset.id === selectedId
        return (
          <button
            key={preset.id}
            type="button"
            role="radio"
            aria-checked={selected}
            data-testid={`model-preset-${preset.id}`}
            disabled={disabled}
            onClick={() => onSelect(preset)}
            className={`flex cursor-pointer flex-col gap-1 border px-3 py-2 text-left transition disabled:cursor-not-allowed disabled:opacity-60 ${ selected ? 'border-accent bg-accent-soft' : 'border-line hover:border-line-strong hover:bg-hover' }`}
          >
            <span className="flex items-center gap-1.5">
              <span className="truncate text-xs font-medium text-fg">{preset.name}</span>
              {preset.local && (
                <span className="shrink-0 border border-line bg-raised px-1 text-[10px] text-muted">
                  本地
                </span>
              )}
              {!preset.needsKey && !preset.local && (
                <span className="shrink-0 border border-line bg-raised px-1 text-[10px] text-muted">
                  免密钥
                </span>
              )}
              {selected && <span className="ml-auto shrink-0 font-mono text-[10px] uppercase tracking-[0.14em] text-accent-text">已选</span>}
            </span>
            <span className="truncate font-mono text-[11px] text-muted">{preset.defaultModel}</span>
            <span className="truncate text-[10px] text-subtle" title={preset.baseUrl}>
              {providerLabel(preset.provider)} · {preset.baseUrl}
            </span>
          </button>
        )
      })}
    </div>
  )
}
