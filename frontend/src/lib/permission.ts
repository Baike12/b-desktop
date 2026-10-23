import type { PermissionMode } from '../types/agent'

export interface PermissionModeMeta {
  id: PermissionMode
  label: string
  hint: string
}

// 顺序与 internal/permission 的五个 Mode 一致。
export const PERMISSION_MODES: readonly PermissionModeMeta[] = [
  { id: 'default', label: '默认', hint: '每次调用都询问（只读工具自动放行）' },
  { id: 'acceptEdits', label: '接受编辑', hint: '自动接受工作目录内的文件编辑' },
  { id: 'plan', label: '计划', hint: '禁止一切副作用，仅允许只读操作' },
  { id: 'bypassPermissions', label: '全放行', hint: '放行全部调用（硬拒绝规则除外）' },
  { id: 'dontAsk', label: '不问', hint: '需要审批的调用一律拒绝' },
]

const MODE_IDS = new Set<string>(PERMISSION_MODES.map((mode) => mode.id))

// normalizePermissionMode 把后端字符串收敛到五档；未知值按 default 展示。
export function normalizePermissionMode(value: string | undefined): PermissionMode {
  return value && MODE_IDS.has(value) ? (value as PermissionMode) : 'default'
}

// permissionModeLabel 返回中文标签。
export function permissionModeLabel(value: PermissionMode): string {
  return PERMISSION_MODES.find((mode) => mode.id === value)?.label ?? value
}
