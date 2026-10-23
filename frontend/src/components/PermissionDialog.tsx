import { useMemo, useState } from 'react'
import { stringifyJSON, truncate } from '../lib/format'
import { useWorkbenchStore } from '../stores/workbench'

const OPTION_LABEL: Record<string, string> = {
  allow_once: '允许一次',
  allow_always: '总是允许此命令',
  deny_once: '拒绝',
  deny_always: '总是拒绝此命令',
}

const FALLBACK_OPTIONS = [
  { id: 'allow_once', label: '允许一次' },
  { id: 'allow_always', label: '总是允许此命令' },
  { id: 'deny_once', label: '拒绝' },
]

// PermissionDialog 展示队首审批请求。没有确认按钮之外的关闭方式：
// 权限检查不得默认放行，用户必须显式选择。
export default function PermissionDialog() {
  const approvals = useWorkbenchStore((state) => state.approvals)
  const approve = useWorkbenchStore((state) => state.approve)
  const [submitting, setSubmitting] = useState(false)

  const approval = approvals[0]
  const inputText = useMemo(() => {
    if (!approval) return ''
    return truncate(stringifyJSON(approval.input) || '（无参数）', 4000)
  }, [approval])

  if (!approval) return null

  const options =
    approval.options.length > 0 ? approval.options : FALLBACK_OPTIONS

  const handle = (option: string) => {
    if (submitting) return
    setSubmitting(true)
    void approve(approval.requestId, option).finally(() => setSubmitting(false))
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-overlay p-6">
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="permission-title"
        className="w-full max-w-lg overflow-hidden rounded-xl border border-line-strong bg-panel shadow-2xl"
      >
        <div className="border-b border-line px-4 py-3">
          <h2 id="permission-title" className="text-sm font-medium text-fg">
            工具调用需要审批
          </h2>
          <p className="mt-1 text-[11px] text-subtle">
            {approvals.length > 1 ? `还有 ${approvals.length - 1} 个请求在排队` : '请选择本次调用的处理方式'}
          </p>
        </div>

        <div className="space-y-3 px-4 py-3">
          <div className="flex items-center gap-2">
            <span className="rounded bg-raised px-2 py-0.5 font-mono text-xs text-fg">
              {approval.toolName}
            </span>
            {approval.reason && <span className="text-[11px] text-subtle">{approval.reason}</span>}
          </div>

          <pre className="max-h-60 overflow-auto whitespace-pre-wrap break-words rounded-lg bg-code p-3 font-mono text-[11px] text-muted">
            {inputText}
          </pre>
          <p className="text-[11px] leading-relaxed text-subtle">
            「总是允许此命令」只对当前这条完整命令生效，后续不同命令仍会询问；需要完全停止审批请切换输入框左下角的权限模式。
          </p>
        </div>

        <div className="flex flex-wrap items-center justify-end gap-2 border-t border-line px-4 py-3">
          {options.map((option) => {
            const deny = option.id.startsWith('deny')
            const always = option.id.endsWith('_always')
            return (
              <button
                key={option.id}
                type="button"
                disabled={submitting}
                onClick={() => handle(option.id)}
                className={
                  deny
                    ? always
                      ? 'cursor-pointer rounded-lg border border-danger-line bg-danger-soft px-3 py-1.5 text-xs font-medium text-danger-text hover:border-danger disabled:cursor-not-allowed disabled:opacity-50'
                      : 'cursor-pointer rounded-lg bg-danger px-3 py-1.5 text-xs font-medium text-white hover:bg-danger-hover disabled:cursor-not-allowed disabled:opacity-50'
                    : always
                      ? 'cursor-pointer rounded-lg border border-success-line bg-success-soft px-3 py-1.5 text-xs font-medium text-success-text hover:border-success disabled:cursor-not-allowed disabled:opacity-50'
                      : 'cursor-pointer rounded-lg bg-success px-3 py-1.5 text-xs font-medium text-white hover:bg-success-hover disabled:cursor-not-allowed disabled:opacity-50'
                }
              >
                {OPTION_LABEL[option.id] ?? option.label}
              </button>
            )
          })}
        </div>
      </div>
    </div>
  )
}
