import { useEffect, useRef } from 'react'
import '@xterm/xterm/css/xterm.css'
import {
  resizeTerminal,
  useTerminalStore,
} from '../stores/terminal'
import { useWorkbenchStore } from '../stores/workbench'

// TerminalPanel 是底部终端抽屉：xterm 渲染 + 后端 PTY。
// 终端随会话创建（一个会话一个实例），抽屉收起只隐藏视图，PTY 继续运行。
export default function TerminalPanel() {
  const activeId = useWorkbenchStore((state) => state.activeId)
  const workDir = useWorkbenchStore((state) => state.workDir)
  const controller = useTerminalStore((state) => state.controller)
  const exited = useTerminalStore((state) => state.exited)
  const error = useTerminalStore((state) => state.error)
  const attach = useTerminalStore((state) => state.attach)
  const reset = useTerminalStore((state) => state.reset)
  const hostRef = useRef<HTMLDivElement | null>(null)

  // 挂接当前会话的终端：首次创建 PTY，重复调用复用。
  useEffect(() => {
    if (!activeId) return
    void attach(activeId, workDir)
  }, [activeId, workDir, attach])

  // 把 xterm 实例挂到 DOM 并自适应尺寸；controller 变化（新终端）时重建。
  useEffect(() => {
    const host = hostRef.current
    if (!controller || !host) return
    const { terminal, fit } = controller
    terminal.open(host)
    try {
      fit.fit()
      void resizeTerminal(controller.terminalId, fit)
    } catch {
      // 面板尺寸未就绪时忽略，ResizeObserver 会再次触发。
    }
    terminal.focus()

    const observer = new ResizeObserver(() => {
      try {
        fit.fit()
        void resizeTerminal(controller.terminalId, fit)
      } catch {
        // 收起动画中尺寸可能非法：忽略。
      }
    })
    observer.observe(host)
    return () => observer.disconnect()
  }, [controller])

  if (!activeId) {
    return (
      <div data-testid="terminal-panel" className="shrink-0 border-t border-line/70 bg-panel">
        <p className="py-6 text-center font-mono text-[11px] text-subtle">选择会话后打开终端</p>
      </div>
    )
  }

  return (
    <div
      data-testid="terminal-panel"
      className="relative flex h-[38vh] max-h-[420px] min-h-[180px] shrink-0 flex-col border-t border-line/70 bg-panel"
    >
      <div className="flex shrink-0 items-center gap-2 border-b border-line/70 px-3 py-1.5">
        <span className="font-mono text-[10px] uppercase tracking-[0.14em] text-subtle">
          终端
        </span>
        <span className="min-w-0 truncate font-mono text-[10px] text-subtle">{workDir}</span>
        <div className="ml-auto flex items-center gap-1.5">
          {exited && (
            <button
              type="button"
              data-testid="terminal-restart"
              onClick={() => void attach(activeId, workDir)}
              className="cursor-pointer rounded-md border border-line px-2 py-0.5 font-mono text-[10px] text-muted transition hover:border-accent hover:text-accent-text"
            >
              重新打开
            </button>
          )}
          <button
            type="button"
            data-testid="terminal-close"
            onClick={reset}
            className="cursor-pointer rounded-md border border-line px-2 py-0.5 font-mono text-[10px] text-muted transition hover:border-danger hover:text-danger-text"
          >
            关闭终端
          </button>
        </div>
      </div>
      {error && (
        <p className="shrink-0 px-3 py-1 font-mono text-[10px] text-danger-text">{error}</p>
      )}
      <div className="relative min-h-0 flex-1 px-2 py-1">
        <div ref={hostRef} data-testid="terminal-host" className="h-full w-full" />
      </div>
    </div>
  )
}
