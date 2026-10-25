import { useRef, useState } from 'react'
import { formatTokens } from '../lib/format'
import { imageDataUrl, prepareImageFile } from '../lib/image'
import { useAppStore } from '../stores/app'
import { useWorkbenchStore } from '../stores/workbench'
import type { UserImage } from '../types/chat'
import PermissionModeSelect from './PermissionModeSelect'

// ModelContextMeter 显示当前模型与上下文占用：已用 / 窗口上限。
function ModelContextMeter() {
  const model = useWorkbenchStore((state) => state.model)
  const contextWindow = useWorkbenchStore((state) => state.contextWindow)
  const contextTokens = useWorkbenchStore((state) => state.contextTokens)
  const ratio = contextWindow > 0 ? contextTokens / contextWindow : 0
  const title = [
    `模型：${model || '未配置'}`,
    `当前上下文：${contextTokens.toLocaleString()} / ${contextWindow.toLocaleString()} tokens`,
  ].join(' · ')

  return (
    <span
      data-testid="model-context"
      title={title}
      className="@max-[280px]:hidden flex min-w-0 cursor-default items-center gap-1.5 font-mono text-[10px]"
    >
      <span className="max-w-36 truncate text-muted">{model || '未配置模型'}</span>
      <span
        className={`shrink-0 tabular-nums ${ratio >= 0.8 ? 'text-warn-text' : 'text-subtle'}`}
      >
        {formatTokens(contextTokens)}/{formatTokens(contextWindow)}
      </span>
    </span>
  )
}

// UsageMeter 是输入框内的 token 统计，与模型名并排显示。
function UsageMeter() {
  const usage = useWorkbenchStore((state) => state.usage)
  const roundUsage = useWorkbenchStore((state) => state.roundUsage)
  const running = useWorkbenchStore((state) => state.running)

  const current = running && roundUsage ? roundUsage : usage
  const label = running && roundUsage ? '本轮' : '累计'
  const title = [
    `${label}用量`,
    `输入 ${current.inputTokens}`,
    `输出 ${current.outputTokens}`,
    `缓存读 ${current.cacheReadInputTokens}`,
    `缓存写 ${current.cacheCreationInputTokens}`,
  ].join(' · ')

  return (
    <span
      data-testid="usage"
      title={title}
      className="@max-[560px]:hidden inline shrink-0 cursor-default font-mono tabular-nums text-[10px] text-subtle"
    >
      ↑{formatTokens(current.inputTokens)} ↓{formatTokens(current.outputTokens)}
    </span>
  )
}

// AttachIcon 是附件按钮里的图片图标。
function AttachIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-3.5"
      aria-hidden="true"
    >
      <rect x="3" y="3" width="18" height="18" rx="3" />
      <circle cx="9" cy="9" r="2" />
      <path d="m21 15-3.1-3.1a2 2 0 0 0-2.8 0L6 21" />
    </svg>
  )
}

interface ComposerProps {
  // hero：空会话时悬浮在内容区中央；docked：会话进行中固定在底部。
  variant?: 'hero' | 'docked'
}

