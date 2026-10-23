import { useEffect } from 'react'
import { EventsOff, EventsOn } from '../../wailsjs/runtime/runtime'
import { useWorkbenchStore } from '../stores/workbench'

export const SESSION_TITLE_EVENT_NAME = 'session:title'

interface SessionTitlePayload {
  sessionId?: unknown
  title?: unknown
}

// useSessionTitleEvents 订阅后端 "session:title" 事件。
// 首轮消息后标题由模型生成，前端只更新对应列表项与标题栏，不重新加载会话。
export function useSessionTitleEvents(): void {
  useEffect(() => {
    const unsubscribe = EventsOn(SESSION_TITLE_EVENT_NAME, (...data: unknown[]) => {
      const payload = data[0] as SessionTitlePayload | undefined
      if (!payload || typeof payload.sessionId !== 'string' || typeof payload.title !== 'string') {
        return
      }
      useWorkbenchStore.getState().applySessionTitle(payload.sessionId, payload.title)
    })

    return () => {
      unsubscribe()
      EventsOff(SESSION_TITLE_EVENT_NAME)
    }
  }, [])
}
