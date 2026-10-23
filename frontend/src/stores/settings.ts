import { create } from 'zustand'
import {
  applyTheme,
  readStoredTheme,
  THEME_STORAGE_KEY,
  type ThemeMode,
} from '../lib/theme'

const SIDEBAR_STORAGE_KEY = 'b-desktop:sidebar-collapsed'
const RIGHT_SIDEBAR_STORAGE_KEY = 'b-desktop:right-sidebar-open'
const RIGHT_SIDEBAR_WIDTH_STORAGE_KEY = 'b-desktop:right-sidebar-width'

export const RIGHT_SIDEBAR_DEFAULT_WIDTH = 300
export const RIGHT_SIDEBAR_MIN_WIDTH = 240
export const RIGHT_SIDEBAR_MAX_WIDTH = 560

// readStoredSidebar 返回上次的折叠状态；读取失败时保持展开。
function readStoredSidebar(): boolean {
  try {
    return window.localStorage.getItem(SIDEBAR_STORAGE_KEY) === '1'
  } catch {
    return false
  }
}

// readStoredRightSidebar 返回右侧文件栏的开关；默认展开（Zed 的默认行为）。
function readStoredRightSidebar(): boolean {
  try {
    return window.localStorage.getItem(RIGHT_SIDEBAR_STORAGE_KEY) !== '0'
  } catch {
    return true
  }
}

// readStoredRightWidth 返回上次的宽度，越界值回落到默认值。
function readStoredRightWidth(): number {
  try {
    const raw = Number(window.localStorage.getItem(RIGHT_SIDEBAR_WIDTH_STORAGE_KEY))
    if (Number.isFinite(raw) && raw >= RIGHT_SIDEBAR_MIN_WIDTH && raw <= RIGHT_SIDEBAR_MAX_WIDTH) {
      return raw
    }
  } catch {
    // 读取失败时使用默认宽度。
  }
  return RIGHT_SIDEBAR_DEFAULT_WIDTH
}

function clampRightWidth(width: number): number {
  return Math.min(RIGHT_SIDEBAR_MAX_WIDTH, Math.max(RIGHT_SIDEBAR_MIN_WIDTH, Math.round(width)))
}

interface SettingsState {
  theme: ThemeMode
  open: boolean
  sidebarCollapsed: boolean
  rightSidebarOpen: boolean
  rightSidebarWidth: number
  setTheme: (mode: ThemeMode) => void
  toggleSidebar: () => void
  toggleRightSidebar: () => void
  setRightSidebarOpen: (open: boolean) => void
  setRightSidebarWidth: (width: number) => void
  persistRightSidebarWidth: () => void
  openSettings: () => void
  closeSettings: () => void
}

// 设置只保存在本机 localStorage，不经过后端。
export const useSettingsStore = create<SettingsState>((set, get) => ({
  theme: readStoredTheme(),
  open: false,
  sidebarCollapsed: readStoredSidebar(),
  rightSidebarOpen: readStoredRightSidebar(),
  rightSidebarWidth: readStoredRightWidth(),
  setTheme: (theme) => {
    try {
      window.localStorage.setItem(THEME_STORAGE_KEY, theme)
    } catch {
      // 存储失败只影响下次启动的默认值，本次切换仍然生效。
    }
    applyTheme(theme)
    set({ theme })
  },
  toggleSidebar: () =>
    set((state) => {
      const next = !state.sidebarCollapsed
      try {
        window.localStorage.setItem(SIDEBAR_STORAGE_KEY, next ? '1' : '0')
      } catch {
        // 同上：持久化失败不影响本次折叠。
      }
      return { sidebarCollapsed: next }
    }),
  toggleRightSidebar: () => get().setRightSidebarOpen(!get().rightSidebarOpen),
  setRightSidebarOpen: (open) => {
    try {
      window.localStorage.setItem(RIGHT_SIDEBAR_STORAGE_KEY, open ? '1' : '0')
    } catch {
      // 持久化失败不影响本次开关。
    }
    set({ rightSidebarOpen: open })
  },
  setRightSidebarWidth: (width) => set({ rightSidebarWidth: clampRightWidth(width) }),
  persistRightSidebarWidth: () => {
    try {
      window.localStorage.setItem(
        RIGHT_SIDEBAR_WIDTH_STORAGE_KEY,
        String(get().rightSidebarWidth),
      )
    } catch {
      // 同上：持久化失败不影响本次宽度。
    }
  },
  openSettings: () => set({ open: true }),
  closeSettings: () => set({ open: false }),
}))
