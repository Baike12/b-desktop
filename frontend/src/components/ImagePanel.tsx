import { useState } from 'react'
import { GenerateImage } from '../../wailsjs/go/app/App'
import { errorMessage } from '../stores/workbench'

// 常用图片尺寸；后端未指定尺寸时使用 1024x1024。
const SIZE_OPTIONS = [
  '1024x1024',
  '512x512',
  '1536x1024',
  '1024x1536',
  '1792x1024',
  '1024x1792',
] as const

const fieldClass =
  'w-full rounded-lg border border-line bg-surface px-3 py-2 text-sm text-fg outline-none transition placeholder:text-subtle focus:border-accent disabled:cursor-not-allowed disabled:opacity-60'

// ImagePanel 通过 OpenAI 兼容的图片接口生成图片，并展示落盘路径。
// 接口地址、模型与密钥沿用当前 Provider 配置（由后端回落到已保存配置）。
export default function ImagePanel({ className = '' }: { className?: string }) {
  const [prompt, setPrompt] = useState('')
  const [size, setSize] = useState<string>(SIZE_OPTIONS[0])
  const [generating, setGenerating] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [paths, setPaths] = useState<string[]>([])
  const [copiedPath, setCopiedPath] = useState<string | null>(null)

  const canGenerate = prompt.trim() !== '' && !generating

  const generate = async () => {
    const value = prompt.trim()
    if (value === '' || generating) return
    setGenerating(true)
    setError(null)
    setCopiedPath(null)
    try {
      // model / baseURL / apiKey 传空串，由后端回落到当前 Provider 配置。
      const result = await GenerateImage(value, size, '', '', '')
      setPaths(result.paths)
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setGenerating(false)
    }
  }

  const copyPath = async (path: string) => {
    try {
      await navigator.clipboard.writeText(path)
      setCopiedPath(path)
    } catch {
      setError('复制失败，请手动选择路径。')
    }
  }

  return (
    <section
      data-testid="image-panel"
      className={`flex flex-col gap-3 rounded-xl border border-line bg-surface p-4 ${className}`}
    >
      <header>
        <h2 className="text-sm font-semibold text-fg">图片生成</h2>
        <p className="mt-0.5 text-[11px] text-subtle">
          接口地址、模型与密钥沿用模型设置中的当前配置；结果保存到 ~/.b-desktop/images。
        </p>
      </header>

      <label className="block">
        <span className="mb-1 block text-[11px] text-muted">提示词</span>
        <textarea
          data-testid="image-prompt"
          value={prompt}
          disabled={generating}
          rows={4}
          onChange={(event) => setPrompt(event.target.value)}
          onKeyDown={(event) => {
            if (event.key !== 'Enter' || !(event.metaKey || event.ctrlKey)) return
            if (event.nativeEvent.isComposing) return
            event.preventDefault()
            void generate()
          }}
          placeholder="描述要生成的图片，例如：雨夜的东京街角，霓虹灯倒映在积水里"
          className={`${fieldClass} resize-none leading-6`}
        />
      </label>

      <div className="flex flex-wrap items-end gap-3">
        <label className="block">
          <span className="mb-1 block text-[11px] text-muted">尺寸</span>
          <select
            data-testid="image-size"
            value={size}
            disabled={generating}
            onChange={(event) => setSize(event.target.value)}
            className={`${fieldClass} w-40 cursor-pointer`}
          >
            {SIZE_OPTIONS.map((option) => (
              <option key={option} value={option}>
                {option}
              </option>
            ))}
          </select>
        </label>
        <button
          type="button"
          data-testid="image-submit"
          onClick={() => void generate()}
          disabled={!canGenerate}
          className="cursor-pointer rounded-lg bg-accent px-4 py-2 text-xs font-medium text-on-accent hover:bg-accent-hover disabled:cursor-not-allowed disabled:opacity-50"
        >
          {generating ? '生成中…' : '生成图片'}
        </button>
        <span className="text-[11px] text-subtle">⌘/Ctrl + Enter 生成</span>
      </div>

      {generating && (
        <p className="text-[11px] text-warn-text">正在请求图片接口并写入本地目录…</p>
      )}

      {error && (
        <p
          role="alert"
          data-testid="image-error"
          className="rounded-md border border-danger-line bg-danger-soft px-3 py-2 text-[11px] text-danger-text"
        >
          {error}
        </p>
      )}

      {paths.length > 0 && (
        <div data-testid="image-result">
          <div className="flex items-center justify-between">
            <span className="text-[11px] text-muted">本次生成 {paths.length} 张</span>
            {copiedPath && <span className="text-[11px] text-success-text">已复制路径</span>}
          </div>
          <ul className="mt-1.5 space-y-1.5">
            {paths.map((path, index) => (
              <li
                key={`${path}-${index}`}
                data-testid={`image-path-${index}`}
                className="flex items-center gap-2 rounded-lg border border-line bg-raised px-3 py-2"
              >
                <span className="min-w-0 flex-1 break-all font-mono text-[11px] text-fg">
                  {path}
                </span>
                <button
                  type="button"
                  data-testid={`image-copy-${index}`}
                  onClick={() => void copyPath(path)}
                  className="shrink-0 cursor-pointer rounded-md border border-line-strong px-2 py-1 text-[11px] text-muted transition hover:bg-hover hover:text-fg"
                >
                  复制
                </button>
              </li>
            ))}
          </ul>
        </div>
      )}
    </section>
  )
}
