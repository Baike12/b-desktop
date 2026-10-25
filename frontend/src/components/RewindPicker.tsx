import { useEffect, useRef } from 'react'
import { useWorkbenchStore } from '../stores/workbench'
import { formatClock } from '../lib/format'

// RewindPicker 是回溯选择器：双击 Esc（或双击用户气泡）打开，
// 选中一条历史用户消息后，其后的全部消息作废、内容回填输入框重新编辑。
export default function RewindPicker() {
  const open = useWorkbenchStore((state) => state.rewindOpen)
  const points = useWorkbenchStore((state) => state.rewindPoints)
  const rewindTo = useWorkbenchStore((state) => state.rewindTo)
  const closeRewind = useWorkbenchStore((state) => state.closeRewind)
  const inputRef = useRef<HTMLInputElement | null>(null)

  useEffect(() => {
    if (open) inputRef.current?.focus()
  }, [open])

  if (!open) return null

  return (
    <div
      data-testid="rewind-picker"
      className="fixed inset-0 z-40 flex items-start justify-center bg-black/25 pt-[16vh]"
      onClick={closeRewind}
    >
      <div
        className="w-[34rem] max-w-[90vw] overflow-hidden rounded-xl border border-line bg-panel shadow-pane"
        onClick={(event) => event.stopPropagation()}
      >
        <div className="flex items-center gap-2 border-b border-line/70 px-4 py-2.5">
          <span className="text-[12px] font-medium text-fg">回溯到某条消息</span>
          <span className="ml-auto font-mono text-[10px] text-subtle">
            之后的消息将作废 · Esc 关闭
          </span>
        </div>
        {points.length === 0 ? (
          <p className="px-4 py-6 text-center font-mono text-[11px] text-subtle">
            还没有可回溯的历史消息
          </p>
        ) : (
          <ul className="max-h-[50vh] overflow-auto py-1">
            {points.map((point) => (
              <li key={point.messageId}>
                <button
                  type="button"
                  data-testid={`rewind-point-${point.messageId}`}
                  onClick={() => void rewindTo(point.messageId)}
                  className="flex w-full cursor-pointer flex-col gap-1 border-b border-line/40 px-4 py-2.5 text-left transition last:border-b-0 hover:bg-hover"
                >
                  <span className="flex items-baseline gap-2">
                    <span className="font-mono text-[10px] text-subtle">
                      {point.timeMs > 0 ? formatClock(point.timeMs) : ''}
                    </span>
                    {point.imageCount > 0 && (
                      <span className="font-mono text-[10px] text-subtle">
                        +{point.imageCount} 图
                      </span>
                    )}
                  </span>
                  <span className="line-clamp-2 text-[12px] leading-5 text-fg">
                    {point.preview || '（图片消息）'}
                  </span>
                </button>
              </li>
            ))}
          </ul>
        )}
        <input
          ref={inputRef}
          aria-hidden="true"
          tabIndex={-1}
          onKeyDown={(event) => {
            if (event.key === 'Escape') {
              event.preventDefault()
              closeRewind()
            }
          }}
          className="sr-only"
        />
      </div>
    </div>
  )
}
