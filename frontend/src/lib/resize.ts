// 分栏手柄的阴影只跟随指针附近的纵向位置，避免整条分界线发暗。
export function trackResizeShadow(handle: HTMLElement, clientY: number) {
  const rect = handle.getBoundingClientRect()
  const y = Math.min(rect.height, Math.max(0, clientY - rect.top))
  handle.style.setProperty('--resize-y', `${y}px`)
}