export default function Composer({ variant = 'docked' }: ComposerProps) {
  const [text, setText] = useState('')
  const [images, setImages] = useState<UserImage[]>([])
  const [imageNames, setImageNames] = useState<string[]>([])
  const [imageError, setImageError] = useState<string | null>(null)
  const [dragOver, setDragOver] = useState(false)
  const fileInputRef = useRef<HTMLInputElement>(null)

  const status = useAppStore((state) => state.status)
  const running = useWorkbenchStore((state) => state.running)
  const loading = useWorkbenchStore((state) => state.loading)
  const compacting = useWorkbenchStore((state) => state.compacting)
  const retry = useWorkbenchStore((state) => state.retry)
  const notice = useWorkbenchStore((state) => state.notice)
  const stopReason = useWorkbenchStore((state) => state.stopReason)
  const send = useWorkbenchStore((state) => state.send)
  const cancel = useWorkbenchStore((state) => state.cancel)

  const hero = variant === 'hero'
  const canSend =
    status === 'ready' && !running && !loading && (text.trim() !== '' || images.length > 0)
  const hasStatus = compacting || retry !== null || notice !== null

  // addImageFiles 压缩并追加图片；单张失败不阻塞其余图片。
  const addImageFiles = async (files: Iterable<File | Blob>) => {
    const incoming = Array.from(files).filter(
      (file) => !('type' in file) || file.type === '' || file.type.startsWith('image/'),
    )
    if (incoming.length === 0) return
    setImageError(null)
    for (const file of incoming) {
      const name = 'name' in file && typeof file.name === 'string' ? file.name : ''
      try {
        const prepared = await prepareImageFile(file, name)
        setImages((prev) => [...prev, { mediaType: prepared.mediaType, data: prepared.data }])
        setImageNames((prev) => [...prev, prepared.name || `图片 ${prev.length + 1}`])
      } catch (err) {
        setImageError(`${name || '图片'}：${err instanceof Error ? err.message : '无法读取'}`)
      }
    }
  }

  const removeImage = (index: number) => {
    setImages((prev) => prev.filter((_, i) => i !== index))
    setImageNames((prev) => prev.filter((_, i) => i !== index))
  }

  const submit = () => {
    if (!canSend) return
    const value = text.trim()
    const pending = images
    setText('')
    setImages([])
    setImageNames([])
    setImageError(null)
    void send(value, pending)
  }

  return (
    <div
      data-testid="composer"
      className={hero ? 'w-full max-w-[46rem]' : 'shrink-0 px-5 pb-5 pt-2'}
    >
      <div className={hero ? 'w-full' : 'mx-auto w-full max-w-[46rem]'}>
        <div
          className={`rounded-2xl border bg-panel transition focus-within:border-accent focus-within:ring-4 focus-within:ring-accent-soft ${
            dragOver ? 'border-accent' : 'border-line'
          } ${hero ? 'shadow-pane' : 'shadow-card'}`}
          onDragOver={(event) => {
            event.preventDefault()
            setDragOver(true)
          }}
          onDragLeave={() => setDragOver(false)}
          onDrop={(event) => {
            event.preventDefault()
            setDragOver(false)
            if (status === 'ready') void addImageFiles(event.dataTransfer.files)
          }}
        >
          {images.length > 0 && (
            <div
              data-testid="composer-attachments"
              className="flex flex-wrap gap-2 px-4 pt-3"
            >
              {images.map((img, index) => (
                <span
                  key={`${index}-${img.data.length}`}
                  className="group relative inline-block"
                  title={imageNames[index]}
                >
                  <img
                    src={imageDataUrl(img.mediaType, img.data)}
                    alt={imageNames[index]}
                    className="h-14 w-14 rounded-md border border-line object-cover"
                  />
                  <button
                    type="button"
                    onClick={() => removeImage(index)}
                    aria-label={`移除 ${imageNames[index]}`}
                    className="absolute -right-1.5 -top-1.5 flex size-4 cursor-pointer items-center justify-center rounded-full border border-line bg-panel font-mono text-[9px] leading-none text-muted shadow-card transition hover:border-danger hover:text-danger-text"
                  >
                    ×
                  </button>
                </span>
              ))}
            </div>
          )}
          <textarea
            value={text}
            onChange={(event) => setText(event.target.value)}
            onKeyDown={(event) => {
              if (event.key !== 'Enter' || event.shiftKey) return
              if (event.nativeEvent.isComposing) return
              event.preventDefault()
              submit()
            }}
            onPaste={(event) => {
              const files = Array.from(event.clipboardData.files ?? [])
              if (files.some((file) => file.type.startsWith('image/'))) {
                event.preventDefault()
                void addImageFiles(files)
              }
            }}
            rows={3}
            disabled={status !== 'ready'}
            placeholder={status === 'ready' ? '输入消息，可粘贴或拖入图片' : '后端未连接'}
            className="composer-input w-full resize-none rounded-t-2xl bg-transparent px-4 py-3 text-sm leading-6 text-fg outline-none placeholder:text-subtle disabled:cursor-not-allowed"
          />
          <div className="@container flex items-center justify-between gap-3 border-t border-line/70 px-3 py-2">
            <div className="flex min-w-0 items-center gap-2">
              <input
                ref={fileInputRef}
                type="file"
                accept="image/png,image/jpeg,image/gif,image/webp"
                multiple
                className="hidden"
                onChange={(event) => {
                  if (event.target.files) void addImageFiles(event.target.files)
                  event.target.value = ''
                }}
              />
              <button
                type="button"
                data-testid="attach-image"
                title="添加图片"
                disabled={status !== 'ready'}
                onClick={() => fileInputRef.current?.click()}
                className="shrink-0 cursor-pointer rounded-md border border-line bg-raised p-1.5 text-subtle transition hover:border-accent hover:text-accent-text disabled:cursor-not-allowed"
              >
                <AttachIcon />
              </button>
              <PermissionModeSelect />
              {compacting && (
                <span className="shrink-0 font-mono text-[10px] text-warn-text">压缩中…</span>
              )}
              {retry && (
                <span className="shrink-0 font-mono text-[10px] text-warn-text">
                  重试 {retry.attempt}/{retry.max}
                </span>
              )}
              {notice && (
                <span className="truncate font-mono text-[10px] text-accent-text">{notice}</span>
              )}
              {imageError && (
                <span
                  data-testid="image-error"
                  className="truncate font-mono text-[10px] text-danger-text"
                >
                  {imageError}
                </span>
              )}
              {!running && !hasStatus && stopReason && stopReason !== 'completed' && (
                <span className="@max-[420px]:hidden shrink-0 font-mono text-[10px] text-subtle">
                  上轮 {stopReason}
                </span>
              )}
            </div>
            <div className="flex min-w-0 flex-1 items-center justify-end gap-2.5">
              <ModelContextMeter />
              <UsageMeter />
              {running && (
                <button
                  type="button"
                  onClick={() => void cancel()}
                  className="shrink-0 cursor-pointer rounded-full border border-danger-line bg-danger-soft px-3 py-1 text-[11px] text-danger-text transition hover:border-danger"
                >
                  停止本轮
                </button>
              )}
              <button
                type="button"
                onClick={submit}
                disabled={!canSend}
                className="shrink-0 cursor-pointer rounded-full bg-accent px-4 py-1.5 text-[11px] font-medium text-on-accent shadow-card transition hover:bg-accent-hover active:translate-y-px disabled:cursor-not-allowed disabled:bg-hover disabled:text-subtle disabled:shadow-none"
              >
                发送
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}
