import { useCallback, useEffect, useState } from 'react'
import {
  CreateSkill,
  DeleteSkill,
  GetSkillDetail,
  ListSkills,
  ReloadSkills,
  UpdateSkill,
} from '../../wailsjs/go/app/App'
import type { app } from '../../wailsjs/go/models'
import { errorMessage } from '../stores/workbench'

// SkillSettings 在设置页管理 ~/.b-desktop/skills 与项目级技能。
// 新建写入用户级目录；项目级技能可以编辑与删除，但删除时会同时移除其目录。

interface FormState {
  name: string
  description: string
  content: string
}

const EMPTY_FORM: FormState = { name: '', description: '', content: '' }

const SOURCE_LABEL: Record<string, string> = {
  user: '用户级',
  project: '项目级',
}

const fieldClass =
  'w-full border border-line bg-surface px-3 py-2 text-[13px] text-fg outline-none transition placeholder:text-subtle focus:border-accent disabled:opacity-60'

const smallButton =
  'cursor-pointer border border-line px-2 py-1 text-[11px] text-muted transition hover:border-line-strong hover:bg-hover hover:text-fg'

export default function SkillSettings() {
  const [skills, setSkills] = useState<app.SkillDTO[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [form, setForm] = useState<FormState | null>(null)
  const [editingName, setEditingName] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [busyName, setBusyName] = useState<string | null>(null)
  const [query, setQuery] = useState('')

  const refresh = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setSkills(await ListSkills())
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void refresh()
  }, [refresh])

  const startCreate = () => {
    setEditingName(null)
    setForm({ ...EMPTY_FORM })
    setError(null)
  }

  const startEdit = async (skill: app.SkillDTO) => {
    setError(null)
    setBusyName(skill.name)
    try {
      const detail = await GetSkillDetail(skill.name)
      setEditingName(skill.name)
      setForm({
        name: detail.name,
        description: detail.description,
        content: detail.content,
      })
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusyName(null)
    }
  }

  const save = async () => {
    if (!form || saving) return
    const name = form.name.trim()
    if (name === '') {
      setError('技能名不能为空')
      return
    }
    setSaving(true)
    setError(null)
    try {
      if (editingName === null) {
        await CreateSkill({ name, description: form.description.trim(), content: form.content })
      } else {
        await UpdateSkill({ name: editingName, description: form.description.trim(), content: form.content })
      }
      setForm(null)
      setEditingName(null)
      await refresh()
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  const remove = async (skill: app.SkillDTO) => {
    if (busyName) return
    if (!window.confirm(`删除技能「${skill.name}」及其目录？`)) return
    setBusyName(skill.name)
    setError(null)
    try {
      await DeleteSkill(skill.name)
      await refresh()
      if (editingName === skill.name) {
        setForm(null)
        setEditingName(null)
      }
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusyName(null)
    }
  }

  const reloadAll = async () => {
    setError(null)
    try {
      setSkills(await ReloadSkills())
    } catch (err) {
      setError(errorMessage(err))
    }
  }

  const keyword = query.trim().toLowerCase()
  const filtered =
    keyword === ''
      ? skills
      : skills.filter((skill) => skill.name.toLowerCase().includes(keyword))

  return (
    <section role="tabpanel" aria-label="技能" data-testid="settings-skills">
      <div className="flex items-center justify-between">
        <div>
          <h3 className="font-mono text-[10px] font-medium uppercase tracking-[0.16em] text-subtle">技能</h3>
          <p className="mt-1 text-[11px] text-subtle">
            新建写入 ~/.b-desktop/skills/&lt;名称&gt;/SKILL.md；项目级技能来自工作目录下的 .b-desktop/skills。
          </p>
        </div>
        <div className="flex shrink-0 gap-1.5">
          <button type="button" onClick={() => void reloadAll()} className={smallButton}>
            重新扫描
          </button>
          <button
            type="button"
            data-testid="skill-add"
            onClick={startCreate}
            className="cursor-pointer bg-accent px-2.5 py-1 text-[11px] font-medium text-on-accent transition hover:bg-accent-hover"
          >
            新建技能
          </button>
        </div>
      </div>

      {error && (
        <p data-testid="skill-error" className="mt-3 border border-danger-line bg-danger-soft px-3 py-2 text-xs text-danger-text">
          {error}
        </p>
      )}

      {form && (
        <div data-testid="skill-form" className="mt-3 space-y-2 border border-line bg-panel p-3">
          <div className="grid gap-2 sm:grid-cols-2">
            <label className="block">
              <span className="text-[11px] text-muted">名称</span>
              <input
                data-testid="skill-name"
                value={form.name}
                disabled={editingName !== null}
                onChange={(event) => setForm({ ...form, name: event.target.value })}
                placeholder="代码审查"
                className={`mt-1 ${fieldClass}`}
              />
            </label>
            <label className="block">
              <span className="text-[11px] text-muted">描述</span>
              <input
                data-testid="skill-description"
                value={form.description}
                onChange={(event) => setForm({ ...form, description: event.target.value })}
                placeholder="一句话说明何时使用该技能"
                className={`mt-1 ${fieldClass}`}
              />
            </label>
          </div>
          <label className="block">
            <span className="text-[11px] text-muted">正文（Markdown，调用 Skill 工具时加载）</span>
            <textarea
              data-testid="skill-content"
              value={form.content}
              onChange={(event) => setForm({ ...form, content: event.target.value })}
              rows={8}
              placeholder={'# 步骤\n\n1. 读取改动\n2. 逐条检查'}
              className={`mt-1 resize-y font-mono text-xs ${fieldClass}`}
            />
          </label>
          <div className="flex justify-end gap-2 pt-1">
            <button
              type="button"
              onClick={() => {
                setForm(null)
                setEditingName(null)
              }}
              className={smallButton}
            >
              取消
            </button>
            <button
              type="button"
              data-testid="skill-save"
              disabled={saving}
              onClick={() => void save()}
              className="cursor-pointer bg-accent px-3 py-1 text-[11px] font-medium text-on-accent transition hover:bg-accent-hover disabled:opacity-60"
            >
              {saving ? '保存中…' : '保存'}
            </button>
          </div>
        </div>
      )}

      <div className="mt-3">
        <input
          type="search"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          placeholder="按名称过滤…"
          aria-label="按名称过滤技能"
          className={fieldClass}
        />
      </div>

      <div className="mt-3 space-y-2">
        {loading && skills.length === 0 && <p className="text-xs text-subtle">正在扫描技能…</p>}
        {!loading && skills.length === 0 && !form && (
          <div className="border border-dashed border-line px-4 py-8 text-center">
            <p className="text-sm text-muted">尚未安装技能</p>
            <p className="mt-1 text-[11px] text-subtle">新建一个技能，Agent 会在系统提示里看到它的名称与描述</p>
          </div>
        )}
        {!loading && skills.length > 0 && filtered.length === 0 && (
          <p className="border border-dashed border-line px-4 py-6 text-center text-xs text-subtle">
            没有匹配「{query.trim()}」的技能
          </p>
        )}
        {filtered.map((skill) => (
          <div
            key={`${skill.source}:${skill.name}`}
            data-testid={`skill-item-${skill.name}`}
            className="flex items-start gap-3 border border-line bg-panel p-3"
          >
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-2">
                <span className="truncate text-sm font-medium text-fg">{skill.name}</span>
                <span className="border border-line px-1.5 py-0.5 text-[10px] text-subtle">
                  {SOURCE_LABEL[skill.source] ?? skill.source}
                </span>
              </div>
              <p className="mt-1 text-xs text-muted">{skill.description || '（无描述）'}</p>
            </div>
            <div className="flex shrink-0 gap-1.5">
              <button
                type="button"
                disabled={busyName === skill.name}
                onClick={() => void startEdit(skill)}
                className={`${smallButton} disabled:opacity-50`}
              >
                编辑
              </button>
              <button
                type="button"
                disabled={busyName === skill.name}
                onClick={() => void remove(skill)}
                className={`${smallButton} text-danger-text disabled:opacity-50`}
              >
                删除
              </button>
            </div>
          </div>
        ))}
      </div>
    </section>
  )
}
