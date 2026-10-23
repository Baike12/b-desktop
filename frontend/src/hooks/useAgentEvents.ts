import { useEffect } from 'react'
import { EventsOff, EventsOn } from '../../wailsjs/runtime/runtime'
import { useWorkbenchStore } from '../stores/workbench'
import type { AgentEvent } from '../types/agent'

export const AGENT_EVENT_NAME = 'agent:event'

// useAgentEvents 订阅后端唯一的 "agent:event" 事件流。
// 事件先用 requestAnimationFrame 合批，再交给 store 归并：
// 一帧内几十条 text_delta 只触发一次 React 更新，避免逐字全量重排。
// 卸载时取消 rAF 并 EventsOff，不做任何轮询。
export function useAgentEvents(): void {
  useEffect(() => {
    let queue: AgentEvent[] = []
    let frame = 0

    const flush = () => {
      frame = 0
      if (queue.length === 0) return
      const batch = queue
      queue = []
      useWorkbenchStore.getState().ingest(batch)
    }

    const unsubscribe = EventsOn(AGENT_EVENT_NAME, (...data: unknown[]) => {
      const payload = data[0]
      if (!payload || typeof payload !== 'object') return
      queue.push(payload as AgentEvent)
      if (frame === 0) frame = requestAnimationFrame(flush)
    })

    return () => {
      unsubscribe()
      EventsOff(AGENT_EVENT_NAME)
      if (frame !== 0) cancelAnimationFrame(frame)
      queue = []
    }
  }, [])
}
