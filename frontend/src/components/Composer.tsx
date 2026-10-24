import { useState } from 'react'
import { formatTokens } from '../lib/format'
import { useAppStore } from '../stores/app'
import { useWorkbenchStore } from '../stores/workbench'
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

interface ComposerProps {
  // hero：空会话时悬浮在内容区中央；docked：会话进行中固定在底部。
  variant?: 'hero' | 'docked'
}

export default function Composer({ variant = 'docked' }: ComposerProps) {
  const [text, setText] = useState('')
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
  const canSend = status === 'ready' && !running && !loading && text.trim() !== ''
  const hasStatus = compacting || retry !== null || notice !== null
  const submit = () => {
    if (!canSend) return
    const value = text.trim()
    setText('')
    void send(value)
  }

  return (
    <div
      data-testid="composer"
      className={hero ? 'w-full max-w-[46rem]' : 'shrink-0 px-5 pb-4 pt-2'}
    >
      <div className={hero ? 'w-full' : 'mx-auto w-full max-w-[46rem]'}>
        <div className="border border-line bg-surface transition-colors focus-within:border-accent">
          <textarea
            value={text}
            onChange={(event) => setText(event.target.value)}
            onKeyDown={(event) => {
              if (event.key !== 'Enter' || event.shiftKey) return
              if (event.nativeEvent.isComposing) return
              event.preventDefault()
              submit()
            }}
            rows={3}
            disabled={status !== 'ready'}
            placeholder={status === 'ready' ? '输入消息' : '后端未连接'}
            className="composer-input w-full resize-none bg-transparent px-3 py-2.5 text-sm leading-6 text-fg outline-none placeholder:text-subtle disabled:cursor-not-allowed"
          />
          <div className="@container flex items-center justify-between gap-3 border-t border-line px-2.5 py-1.5">
            <div className="flex min-w-0 items-center gap-2">
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
                  className="shrink-0 cursor-pointer border border-danger-line bg-danger-soft px-3 py-1 text-[11px] text-danger-text transition hover:border-danger"
                >
                  停止本轮
                </button>
              )}
              <button
                type="button"
                onClick={submit}
                disabled={!canSend}
                className="shrink-0 cursor-pointer bg-accent px-4 py-1 text-[11px] font-medium text-on-accent transition hover:bg-accent-hover disabled:cursor-not-allowed disabled:bg-hover disabled:text-subtle"
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
