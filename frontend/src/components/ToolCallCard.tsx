import { memo, useState } from 'react'
import { stringifyJSON, summarizeInput, truncate } from '../lib/format'
import type { AssistantBlock, ToolStatus } from '../types/chat'
import { DiffView } from './DiffView'

type ToolBlock = Extract<AssistantBlock, { kind: 'tool' }>

type DiffSpec = { diff: string } | { oldText: string; newText: string }

interface ToolDiff {
  path: string
  spec: DiffSpec
}

const EDIT_TOOLS = new Set(['edit', 'write', 'multiedit', 'notebookedit'])

function readString(record: Record<string, unknown>, keys: readonly string[]): string | undefined {
  for (const key of keys) {
    const value = record[key]
    if (typeof value === 'string') return value
  }
  return undefined
}

// toolDiff 从 Edit/Write 参数或结果预览中提取可展示的差异：
// 优先用真正的 unified diff 字段，拿不到就用 old_string/new_string 做行级对比。
function toolDiff(block: ToolBlock): ToolDiff | null {
  if (!EDIT_TOOLS.has(block.toolName.toLowerCase())) return null

  const input =
    block.input !== null && typeof block.input === 'object'
      ? (block.input as Record<string, unknown>)
      : null
  const path =
    (input && readString(input, ['file_path', 'filePath', 'path', 'notebook_path'])) ?? ''

  if (input) {
    const unified = readString(input, ['diff', 'patch', 'unified_diff'])
    if (unified && unified.trim() !== '') return { path, spec: { diff: unified } }

    const oldText = readString(input, ['old_string', 'oldText', 'old_str'])
    const newText = readString(input, ['new_string', 'newText', 'new_str'])
    if (oldText !== undefined || newText !== undefined) {
      return { path, spec: { oldText: oldText ?? '', newText: newText ?? '' } }
    }

    const content = readString(input, ['content', 'text', 'file_text'])
    if (content !== undefined) {
      return { path, spec: { oldText: '', newText: content } }
    }
  }

  // 部分后端会把 diff 放在工具结果预览里。
  const preview = block.result?.preview?.trim() ?? ''
  if (preview.startsWith('diff --git') || preview.startsWith('--- ') || preview.includes('\n@@ ')) {
    return { path, spec: { diff: preview } }
  }
  return null
}

const STATUS_META: Record<ToolStatus, { label: string; className: string; dot: string }> = {
  pending: {
    label: '参数流式',
    className: 'border-line bg-raised text-muted',
    dot: 'bg-subtle',
  },
  running: {
    label: '执行中',
    className: 'border-accent/40 bg-accent-soft text-accent-text',
    dot: 'bg-accent',
  },
  waiting: {
    label: '待审批',
    className: 'border-warn-line bg-warn-soft text-warn-text',
    dot: 'bg-warn-text',
  },
  success: {
    label: '完成',
    className: 'border-success-line bg-success-soft text-success-text',
    dot: 'bg-success',
  },
  error: {
    label: '失败',
    className: 'border-danger-line bg-danger-soft text-danger-text',
    dot: 'bg-danger',
  },
  denied: {
    label: '已拒绝',
    className: 'border-danger-line bg-danger-soft text-danger-text',
    dot: 'bg-danger',
  },
}

// ToolCallCard 展示一次工具调用：工具名、状态、参数摘要，展开后看完整参数与结果预览。
export const ToolCallCard = memo(function ToolCallCard({ block }: { block: ToolBlock }) {
  const [open, setOpen] = useState(false)
  const meta = STATUS_META[block.status]
  const summary =
    block.input !== undefined
      ? summarizeInput(block.input)
      : block.inputText
        ? `${block.inputText.length} 字符参数正在流式接收…`
        : ''

  const inputDetail = block.input !== undefined ? stringifyJSON(block.input) : block.inputText
  const resultPreview = block.result?.preview ?? ''
  const diff = toolDiff(block)

  return (
    <div className="border border-line bg-panel">
      <button
        type="button"
        onClick={() => setOpen((value) => !value)}
        className="flex w-full cursor-pointer items-center gap-2 px-3 py-2 text-left transition hover:bg-hover"
        aria-expanded={open}
      >
        <span className={`size-1.5 shrink-0 ${meta.dot}`} />
        <span className="shrink-0 font-mono text-[12px] text-fg">{block.toolName}</span>
        <span
          className={`shrink-0 border px-1.5 py-0.5 font-mono text-[10px] uppercase tracking-[0.14em] ${meta.className}`}
        >
          {meta.label}
        </span>
        <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-subtle">
          {summary}
        </span>
        <span className="shrink-0 font-mono text-[10px] uppercase tracking-[0.14em] text-subtle">
          {open ? '收起' : '详情'}
        </span>
      </button>

      {block.status === 'running' && block.progress && (
        <p className="border-t border-line px-3 py-1.5 font-mono text-[11px] text-accent-text">
          {truncate(block.progress, 200)}
        </p>
      )}

      {open && (
        <div className="space-y-3 border-t border-line px-3 py-2.5">
          {diff && (
            <div>
              <p className="mb-1 font-mono text-[10px] uppercase tracking-[0.14em] text-subtle">
                差异
              </p>
              {'diff' in diff.spec ? (
                <DiffView diff={diff.spec.diff} title={diff.path} maxHeightClass="max-h-72" />
              ) : (
                <DiffView
                  oldText={diff.spec.oldText}
                  newText={diff.spec.newText}
                  title={diff.path}
                  maxHeightClass="max-h-72"
                  emptyLabel="old_string 与 new_string 相同"
                />
              )}
            </div>
          )}
          {inputDetail && (
            <div>
              <p className="mb-1 font-mono text-[10px] uppercase tracking-[0.14em] text-subtle">
                参数
              </p>
              <pre className="max-h-56 overflow-auto whitespace-pre-wrap break-words border border-line bg-code p-2 font-mono text-[11px] text-muted">
                {truncate(inputDetail, 4000)}
              </pre>
            </div>
          )}
          {block.result && (
            <div>
              <p className="mb-1 flex items-center gap-2 font-mono text-[10px] uppercase tracking-[0.14em] text-subtle">
                结果
                {block.result.truncated && (
                  <span className="normal-case text-warn-text">
                    已截断 · {block.result.bytes} 字节
                  </span>
                )}
              </p>
              <pre
                className={
                  block.result.isError
                    ? 'max-h-56 overflow-auto whitespace-pre-wrap break-words border border-danger-line bg-danger-soft p-2 font-mono text-[11px] text-danger-text'
                    : 'max-h-56 overflow-auto whitespace-pre-wrap break-words border border-line bg-code p-2 font-mono text-[11px] text-muted'
                }
              >
                {truncate(resultPreview, 4000) || '（空结果）'}
              </pre>
            </div>
          )}
        </div>
      )}
    </div>
  )
})
