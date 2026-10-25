import { Fragment, useEffect, useLayoutEffect, useRef, useState } from 'react'

// ContextMenu 是轻量的右键菜单：固定定位、点击外部或 Esc 关闭。
// 不引入第三方菜单库，桌面端只需要几个固定项。

export interface MenuItem {
  // id 是菜单项的稳定标识，用于 React key 与自动化测试。
  id: string
  // label 是菜单文字。
  label: string
  // danger 为 true 时用危险色文字。
  danger?: boolean
  // disabled 为 true 时不可点击。
  disabled?: boolean
  // separatorBefore 为 true 时在菜单项前画一条分隔线。
  separatorBefore?: boolean
  // keepOpen 为 true 时点击后菜单保持打开（用于二次确认类操作）。
  keepOpen?: boolean
  // onSelect 是点击后的动作。
  onSelect: () => void
}

export interface ContextMenuProps {
  // x/y 是菜单左上角在视口坐标系里的位置。
  x: number
  y: number
  // items 是菜单项列表。
  items: MenuItem[]
  // onClose 在菜单关闭时调用（选中一项后也会调用）。
  onClose: () => void
}

const MENU_WIDTH = 188

export default function ContextMenu({ x, y, items, onClose }: ContextMenuProps) {
  const ref = useRef<HTMLDivElement>(null)
  const [position, setPosition] = useState({ left: x, top: y })

  // 贴近视口右/下边缘时向内翻转，避免菜单被裁掉。
  useLayoutEffect(() => {
    const element = ref.current
    if (!element) return
    const rect = element.getBoundingClientRect()
    let left = x
    let top = y
    if (left + rect.width > window.innerWidth - 8) {
      left = Math.max(8, window.innerWidth - rect.width - 8)
    }
    if (top + rect.height > window.innerHeight - 8) {
      top = Math.max(8, window.innerHeight - rect.height - 8)
    }
    setPosition({ left, top })
  }, [x, y, items.length])

  useEffect(() => {
    const onPointerDown = (event: PointerEvent) => {
      if (ref.current && !ref.current.contains(event.target as Node)) onClose()
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose()
    }
    window.addEventListener('pointerdown', onPointerDown, true)
    window.addEventListener('keydown', onKeyDown)
    return () => {
      window.removeEventListener('pointerdown', onPointerDown, true)
      window.removeEventListener('keydown', onKeyDown)
    }
  }, [onClose])

  return (
    <div
      ref={ref}
      role="menu"
      data-testid="context-menu"
      style={{ left: position.left, top: position.top, minWidth: MENU_WIDTH }}
      className="popover fixed z-50 overflow-hidden rounded-xl border border-line-strong bg-raised p-1 shadow-pop"
    >
      {items.map((item, index) => (
        <Fragment key={item.id}>
          {item.separatorBefore && index > 0 && <div className="my-1 h-px bg-line" />}
          <button
            type="button"
            role="menuitem"
            data-testid={`context-menu-${item.id}`}
            disabled={item.disabled}
            onClick={() => {
              if (item.disabled) return
              item.onSelect()
              if (!item.keepOpen) onClose()
            }}
            className={
              item.disabled
                ? 'block w-full cursor-not-allowed rounded-lg px-3 py-1.5 text-left text-[11px] text-subtle'
                : item.danger
                  ? 'block w-full cursor-pointer rounded-lg px-3 py-1.5 text-left text-[11px] text-danger-text hover:bg-danger-soft'
                  : 'block w-full cursor-pointer rounded-lg px-3 py-1.5 text-left text-[11px] text-muted hover:bg-hover hover:text-fg'
            }
          >
            {item.label}
          </button>
        </Fragment>
      ))}
    </div>
  )
}
