import { useState } from 'react'
import { PERMISSION_MODES } from '../lib/permission'
import { useWorkbenchStore } from '../stores/workbench'
import type { PermissionMode } from '../types/agent'

function ShieldIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-3.5 shrink-0 text-warn-text"
      aria-hidden="true"
    >
      <path d="M12 3 5 5.5v5.2c0 4.3 2.9 7.8 7 9.3 4.1-1.5 7-5 7-9.3V5.5L12 3Z" />
      <path d="M12 9v4" />
      <path d="M12 16h.01" />
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

// PermissionModeSelect 是输入框内的权限模式切换器。
// 模式属于当前会话：切换立即对后续工具调用生效，并写入 transcript。
export default function PermissionModeSelect() {
  const current = useWorkbenchStore((state) => state.permissionMode)
  const activeId = useWorkbenchStore((state) => state.activeId)
  const setPermissionMode = useWorkbenchStore((state) => state.setPermissionMode)
  const [open, setOpen] = useState(false)

  const active = PERMISSION_MODES.find((mode) => mode.id === current)
  const label = active?.label ?? current
  const hint = active?.hint ?? ''
  const disabled = !activeId

  const choose = (mode: PermissionMode) => {
    setOpen(false)
    if (mode !== current) void setPermissionMode(mode)
  }

  return (
    <div className="relative shrink-0">
      <button
        type="button"
        disabled={disabled}
        onClick={() => setOpen((value) => !value)}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={`权限模式：${label}`}
        title={disabled ? '先新建或选择一个会话' : hint}
        className="flex cursor-pointer items-center gap-1.5 whitespace-nowrap px-1.5 py-1 text-[11px] text-muted transition hover:bg-hover hover:text-fg disabled:cursor-not-allowed disabled:opacity-50"
      >
        <ShieldIcon />
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
            aria-label="选择权限模式"
            className="absolute bottom-full left-0 z-20 mb-2 w-72 border border-line-strong bg-panel"
          >
            {PERMISSION_MODES.map((mode) => {
              const selected = mode.id === current
              return (
                <button
                  key={mode.id}
                  type="button"
                  role="menuitemradio"
                  aria-checked={selected}
                  onClick={() => choose(mode.id)}
                  className={`flex w-full cursor-pointer flex-col items-start gap-0.5 border-b border-line px-3 py-2 text-left transition last:border-b-0 ${ selected ? 'bg-warn-soft' : 'hover:bg-hover' }`}
                >
                  <span className="flex w-full items-center justify-between gap-2 text-[12px] font-medium">
                    <span className={selected ? 'text-warn-text' : 'text-fg'}>{mode.label}</span>
                    {selected && (
                      <span className="font-mono text-[10px] uppercase tracking-[0.14em] text-warn-text">
                        当前
                      </span>
                    )}
                  </span>
                  <span className="text-[11px] leading-relaxed text-subtle">{mode.hint}</span>
                </button>
              )
            })}
          </div>
        </>
      )}
    </div>
  )
}
