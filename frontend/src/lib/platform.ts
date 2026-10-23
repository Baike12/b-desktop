import type { CSSProperties } from 'react'

// Wails 通过 --wails-draggable 标记窗口拖动区域（见 mac.TitleBarHiddenInset）。
export const IS_MAC =
  typeof navigator !== 'undefined' && /mac/i.test(navigator.userAgent)

export const DRAG_REGION = { '--wails-draggable': 'drag' } as CSSProperties

// 拖动区域内的按钮必须显式退出拖动，否则 Wails 会把 mousedown 当成拖窗口。
export const NO_DRAG = { '--wails-draggable': 'no-drag' } as CSSProperties
