import { useState } from 'react'
import { useWorkbenchStore } from '../stores/workbench'

// BenbenIcon 是本地 agent 的图标（贝壳纹样，呼应「笨笨」）。
function BenbenIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-3.5 shrink-0 text-accent-text"
      aria-hidden="true"
    >
      <path d="M12 3c4.5 2.4 7 6.4 7 10.4 0 3.5-3.1 6.1-7 6.1s-7-2.6-7-6.1C5 9.4 7.5 5.4 12 3Z" />
      <path d="M12 4v15" />
      <path d="M8.2 6.2c2.2 3.6 2.2 8.6 1 12.6" />
      <path d="M15.8 6.2c-2.2 3.6-2.2 8.6-1 12.6" />
    </svg>
  )
}

// ClaudeIcon 是 Claude Code 的星形标志。
function ClaudeIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="currentColor" className="size-3.5 shrink-0" aria-hidden="true">
      <path d="M12 2.5c.4 2.9 1.6 4.6 3.4 5.2-1.4 1.2-2 2.6-1.8 4.3 1.6-.4 3-.1 4.2 1-1.7 1.2-2.5 2.6-2.4 4.4-1.6-.7-3.1-.6-4.5.2-1.4-.8-2.9-.9-4.5-.2.1-1.8-.7-3.2-2.4-4.4 1.2-1.1 2.6-1.4 4.2-1 .2-1.7-.4-3.1-1.8-4.3 1.8-.6 3-2.3 3.4-5.2h2.2Z" />
      <circle cx="12" cy="12" r="2.1" fill="none" stroke="currentColor" strokeWidth="1.4" />
    </svg>
  )
}

function ChevronIcon({ open }: { open: boolean }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className={`size-3 shrink-0 transition-transform ${open ? 'rotate-180' : ''}`}
      aria-hidden="true"
    >
      <path d="m6 9 6 6 6-6" />
    </svg>
  )
}

// AgentSelect 是输入框内的 agent 切换器：本地 benben 与外部 ACP agent。
// agent 是会话级属性，切换立即写 transcript（运行中禁止）。
export default function AgentSelect() {
  const agents = useWorkbenchStore((state) => state.agents)
  const current = useWorkbenchStore((state) => state.agentKind)
  const activeId = useWorkbenchStore((state) => state.activeId)
  const running = useWorkbenchStore((state) => state.running)
  const setAgent = useWorkbenchStore((state) => state.setAgent)
  const [open, setOpen] = useState(false)

  const active = agents.find((agent) => agent.id === current)
  const label = active?.name ?? (current === 'benben' ? 'Benben Agent' : current)
  const disabled = !activeId || running

  const choose = (id: string) => {
    setOpen(false)
    if (id !== current) void setAgent(id)
  }

  return (
    <div className="relative shrink-0">
      <button
        type="button"
        data-testid="agent-select"
        disabled={disabled}
        onClick={() => setOpen((value) => !value)}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={`Agent：${label}`}
        title={disabled ? (running ? '运行中不能切换 agent' : '先新建或选择一个会话') : '切换会话使用的 agent'}
        className="flex cursor-pointer items-center gap-1.5 whitespace-nowrap rounded-full px-2 py-1 text-[11px] text-muted transition hover:bg-hover hover:text-fg disabled:cursor-not-allowed disabled:opacity-50"
      >
        {current === 'benben' ? <BenbenIcon /> : <ClaudeIcon />}
        <span className="font-medium text-fg">{label}</span>
        <ChevronIcon open={open} />
      </button>

      {open && !disabled && (
        <>
          <div
            className="fixed inset-0 z-10"
            onClick={() => setOpen(false)}
            aria-hidden="true"
          />
          <div
            role="menu"
            aria-label="选择 agent"
            data-testid="agent-menu"
            className="popover absolute bottom-full left-0 z-20 mb-2 w-72 space-y-0.5 rounded-xl border border-line-strong bg-panel p-1.5 shadow-pop"
          >
            {agents.length === 0 && (
              <p className="px-3 py-2 font-mono text-[10px] text-subtle">没有可选的 agent</p>
            )}
            {agents.map((agent) => {
              const selected = agent.id === current
              return (
                <button
                  key={agent.id}
                  type="button"
                  role="menuitemradio"
                  aria-checked={selected}
                  disabled={!agent.available}
                  data-testid={`agent-option-${agent.id}`}
                  onClick={() => choose(agent.id)}
                  className={`flex w-full cursor-pointer flex-col items-start gap-0.5 rounded-lg px-3 py-2 text-left transition ${
                    !agent.available
                      ? 'cursor-not-allowed opacity-50'
                      : selected
                        ? 'bg-surface shadow-card'
                        : 'hover:bg-hover'
                  }`}
                >
                  <span className="flex w-full items-center justify-between gap-2 text-[12px] font-medium">
                    <span className="flex min-w-0 items-center gap-1.5">
                      {agent.id === 'benben' ? <BenbenIcon /> : <ClaudeIcon />}
                      <span className="truncate text-fg">{agent.name}</span>
                    </span>
                    {selected && (
                      <span className="shrink-0 font-mono text-[10px] uppercase tracking-[0.14em] text-accent-text">
                        当前
                      </span>
                    )}
                  </span>
                  <span className="text-[11px] leading-relaxed text-subtle">
                    {agent.available ? agent.description || '外部 ACP agent' : `不可用：${agent.detail ?? '未知原因'}`}
                  </span>
                </button>
              )
            })}
          </div>
        </>
      )}
    </div>
  )
}
