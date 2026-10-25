import { SetDockIcon } from '../../wailsjs/go/app/App'
import type { CharacterId } from './character'

// syncDockIcon 把当前角色同步给 macOS Dock；非 Wails 环境没有绑定，直接跳过。
export function syncDockIcon(character: CharacterId): void {
  try {
    void SetDockIcon(character).catch(() => {})
  } catch {
    // 浏览器 harness 或纯预览页面没有 window.go，不影响界面。
  }
}
