import { create } from 'zustand'
import {
  applyTheme,
  readStoredTheme,
  THEME_STORAGE_KEY,
  type ThemeMode,
} from '../lib/theme'

const SIDEBAR_STORAGE_KEY = 'anc-dsk:sidebar-collapsed'

// readStoredSidebar 返回上次的折叠状态；读取失败时保持展开。
function readStoredSidebar(): boolean {
  try {
    return window.localStorage.getItem(SIDEBAR_STORAGE_KEY) === '1'
  } catch {
    return false
  }
}

interface SettingsState {
  theme: ThemeMode
  open: boolean
  sidebarCollapsed: boolean
  setTheme: (mode: ThemeMode) => void
  toggleSidebar: () => void
  openSettings: () => void
  closeSettings: () => void
}

// 设置只保存在本机 localStorage，不经过后端。
export const useSettingsStore = create<SettingsState>((set) => ({
  theme: readStoredTheme(),
  open: false,
  sidebarCollapsed: readStoredSidebar(),
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
  openSettings: () => set({ open: true }),
  closeSettings: () => set({ open: false }),
}))
