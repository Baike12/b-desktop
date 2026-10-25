import { useEffect, useMemo, useRef, useState } from 'react'
import { ListSlashCommands, SearchSessionFiles } from '../../wailsjs/go/app/App'
import type { app } from '../../wailsjs/go/models'
import { formatTokens } from '../lib/format'
import { imageDataUrl, prepareImageFile } from '../lib/image'
import { useAppStore } from '../stores/app'
import { useWorkbenchStore } from '../stores/workbench'
import type { UserImage } from '../types/chat'
import PermissionModeSelect from './PermissionModeSelect'

// 补全面板的候选：@ 文件路径或 / 命令。
type SuggestCandidate =
  | { kind: 'file'; path: string }
  | { kind: 'command'; name: string; description: string; argumentHint: string; source: string }

// SuggestTrigger 是当前激活的补全触发器：start 是触发字符（@ 或 /）在文本中的下标。
type SuggestTrigger = { mode: 'at' | 'slash'; start: number; query: string }

const MAX_SUGGESTIONS = 20
const FILE_SEARCH_DEBOUNCE_MS = 120

// detectTrigger 检测光标前是否处于补全上下文：@ 后的路径 token（支持中文紧邻），
// 或文本开头的 / 命令 token。与参考实现的触发规则一致。
function detectTrigger(value: string, caret: number): SuggestTrigger | null {
  const before = value.slice(0, caret)
  const at = /(?:^|[\s\p{Script=Han}])@([^\s]*)$/u.exec(before)
  if (at) return { mode: 'at', start: caret - at[1].length - 1, query: at[1] }
  const slash = /^\/([^\s]*)$/.exec(before)
  if (slash) return { mode: 'slash', start: 0, query: slash[1] }
  return null
}

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
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  // 补全面板状态。
  const [suggest, setSuggest] = useState<SuggestTrigger | null>(null)
  const [fileCandidates, setFileCandidates] = useState<string[]>([])
  const [commands, setCommands] = useState<app.SlashCommandDTO[]>([])
  const [selected, setSelected] = useState(0)
  // dismissedRef 记录 Esc 关闭时的触发签名：同一 token 不再自动弹回，
  // 输入变化后才能重新打开（与参考实现的 Esc 语义一致）。
  const dismissedRef = useRef<string | null>(null)
  // pendingCaret 是候选写回后的光标位置，在文本更新后应用。
  const pendingCaret = useRef<number | null>(null)

  const status = useAppStore((state) => state.status)
  const running = useWorkbenchStore((state) => state.running)
  const loading = useWorkbenchStore((state) => state.loading)
  const compacting = useWorkbenchStore((state) => state.compacting)
  const retry = useWorkbenchStore((state) => state.retry)
  const notice = useWorkbenchStore((state) => state.notice)
  const stopReason = useWorkbenchStore((state) => state.stopReason)
  const activeId = useWorkbenchStore((state) => state.activeId)
  const workDir = useWorkbenchStore((state) => state.workDir)
  const send = useWorkbenchStore((state) => state.send)
  const cancel = useWorkbenchStore((state) => state.cancel)

  const hero = variant === 'hero'
  const canSend =
    status === 'ready' && !running && !loading && (text.trim() !== '' || images.length > 0)
  const hasStatus = compacting || retry !== null || notice !== null

  const candidates = useMemo<SuggestCandidate[]>(() => {
    if (!suggest) return []
    if (suggest.mode === 'at') {
      return fileCandidates.slice(0, MAX_SUGGESTIONS).map((path) => ({ kind: 'file', path }))
    }
    const query = suggest.query.toLowerCase()
    return commands
      .filter((command) => command.name.toLowerCase().includes(query))
      .slice(0, MAX_SUGGESTIONS)
      .map((command) => ({ kind: 'command', ...command }))
  }, [suggest, fileCandidates, commands])

  useEffect(() => {
    if (selected >= candidates.length) setSelected(0)
  }, [candidates.length, selected])

  // @ 模式下的文件候选：防抖搜索工作区文件。
  useEffect(() => {
    if (suggest?.mode !== 'at' || !activeId || suggest.query.trim() === '') {
      setFileCandidates([])
      return
    }
    let cancelled = false
    const handle = setTimeout(async () => {
      try {
        const paths = await SearchSessionFiles(activeId, workDir, suggest.query)
        if (!cancelled) setFileCandidates(paths)
      } catch {
        if (!cancelled) setFileCandidates([])
      }
    }, FILE_SEARCH_DEBOUNCE_MS)
    return () => {
      cancelled = true
      clearTimeout(handle)
    }
  }, [suggest, activeId, workDir])

  // / 模式打开时加载一次命令清单（内置命令 + 当前工作目录的技能）。
  useEffect(() => {
    if (suggest?.mode !== 'slash') return
    let cancelled = false
    ListSlashCommands(workDir)
      .then((list) => {
        if (!cancelled) setCommands(list)
      })
      .catch(() => {
        if (!cancelled) setCommands([])
      })
    return () => {
      cancelled = true
    }
  }, [suggest?.mode, workDir])

  // 文本更新后把光标移到候选写回的位置。
  useEffect(() => {
    if (pendingCaret.current === null) return
    const position = pendingCaret.current
    pendingCaret.current = null
    textareaRef.current?.setSelectionRange(position, position)
  }, [text])

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
    setSuggest(null)
    void send(value, pending)
  }

  // handleChange 在文本变化后重新检测补全触发器；
  // Esc 关闭后的同一触发签名不再弹回，输入变化后恢复。
  const handleChange = (value: string) => {
    setText(value)
    const caret = textareaRef.current?.selectionStart ?? value.length
    const trigger = detectTrigger(value, caret)
    if (!trigger) {
      setSuggest(null)
      return
    }
    const signature = `${trigger.mode}:${trigger.query}`
    if (dismissedRef.current !== null && dismissedRef.current !== signature) {
      dismissedRef.current = null
    }
    if (dismissedRef.current === signature) {
      setSuggest(null)
      return
    }
    setSuggest(trigger)
    setSelected(0)
  }

  // applySuggestion 把选中候选写回输入框：替换 @query 或 /query token 并补一个空格。
  const applySuggestion = (candidate: SuggestCandidate) => {
    if (!suggest || !textareaRef.current) return
    const caret = textareaRef.current.selectionStart ?? text.length
    const replacement =
      candidate.kind === 'file'
        ? `@${candidate.path} `
        : `/${candidate.name} `
    const next = text.slice(0, suggest.start) + replacement + text.slice(caret)
    dismissedRef.current = null
    pendingCaret.current = suggest.start + replacement.length
    setText(next)
    setSuggest(null)
    textareaRef.current.focus()
  }

  const closeSuggest = () => {
    if (suggest) dismissedRef.current = `${suggest.mode}:${suggest.query}`
    setSuggest(null)
  }

  const handleKeyDown = (event: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (suggest && candidates.length > 0) {
      if (event.key === 'ArrowDown') {
        event.preventDefault()
        setSelected((prev) => (prev + 1) % candidates.length)
        return
      }
      if (event.key === 'ArrowUp') {
        event.preventDefault()
        setSelected((prev) => (prev - 1 + candidates.length) % candidates.length)
        return
      }
      if (event.key === 'Enter' || event.key === 'Tab') {
        event.preventDefault()
        applySuggestion(candidates[selected]!)
        return
      }
      if (event.key === 'Escape') {
        event.preventDefault()
        closeSuggest()
        return
      }
    } else if (event.key === 'Escape' && suggest) {
      event.preventDefault()
      closeSuggest()
      return
    }
    if (event.key !== 'Enter' || event.shiftKey) return
    if (event.nativeEvent.isComposing) return
    event.preventDefault()
    submit()
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
          {suggest && (
            <div
              data-testid="composer-suggest"
              className="mx-3 mt-3 overflow-hidden rounded-lg border border-line bg-raised"
            >
              {candidates.length === 0 ? (
                <p className="px-3 py-2 font-mono text-[10px] text-subtle">
                  {suggest.mode === 'at'
                    ? '输入关键字搜索工作区文件，Tab 跳过候选'
                    : '没有匹配的命令或技能'}
                </p>
              ) : (
                <ul className="max-h-52 overflow-auto py-1">
                  {candidates.map((candidate, index) => {
                    const active = index === selected
                    const label =
                      candidate.kind === 'file' ? `@${candidate.path}` : `/${candidate.name}`
                    return (
                      <li key={label}>
                        <button
                          type="button"
                          data-testid={`suggest-item-${index}`}
                          onMouseDown={(event) => {
                            event.preventDefault()
                            applySuggestion(candidate)
                          }}
                          onMouseEnter={() => setSelected(index)}
                          className={`flex w-full cursor-pointer items-baseline gap-2 px-3 py-1.5 text-left transition ${
                            active
                              ? 'border-y border-line bg-surface shadow-card'
                              : 'border-y border-transparent'
                          }`}
                        >
                          <span className="shrink-0 font-mono text-[11px] text-accent-text">
                            {suggest.mode === 'at' ? '@' : '/'}
                          </span>
                          <span className="min-w-0 truncate font-mono text-[11px] text-fg">
                            {candidate.kind === 'file' ? candidate.path : candidate.name}
                          </span>
                          <span className="min-w-0 flex-1 truncate text-[11px] text-subtle">
                            {candidate.kind === 'command'
                              ? `${candidate.description}${candidate.argumentHint ? ` ${candidate.argumentHint}` : ''}${candidate.source === 'builtin' ? ' · 本地' : ''}`
                              : ''}
                          </span>
                        </button>
                      </li>
                    )
                  })}
                </ul>
              )}
              <p className="border-t border-line/70 bg-panel/60 px-3 py-1 font-mono text-[10px] text-subtle">
                ↑↓ 选择 · Enter/Tab 确认 · Esc 关闭
              </p>
            </div>
          )}
          <textarea
            ref={textareaRef}
            value={text}
            onChange={(event) => handleChange(event.target.value)}
            onKeyDown={handleKeyDown}
            onPaste={(event) => {
              const files = Array.from(event.clipboardData.files ?? [])
              if (files.some((file) => file.type.startsWith('image/'))) {
                event.preventDefault()
                void addImageFiles(files)
              }
            }}
            rows={3}
            disabled={status !== 'ready'}
            placeholder={
              status === 'ready' ? '输入消息，@ 引用文件，/ 调用命令，可粘贴图片' : '后端未连接'
            }
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
