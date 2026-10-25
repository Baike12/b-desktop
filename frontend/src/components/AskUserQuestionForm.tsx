import { useEffect, useRef, useState } from 'react'

// AskUserQuestion 输入的结构（internal/tool/builtin/askuser.go 的 schema）。
export interface AskQuestionOption {
  label: string
  description: string
  preview?: string
}

export interface AskQuestion {
  question: string
  header: string
  options: AskQuestionOption[]
  multiSelect: boolean
}

export interface AskQuestionInput {
  questions: AskQuestion[]
}

type AskUserQuestionFormProps = {
  questions: AskQuestion[]
  disabled: boolean
  onSubmit: (answers: Record<string, string>, annotations: Record<string, { preview?: string }>) => void
  onDecline: () => void
}

// AskUserQuestionForm 渲染 AskUserQuestion 的问答卡片（参考终端实现的步进式布局）：
// 一次只显示一题，header 徽标 + 问题正文 + 「‹ n/N ›」翻页；
// 选项是编号行（粗体标签 + 行内说明），末行是自定义回答输入框；
// 底部左侧是操作提示，右侧是「忽略 / 提交」。数字键直接选择，Enter 进入下一题或提交。
export function AskUserQuestionForm({ questions, disabled, onSubmit, onDecline }: AskUserQuestionFormProps) {
  const [current, setCurrent] = useState(0)
  const [selected, setSelected] = useState<Record<string, string[]>>(() =>
    Object.fromEntries(questions.map((q) => [q.question, [] as string[]])),
  )
  const [custom, setCustom] = useState<Record<string, string>>(() =>
    Object.fromEntries(questions.map((q) => [q.question, ''])),
  )
  const containerRef = useRef<HTMLDivElement>(null)

  // 翻题后把焦点收回卡片容器：数字键 / 方向键 / Enter 始终作用于当前题，
  // 不依赖用户是否刚好点在选项上；不会抢输入框的焦点（仅题号变化时触发）。
  useEffect(() => {
    containerRef.current?.focus({ preventScroll: true })
  }, [current])

  const question = questions[current]
  const labels = selected[question.question] ?? []
  const customText = (custom[question.question] ?? '').trim()
  const answered = (q: AskQuestion): boolean =>
    (selected[q.question] ?? []).length > 0 || (custom[q.question] ?? '').trim() !== ''
  const canSubmit = questions.every(answered)

  const activePreview = (): string | undefined => {
    if (question.multiSelect || labels.length !== 1 || customText !== '') return undefined
    return question.options.find((option) => option.label === labels[0])?.preview
  }

  const toggle = (label: string) => {
    if (disabled) return
    setSelected((previous) => {
      const currentLabels = previous[question.question] ?? []
      const next = question.multiSelect
        ? currentLabels.includes(label)
          ? currentLabels.filter((item) => item !== label)
          : [...currentLabels, label]
        : [label]
      return { ...previous, [question.question]: next }
    })
    if (!question.multiSelect) {
      setCustom((previous) => ({ ...previous, [question.question]: '' }))
    }
  }

  const selectByIndex = (index: number) => {
    if (index >= 0 && index < question.options.length) {
      toggle(question.options[index].label)
    }
  }

  const changeCustom = (value: string) => {
    if (disabled) return
    setCustom((previous) => ({ ...previous, [question.question]: value }))
    if (!question.multiSelect && value.trim() !== '') {
      // 单选题输入自定义回答时清除选项选择：自定义回答优先。
      setSelected((previous) => ({ ...previous, [question.question]: [] }))
    }
  }

  const go = (delta: number) => {
    setCurrent((index) => Math.min(questions.length - 1, Math.max(0, index + delta)))
  }

  const submit = () => {
    if (disabled || !canSubmit) {
      // 跳到第一个未回答的题。
      const pending = questions.findIndex((q) => !answered(q))
      if (pending >= 0) setCurrent(pending)
      return
    }
    const answers: Record<string, string> = {}
    const annotations: Record<string, { preview?: string }> = {}
    for (const q of questions) {
      const picked = selected[q.question] ?? []
      const text = (custom[q.question] ?? '').trim()
      const answer = q.multiSelect ? [...picked, ...(text ? [text] : [])].join(', ') : text || picked[0]
      answers[q.question] = answer
      const preview = q.multiSelect || text ? undefined : q.options.find((option) => option.label === picked[0])?.preview
      if (preview) annotations[q.question] = { preview }
    }
    onSubmit(answers, annotations)
  }

  const handleKeyDown = (event: React.KeyboardEvent) => {
    if (disabled) return
    if (event.key >= '1' && event.key <= '9') {
      // 焦点在自定义输入框里时数字键正常输入，不当作选项序号。
      if (event.target instanceof HTMLInputElement) return
      selectByIndex(Number(event.key) - 1)
      event.preventDefault()
      return
    }
    if (event.key === 'ArrowLeft') {
      go(-1)
      event.preventDefault()
      return
    }
    if (event.key === 'ArrowRight') {
      go(1)
      event.preventDefault()
      return
    }
    if (event.key === 'Enter') {
      const target = event.target
      if (target instanceof HTMLInputElement && event.shiftKey) return
      if (target instanceof HTMLButtonElement && target.getAttribute('data-option') === null) {
        // 焦点在忽略 / 提交 / 翻页按钮上时 Enter 是按钮的激活键，交给按钮。
        return
      }
      // 选项按钮与输入框上：Enter 的语义是「确认并前进」，阻止按钮的原生
      // 激活（原生 click 会把刚选中的单选项反选）。
      if (!answered(question)) return
      if (current < questions.length - 1) {
        go(1)
      } else {
        submit()
      }
      event.preventDefault()
    }
  }

  const preview = activePreview()

  return (
    <div
      ref={containerRef}
      role="form"
      aria-label="AskUserQuestion"
      tabIndex={-1}
      onKeyDown={handleKeyDown}
      className="outline-none"
    >
      <div className="flex items-center justify-between gap-3 px-4 pt-3">
        <div className="flex min-w-0 items-baseline gap-2">
          <span className="shrink-0 rounded-full border border-line bg-raised px-2 py-0.5 font-mono text-[10px] uppercase tracking-[0.14em] text-subtle">
            {question.header}
          </span>
          {questions.length > 1 && question.multiSelect && (
            <span className="font-mono text-[10px] uppercase tracking-[0.14em] text-subtle">多选</span>
          )}
        </div>
        {questions.length > 1 && (
          <div className="flex shrink-0 items-center gap-1 font-mono text-[11px] text-subtle">
            <button
              type="button"
              disabled={disabled || current === 0}
              onClick={() => go(-1)}
              className="cursor-pointer px-1 transition hover:text-fg disabled:cursor-not-allowed disabled:opacity-40"
              aria-label="上一题"
            >
              ‹
            </button>
            <span className="tabular-nums">
              {current + 1}/{questions.length}
            </span>
            <button
              type="button"
              disabled={disabled || current === questions.length - 1}
              onClick={() => go(1)}
              className="cursor-pointer px-1 transition hover:text-fg disabled:cursor-not-allowed disabled:opacity-40"
              aria-label="下一题"
            >
              ›
            </button>
          </div>
        )}
      </div>

      <p className="px-4 pt-2 text-[13px] font-medium leading-relaxed text-fg">{question.question}</p>

      <div className="max-h-72 overflow-y-auto px-4 pb-1 pt-2">
        <ol className="space-y-0.5">
          {question.options.map((option, index) => {
            const checked = labels.includes(option.label)
            return (
              <li key={option.label}>
                <button
                  type="button"
                  role={question.multiSelect ? 'checkbox' : 'radio'}
                  aria-checked={checked}
                  data-option={option.label}
                  disabled={disabled}
                  onClick={() => toggle(option.label)}
                  className={
                    'block w-full cursor-pointer rounded-lg px-2.5 py-1.5 text-left transition disabled:cursor-not-allowed ' +
                    (checked ? 'bg-accent-soft ring-1 ring-accent/40' : 'hover:bg-hover')
                  }
                >
                  <span className="flex items-baseline gap-2">
                    <span className="w-4 shrink-0 text-right font-mono text-[11px] tabular-nums text-subtle">
                      {index + 1}.
                    </span>
                    <span className="text-[13px] font-medium text-fg">{option.label}</span>
                    <span className="min-w-0 truncate text-[12px] text-muted">{option.description}</span>
                  </span>
                </button>
              </li>
            )
          })}
          <li>
            <input
              type="text"
              value={custom[question.question] ?? ''}
              disabled={disabled}
              placeholder="输入你的回答…"
              onChange={(event) => changeCustom(event.target.value)}
              className={
                'w-full rounded-lg border border-dashed bg-surface px-2.5 py-1.5 text-[13px] text-fg outline-none transition placeholder:text-subtle ' +
                (customText !== '' ? 'border-accent ring-1 ring-accent/30' : 'border-line-strong hover:border-line-strong')
              }
            />
          </li>
        </ol>

        {preview && (
          <pre className="mt-2 max-h-40 overflow-auto whitespace-pre-wrap break-words rounded-lg border border-line bg-code p-3 font-mono text-[11px] leading-relaxed text-muted">
            {preview}
          </pre>
        )}
      </div>

      <div className="flex items-center justify-between gap-3 border-t border-line/70 px-4 py-2.5">
        <p className="min-w-0 truncate font-mono text-[10px] text-subtle">
          {questions.length > 1 ? '数字键或点击选择，← → 翻题，Enter 下一题' : '数字键或点击选择，Enter 提交'}
        </p>
        <div className="flex shrink-0 items-center gap-2">
          <button
            type="button"
            disabled={disabled}
            onClick={onDecline}
            className="cursor-pointer rounded-full border border-line bg-surface px-3.5 py-1 text-[11px] text-muted transition hover:border-line-strong hover:text-fg disabled:cursor-not-allowed disabled:opacity-50"
          >
            忽略
          </button>
          <button
            type="button"
            disabled={disabled}
            onClick={submit}
            className="cursor-pointer rounded-full bg-accent px-4 py-1.5 text-[11px] font-medium text-on-accent shadow-card transition hover:bg-accent-hover active:translate-y-px disabled:cursor-not-allowed disabled:bg-hover disabled:text-subtle disabled:shadow-none"
          >
            提交
          </button>
        </div>
      </div>
    </div>
  )
}
