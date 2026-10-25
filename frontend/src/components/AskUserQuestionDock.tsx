import { useMemo } from 'react'
import { characterName } from '../lib/character'
import { useSettingsStore } from '../stores/settings'
import { useWorkbenchStore } from '../stores/workbench'
import { AskUserQuestionForm } from './AskUserQuestionForm'
import type { AskQuestion, AskQuestionInput } from './AskUserQuestionForm'
import MascotPose from './MascotPose'

// AskUserQuestionDock 把 AskUserQuestion 的审批卡片停靠在输入框正上方
// （与 Composer 同宽对齐），代替全屏居中弹窗。其余工具的审批仍走
// PermissionDialog 的居中弹窗。
export default function AskUserQuestionDock() {
  const approvals = useWorkbenchStore((state) => state.approvals)
  const approve = useWorkbenchStore((state) => state.approve)
  const character = useSettingsStore((state) => state.character)
  const mascotName = characterName(character)

  const ask = approvals.find((item) => item.toolName === 'AskUserQuestion')
  const questions = useMemo<AskQuestion[]>(() => {
    if (!ask) return []
    const input = ask.input as AskQuestionInput | null
    if (!input || !Array.isArray(input.questions)) return []
    return input.questions
  }, [ask])

  if (!ask || questions.length === 0) return null

  const handle = (answers: Record<string, string>, annotations: Record<string, { preview?: string }>) => {
    const updatedInput = { ...(ask.input as object), answers, annotations }
    void approve(ask.requestId, 'allow_once', updatedInput)
  }

  return (
    <div className="shrink-0 px-5 pt-2">
      <div
        role="dialog"
        aria-label={`${mascotName}需要你的决定`}
        className="mx-auto w-full max-w-[46rem] overflow-hidden rounded-2xl border border-line-strong bg-panel shadow-pop"
      >
        <div className="flex items-center justify-between gap-3 border-b border-line/70 px-4 py-2.5">
          <div className="min-w-0">
            <h2 className="text-[12px] font-medium text-fg">{mascotName}需要你的决定</h2>
            <p className="mt-0.5 font-mono text-[10px] uppercase tracking-[0.14em] text-subtle">
              请回答以下问题
            </p>
          </div>
          <span data-testid="ask-mascot" className="flex shrink-0">
            <MascotPose pose="question" className="h-12" />
          </span>
        </div>
        <AskUserQuestionForm
          key={ask.requestId}
          questions={questions}
          disabled={false}
          onSubmit={handle}
          onDecline={() => void approve(ask.requestId, 'deny_once')}
        />
      </div>
    </div>
  )
}
