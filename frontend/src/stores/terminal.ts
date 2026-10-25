import { create } from 'zustand'
import {
  CloseTerminal,
  CreateTerminal,
  TerminalResize,
  TerminalWrite,
} from '../../wailsjs/go/app/App'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { EventsOff, EventsOn } from '../../wailsjs/runtime/runtime'

// errorMessage 把 Wails 的 reject 值转成可展示文本（与 workbench.store 同语义）。
function errorMessage(err: unknown): string {
  if (err instanceof Error) return err.message
  if (typeof err === 'string') return err
  try {
    return JSON.stringify(err)
  } catch {
    return String(err)
  }
}

export const TERMINAL_DATA_EVENT = 'terminal:data'
export const TERMINAL_EXIT_EVENT = 'terminal:exit'
export const TERMINAL_CLOSED_EVENT = 'terminal:closed'

// TerminalController 聚合一个 xterm 实例与其后端终端绑定。
interface TerminalController {
  terminal: Terminal
  fit: FitAddon
  terminalId: string
  sessionId: string
  exited: boolean
}

interface TerminalState {
  // open 是底部抽屉的展开状态；PTY 在首次展开时创建，收起只隐藏面板。
  open: boolean
  // exited 表示当前会话的终端进程已退出（面板显示重启按钮）。
  exited: boolean
  ready: boolean
  error: string | null
  controller: TerminalController | null
  // sessionId 记录控制器归属的会话；切换会话后旧终端保留在后台。
  sessionId: string | null

  setOpen: (open: boolean) => void
  toggle: () => void
  attach: (sessionId: string, workDir: string) => Promise<void>
  detachIfSession: (sessionId: string) => void
  reset: () => void
}

// useTerminalStore 管理内置终端：与后端 PTY 一对一，事件直连 xterm。
export const useTerminalStore = create<TerminalState>((set, get) => {
  // handleData 是 terminal:data 事件的统一入口：按 terminalId 路由。
  const handleData = (payload: unknown) => {
    const data = payload as { terminalId?: string; data?: string } | null
    const controller = get().controller
    if (!data?.terminalId || !controller || data.terminalId !== controller.terminalId) return
    if (!data.data) return
    controller.terminal.write(base64ToBytes(data.data))
  }

  const handleExit = (payload: unknown) => {
    const data = payload as { terminalId?: string; error?: string } | null
    const controller = get().controller
    if (!data?.terminalId || !controller || data.terminalId !== controller.terminalId) return
    controller.exited = true
    set({
      exited: true,
      error: data.error ? `终端进程退出：${data.error}` : null,
    })
  }

  const handleClosed = (payload: unknown) => {
    const data = payload as { terminalId?: string } | null
    const controller = get().controller
    if (!data?.terminalId || !controller || data.terminalId !== controller.terminalId) return
    set({ exited: true })
  }

  return {
    open: false,
    exited: false,
    ready: false,
    error: null,
    controller: null,
    sessionId: null,

    setOpen: (open) => set({ open }),
    toggle: () => set((state) => ({ open: !state.open })),

    attach: async (sessionId, _workDir) => {
      const state = get()
      // 同一会话已有活终端：直接展开。
      if (state.controller && state.sessionId === sessionId && !state.exited) {
        set({ open: true })
        return
      }
      set({ open: true, error: null })
      try {
        const info = await CreateTerminal(sessionId)
        // CreateTerminal 复用既有终端：进程可能早已退出，重置视图重新挂接。
        disposeController(state.controller)
        EventsOn(TERMINAL_DATA_EVENT, handleData)
        EventsOn(TERMINAL_EXIT_EVENT, handleExit)
        EventsOn(TERMINAL_CLOSED_EVENT, handleClosed)

        const terminal = new Terminal({
          convertEol: false,
          cursorBlink: true,
          fontSize: 12,
          fontFamily: '"SF Mono", ui-monospace, Menlo, monospace',
          theme: {
            background: 'rgba(0,0,0,0)',
          },
          scrollback: 5000,
        })
        const fit = new FitAddon()
        terminal.loadAddon(fit)
        terminal.onData((data) => {
          void TerminalWrite(info.id, data).catch((err) =>
            set({ error: errorMessage(err) }),
          )
        })
        set({
          controller: {
            terminal,
            fit,
            terminalId: info.id,
            sessionId,
            exited: false,
          },
          sessionId,
          exited: false,
          ready: true,
        })
      } catch (err) {
        set({ error: errorMessage(err), ready: false })
      }
    },

    // detachIfSession 在会话切换时保留 PTY（后台继续运行），只断开视图引用。
    detachIfSession: (sessionId) => {
      if (get().sessionId !== sessionId) return
      set({ open: false })
    },

    // reset 关闭抽屉并杀掉终端进程（显式关闭时调用）。
    reset: () => {
      const state = get()
      const controller = state.controller
      set({ open: false, controller: null, sessionId: null, exited: false, ready: false })
      if (controller) {
        disposeController(controller)
        void CloseTerminal(controller.terminalId).catch(() => undefined)
      }
      EventsOff(TERMINAL_DATA_EVENT, TERMINAL_EXIT_EVENT, TERMINAL_CLOSED_EVENT)
    },
  }

  function disposeController(controller: TerminalController | null) {
    if (!controller) return
    try {
      controller.terminal.dispose()
    } catch {
      // xterm 在快速重挂时可能已释放：忽略。
    }
  }
})

// base64ToBytes 解码 PTY 输出。
function base64ToBytes(value: string): Uint8Array {
  const binary = atob(value)
  const out = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i += 1) out[i] = binary.charCodeAt(i)
  return out
}

// resizeTerminal 把 xterm 网格同步给后端 PTY。
export async function resizeTerminal(terminalId: string, fit: FitAddon): Promise<void> {
  const dimensions = fit.proposeDimensions()
  if (!dimensions || !dimensions.cols || !dimensions.rows) return
  await TerminalResize(terminalId, dimensions.cols, dimensions.rows).catch(() => undefined)
}
