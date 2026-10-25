import { memo, useState } from 'react'
import { stringifyJSON, summarizeInput, truncate } from '../lib/format'
import type { AssistantBlock, ToolStatus } from '../types/chat'
import { DiffView } from './DiffView'
import MascotPose, { mascotPoseForTool } from './MascotPose'

type ToolBlock = Extract<AssistantBlock, { kind: 'tool' }>

type DiffSpec = { diff: string } | { oldText: string; newText: string }

interface ToolDiff {
  path: string
  spec: DiffSpec
}

const EDIT_TOOLS = new Set(['edit', 'write', 'multiedit', 'notebookedit'])

const ACTIVE_STATUSES = new Set<ToolStatus>(['pending', 'running', 'waiting'])

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

const STATUS_META: Record<ToolStatus, { label: string; text: string; dot: string }> = {
  pending: { label: '准备中', text: 'text-subtle', dot: 'bg-subtle' },
  running: { label: '执行中', text: 'text-accent-text', dot: 'bg-accent' },
  waiting: { label: '待审批', text: 'text-warn-text', dot: 'bg-warn-text' },
  success: { label: '完成', text: 'text-subtle', dot: 'bg-success' },
  error: { label: '失败', text: 'text-danger-text', dot: 'bg-danger' },
  denied: { label: '已拒绝', text: 'text-danger-text', dot: 'bg-danger' },
}

// ToolIcon 按工具名给出类别图标，让摘要行在不加卡片的情况下仍可快速扫读。
function ToolIcon({ name }: { name: string }) {
  const normalized = name.toLowerCase()
  const className = 'size-3.5 shrink-0 text-subtle'
  const common = {
    viewBox: '0 0 24 24',
    fill: 'none',
    stroke: 'currentColor',
    strokeWidth: 1.6,
    strokeLinecap: 'round' as const,
    strokeLinejoin: 'round' as const,
    className,
    'aria-hidden': true,
  }
  if (/(bash|shell|terminal|powershell)/.test(normalized)) {
    return (
      <svg {...common}>
        <path d="m4 7 5 5-5 5M11 17h9" />
      </svg>
    )
  }
  if (/(read|write|edit|notebook)/.test(normalized)) {
    return (
      <svg {...common}>
        <path d="M14 3H6a1 1 0 0 0-1 1v16a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V8Z" />
        <path d="M14 3v5h5M8 13h8M8 17h5" />
      </svg>
    )
  }
  if (/(grep|glob|search)/.test(normalized)) {
    return (
      <svg {...common}>
        <circle cx="11" cy="11" r="7" />
        <path d="m20 20-3.5-3.5" />
      </svg>
    )
  }
  if (/(webfetch|websearch|fetch|http)/.test(normalized)) {
    return (
      <svg {...common}>
        <circle cx="12" cy="12" r="9" />
        <path d="M3 12h18M12 3c2.6 2.5 4 5.7 4 9s-1.4 6.5-4 9c-2.6-2.5-4-5.7-4-9s1.4-6.5 4-9Z" />
      </svg>
    )
  }
  if (/(agent|task|team)/.test(normalized)) {
    return (
      <svg {...common}>
        <rect x="5" y="8" width="14" height="11" />
        <path d="M12 8V4M9 13h.01M15 13h.01M9 16h6" />
      </svg>
    )
  }
  if (/todo/.test(normalized)) {
    return (
      <svg {...common}>
        <path d="M9 6h11M9 12h11M9 18h11M4 6h.01M4 12h.01M4 18h.01" />
      </svg>
    )
  }
  return (
    <svg {...common}>
      <path d="M14.7 6.3a4 4 0 0 0-5.4 5.4L4 17l3 3 5.3-5.3a4 4 0 0 0 5.4-5.4l-2.6 2.6-2.1-2.1Z" />
    </svg>
  )
}

// ToolCallCard 展示一次工具调用：单行摘要，悬停时抬起为软卡片；展开后显示参数与结果。
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
  const active = ACTIVE_STATUSES.has(block.status)

  return (
    <div className="group/tool -mx-2 min-w-0 rounded-xl px-2 py-1 transition hover:bg-raised/60">
      <button
        type="button"
        onClick={() => setOpen((value) => !value)}
        className="flex w-full min-w-0 cursor-pointer items-center gap-2 rounded-lg py-1 text-left"
        aria-expanded={open}
      >
        <span
          data-testid={active ? 'tool-mascot' : undefined}
          className="flex size-8 shrink-0 items-center justify-center"
        >
          {active ? (
            <MascotPose pose={mascotPoseForTool(block.toolName)} className="h-8" />
          ) : (
            <ToolIcon name={block.toolName} />
          )}
        </span>
        <span className="shrink-0 font-mono text-[12px] text-fg">{block.toolName}</span>
        <span
          className={`flex shrink-0 items-center gap-1 font-mono text-[10px] tracking-[0.1em] ${meta.text}`}
        >
          <span
            className={`size-1.5 rounded-full ${meta.dot} ${block.status === 'running' ? 'animate-pulse' : ''}`}
          />
          {meta.label}
        </span>
        <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-subtle group-hover/tool:text-muted">
          {summary}
        </span>
        <svg
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="2"
          strokeLinecap="round"
          strokeLinejoin="round"
          className={`size-3 shrink-0 text-subtle transition-transform ${open ? 'rotate-90' : ''}`}
          aria-hidden="true"
        >
          <path d="m9 18 6-6-6-6" />
        </svg>
      </button>

      {block.status === 'running' && block.progress && (
        <p className="mt-0.5 pl-6 font-mono text-[11px] text-accent-text">
          {truncate(block.progress, 200)}
        </p>
      )}

      {open && (
        <div className="mb-1 ml-1 mt-1.5 space-y-3 rounded-xl border border-line/80 bg-panel px-3 py-2.5">
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
              <pre className="max-h-56 overflow-auto whitespace-pre-wrap break-words rounded-lg border border-line bg-code p-2 font-mono text-[11px] text-muted">
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
              {block.result.images && block.result.images.length > 0 && (
                <div className="mb-2 flex flex-wrap gap-2">
                  {block.result.images.map((img, index) => (
                    <img
                      key={`${index}-${img.data.length}`}
                      src={`data:${img.mediaType || 'image/png'};base64,${img.data}`}
                      alt={`工具返回的图片 ${index + 1}`}
                      className="max-h-48 rounded-md border border-line/80 object-contain"
                    />
                  ))}
                </div>
              )}
              <pre
                className={
                  block.result.isError
                    ? 'max-h-56 overflow-auto whitespace-pre-wrap break-words rounded-lg border border-danger-line bg-danger-soft p-2 font-mono text-[11px] text-danger-text'
                    : 'max-h-56 overflow-auto whitespace-pre-wrap break-words rounded-lg border border-line bg-code p-2 font-mono text-[11px] text-muted'
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
