import {
  WindowSetDarkTheme,
  WindowSetLightTheme,
  WindowSetSystemDefaultTheme,
} from '../../wailsjs/runtime/runtime'

export type ThemeMode = 'light' | 'dark' | 'system'
export type ResolvedTheme = 'light' | 'dark'

export const THEME_STORAGE_KEY = 'anc-dsk:theme'

const THEME_MODES = new Set<string>(['light', 'dark', 'system'])

// readStoredTheme 读取本机保存的主题；没有记录时跟随系统。
export function readStoredTheme(): ThemeMode {
  try {
    const value = window.localStorage.getItem(THEME_STORAGE_KEY)
    if (value && THEME_MODES.has(value)) return value as ThemeMode
  } catch {
    // localStorage 不可用时退回跟随系统，不影响渲染。
  }
  return 'system'
}

// systemTheme 读取系统当前的明暗偏好。
export function systemTheme(): ResolvedTheme {
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

// resolveTheme 把三档偏好收敛为实际生效的一档。
export function resolveTheme(mode: ThemeMode): ResolvedTheme {
  return mode === 'system' ? systemTheme() : mode
}

// applyTheme 把主题写到 <html data-theme>，并同步原生窗口外观。
// 原生窗口外观会决定 macOS 红绿灯与标题栏的明暗。
export function applyTheme(mode: ThemeMode): ResolvedTheme {
  const resolved = resolveTheme(mode)
  document.documentElement.dataset.theme = resolved
  document.documentElement.style.colorScheme = resolved
  syncNativeTheme(mode)
  return resolved
}

function syncNativeTheme(mode: ThemeMode): void {
  const runtime = (window as { runtime?: unknown }).runtime
  if (!runtime) return
  try {
    if (mode === 'light') WindowSetLightTheme()
    else if (mode === 'dark') WindowSetDarkTheme()
    else WindowSetSystemDefaultTheme()
  } catch {
    // 非 Wails 环境（如纯浏览器预览）没有窗口 API，忽略即可。
  }
}
