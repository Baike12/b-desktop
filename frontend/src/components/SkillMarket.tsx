import { useDeferredValue, useEffect, useState } from 'react'
import { ListSkills } from '../../wailsjs/go/app/App'
import type { app } from '../../wailsjs/go/models'
import { errorMessage } from '../stores/workbench'

// 技能市场：卡片网格展示本地已安装的 Skills，支持按名称过滤；不联网。

export interface SkillMarketProps {
  // onSelect 在点击卡片时触发，便于外部打开技能详情；不传时卡片只读。
  onSelect?: (name: string) => void
  className?: string
}

const SOURCE_LABEL: Record<string, string> = {
  user: '用户级',
  project: '项目级',
}

function sourceLabel(source: string): string {
  return SOURCE_LABEL[source] ?? (source || '未知')
}

function sourceBadgeClass(source: string): string {
  return source === 'project'
    ? 'border-accent bg-accent-soft text-accent-text'
    : 'border-line bg-raised text-muted'
}

interface SkillCardProps {
  skill: app.SkillDTO
  onSelect?: (name: string) => void
}

function SkillCard({ skill, onSelect }: SkillCardProps) {
  const body = (
    <>
      <span className="flex min-w-0 items-start justify-between gap-2">
        <span className="min-w-0 flex-1 truncate text-sm font-medium text-fg">{skill.name}</span>
        <span
          className={`shrink-0 rounded-full border px-1.5 py-0.5 text-[10px] ${sourceBadgeClass(skill.source)}`}
        >
          {sourceLabel(skill.source)}
        </span>
      </span>
      <span className="mt-1.5 line-clamp-3 text-[11px] leading-relaxed text-muted">
        {skill.description || '无描述'}
      </span>
    </>
  )

  if (!onSelect) {
    return (
      <div className="flex h-full w-full flex-col rounded-lg border border-line bg-surface p-3">
        {body}
      </div>
    )
  }

  return (
    <button
      type="button"
      onClick={() => onSelect(skill.name)}
      title={`查看 ${skill.name}`}
      className="flex h-full w-full cursor-pointer flex-col rounded-lg border border-line bg-surface p-3 text-left transition hover:border-line-strong hover:bg-hover"
    >
      {body}
    </button>
  )
}

// SkillMarket 只读展示 ListSkills 的结果；过滤是渲染期派生值，不触发额外请求。
export default function SkillMarket({ onSelect, className = '' }: SkillMarketProps) {
  const [skills, setSkills] = useState<app.SkillDTO[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [query, setQuery] = useState('')
  const deferredQuery = useDeferredValue(query)

  useEffect(() => {
    let alive = true
    void ListSkills()
      .then((list) => {
        if (!alive) return
        setSkills(list)
      })
      .catch((err) => {
        if (!alive) return
        setError(errorMessage(err))
      })
      .finally(() => {
        if (alive) setLoading(false)
      })
    return () => {
      alive = false
    }
  }, [])

  const keyword = deferredQuery.trim().toLowerCase()
  const filtered =
    keyword === ''
      ? skills
      : skills.filter((skill) => skill.name.toLowerCase().includes(keyword))

  return (
    <section
      data-testid="skill-market"
      aria-label="技能市场"
      className={`flex h-full min-h-0 flex-col overflow-hidden rounded-xl border border-line bg-panel ${className}`}
    >
      <header className="flex items-start justify-between gap-3 border-b border-line px-4 py-3">
        <div>
          <h2 className="text-sm font-semibold text-fg">技能市场</h2>
          <p className="mt-0.5 text-[11px] text-subtle">本地已安装的 Skills，按名称过滤</p>
        </div>
        <span className="shrink-0 rounded-full border border-line bg-raised px-2 py-0.5 text-[10px] tabular-nums text-muted">
          {skills.length} 个技能
        </span>
      </header>

      <div className="border-b border-line px-4 py-2.5">
        <input
          type="search"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          placeholder="按名称过滤…"
          aria-label="按名称过滤技能"
          className="w-full rounded-lg border border-line bg-surface px-3 py-1.5 text-xs text-fg outline-none transition placeholder:text-subtle focus:border-accent"
        />
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto px-4 py-3">
        {error && (
          <div className="mb-3 whitespace-pre-wrap break-words rounded-lg border border-danger-line bg-danger-soft px-3 py-2 text-[11px] leading-relaxed text-danger-text">
            {error}
          </div>
        )}

        {loading ? (
          <p className="py-10 text-center text-xs text-subtle">正在读取技能…</p>
        ) : filtered.length === 0 ? (
          <div className="py-10 text-center">
            <p className="text-xs text-muted">
              {skills.length === 0 ? '还没有安装技能' : '没有匹配的技能'}
            </p>
            <p className="mt-1 text-[11px] leading-relaxed text-subtle">
              {skills.length === 0
                ? '把 SKILL.md 放进 ~/.b-desktop/skills 或 <项目>/.b-desktop/skills'
                : '换个名称关键字试试'}
            </p>
          </div>
        ) : (
          <ul className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
            {filtered.map((skill) => (
              <li key={skill.name} className="min-w-0">
                <SkillCard skill={skill} onSelect={onSelect} />
              </li>
            ))}
          </ul>
        )}
      </div>
    </section>
  )
}
