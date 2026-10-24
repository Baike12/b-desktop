import { useCallback, useEffect, useRef } from 'react'
import { useWorkbenchStore } from '../stores/workbench'
import { MessageRow } from './MessageRow'

export default function MessageList() {
  const items = useWorkbenchStore((state) => state.items)
  const activeId = useWorkbenchStore((state) => state.activeId)
  const running = useWorkbenchStore((state) => state.running)
  const loading = useWorkbenchStore((state) => state.loading)

  const scrollerRef = useRef<HTMLDivElement | null>(null)
  const contentRef = useRef<HTMLDivElement | null>(null)
  // stick 表示用户是否停在底部；只有停在底部时才自动跟随新内容。
  const stickRef = useRef(true)

  const handleScroll = useCallback(() => {
    const element = scrollerRef.current
    if (!element) return
    const distance = element.scrollHeight - element.scrollTop - element.clientHeight
    stickRef.current = distance < 96
  }, [])

  useEffect(() => {
    const element = scrollerRef.current
    if (!element || !stickRef.current) return
    element.scrollTop = element.scrollHeight
  }, [items])

  // Markdown 用 useDeferredValue 延迟渲染，内容高度会在 items 更新之后才变化；
  // 用 ResizeObserver 兜底，避免流式输出时视口落后一帧。
  useEffect(() => {
    const scroller = scrollerRef.current
    const content = contentRef.current
    if (!scroller || !content) return
    const observer = new ResizeObserver(() => {
      if (!stickRef.current) return
      scroller.scrollTop = scroller.scrollHeight
    })
    observer.observe(content)
    return () => observer.disconnect()
  }, [])

  return (
    <div
      ref={scrollerRef}
      onScroll={handleScroll}
      className="grid-canvas min-h-0 flex-1 overflow-y-auto"
    >
      <div
        ref={contentRef}
        className="mx-auto flex w-full max-w-[46rem] flex-col gap-6 px-5 py-8"
      >
        {!activeId && !loading && (
          <div className="py-16 text-center font-mono text-[11px] tracking-wide text-subtle">
            从左侧选择会话，或新建一个会话开始。
          </div>
        )}
        {activeId && items.length === 0 && !loading && (
          <div className="py-16 text-center font-mono text-[11px] tracking-wide text-subtle">
            发送第一条消息开始对话。
          </div>
        )}
        {items.map((item) => (
          <MessageRow key={item.id} item={item} />
        ))}
        {running && items.length === 0 && (
          <p className="text-center text-xs text-subtle">等待模型响应…</p>
        )}
      </div>
    </div>
  )
}
