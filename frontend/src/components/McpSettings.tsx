import { useCallback, useEffect, useState } from 'react'
import {
  DeleteMCPServer,
  ListMcpServers,
  ReloadMcp,
  SaveMCPServer,
} from '../../wailsjs/go/app/App'
import type { app } from '../../wailsjs/go/models'
import { errorMessage } from '../stores/workbench'

// McpSettings 在设置页管理 ~/.b-desktop/mcp.json 中的 stdio 服务器。
// 环境变量的值不会下发前端：编辑时只显示键名，值留空表示保留磁盘上的原值。

interface FormState {
  name: string
  command: string
  argsText: string
  envText: string
}

const EMPTY_FORM: FormState = { name: '', command: '', argsText: '', envText: '' }

const STATE_STYLE: Record<string, { label: string; dot: string }> = {
  ready: { label: '已连接', dot: 'bg-emerald-500' },
  error: { label: '连接失败', dot: 'bg-rose-500' },
  stopped: { label: '未启动', dot: 'bg-amber-500' },
}

const fieldClass =
  'w-full rounded-lg border border-line bg-surface px-3 py-2 text-sm text-fg outline-none transition placeholder:text-subtle focus:border-accent'

const smallButton =
  'cursor-pointer rounded-md border border-line px-2 py-1 text-[11px] text-muted transition hover:bg-hover hover:text-fg'

// parseEnv 解析每行 KEY=VALUE；非法行收集到 invalid。
function parseEnv(text: string): { env: Record<string, string>; invalid: string } {
  const env: Record<string, string> = {}
  for (const raw of text.split('\n')) {
    const line = raw.trim()
    if (line === '') continue
    const index = line.indexOf('=')
    if (index <= 0) return { env, invalid: line }
    const key = line.slice(0, index).trim()
    if (key === '') return { env, invalid: line }
    env[key] = line.slice(index + 1).trim()
  }
  return { env, invalid: '' }
}

function envText(server: app.MCPServerDTO): string {
  return (server.envKeys ?? []).map((key) => `${key}=`).join('\n')
}

export default function McpSettings() {
  const [servers, setServers] = useState<app.MCPServerDTO[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [form, setForm] = useState<FormState | null>(null)
  const [editingName, setEditingName] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [busyName, setBusyName] = useState<string | null>(null)

  const refresh = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setServers(await ListMcpServers())
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

  const startEdit = (server: app.MCPServerDTO) => {
    setEditingName(server.name)
    setForm({
      name: server.name,
      command: server.command,
      argsText: (server.args ?? []).join('\n'),
      envText: envText(server),
    })
    setError(null)
  }

  const save = async () => {
    if (!form || saving) return
    const name = form.name.trim()
    const command = form.command.trim()
    if (name === '' || command === '') {
      setError('服务器名与命令不能为空')
      return
    }
    const parsed = parseEnv(form.envText)
    if (parsed.invalid !== '') {
      setError(`环境变量格式应为 KEY=VALUE：${parsed.invalid}`)
      return
    }
    setSaving(true)
    setError(null)
    try {
      await SaveMCPServer({
        name,
        command,
        args: form.argsText
          .split('\n')
          .map((item) => item.trim())
          .filter((item) => item !== ''),
        env: parsed.env,
      })
      setForm(null)
      setEditingName(null)
      await refresh()
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  const remove = async (server: app.MCPServerDTO) => {
    if (busyName) return
    if (!window.confirm(`删除 MCP 服务器「${server.name}」？`)) return
    setBusyName(server.name)
    setError(null)
    try {
      await DeleteMCPServer(server.name)
      await refresh()
      if (editingName === server.name) {
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
      await ReloadMcp()
      await refresh()
    } catch (err) {
      setError(errorMessage(err))
    }
  }

  return (
    <section role="tabpanel" aria-label="MCP" data-testid="settings-mcp">
      <div className="flex items-center justify-between">
        <div>
          <h3 className="text-xs font-medium uppercase tracking-wide text-subtle">MCP 服务器</h3>
          <p className="mt-1 text-[11px] text-subtle">
            配置写入 ~/.b-desktop/mcp.json；工具以 mcp__服务器__工具 的形式提供给 Agent。
          </p>
        </div>
        <div className="flex shrink-0 gap-1.5">
          <button type="button" onClick={() => void reloadAll()} className={smallButton}>
            重连
          </button>
          <button
            type="button"
            data-testid="mcp-add"
            onClick={startCreate}
            className="cursor-pointer rounded-md bg-accent px-2.5 py-1 text-[11px] font-medium text-white transition hover:opacity-90"
          >
            新增服务器
          </button>
        </div>
      </div>

      {error && (
        <p data-testid="mcp-error" className="mt-3 rounded-lg border border-danger-line bg-danger-soft px-3 py-2 text-xs text-danger-text">
          {error}
        </p>
      )}

      {form && (
        <div data-testid="mcp-form" className="mt-3 space-y-2 rounded-lg border border-line bg-raised p-3">
          <div className="grid gap-2 sm:grid-cols-2">
            <label className="block">
              <span className="text-[11px] text-muted">服务器名</span>
              <input
                data-testid="mcp-name"
                value={form.name}
                disabled={editingName !== null}
                onChange={(event) => setForm({ ...form, name: event.target.value })}
                placeholder="filesystem"
                className={`mt-1 ${fieldClass}`}
              />
            </label>
            <label className="block">
              <span className="text-[11px] text-muted">命令</span>
              <input
                data-testid="mcp-command"
                value={form.command}
                onChange={(event) => setForm({ ...form, command: event.target.value })}
                placeholder="npx"
                className={`mt-1 ${fieldClass}`}
              />
            </label>
          </div>
          <label className="block">
            <span className="text-[11px] text-muted">参数（每行一个）</span>
            <textarea
              data-testid="mcp-args"
              value={form.argsText}
              onChange={(event) => setForm({ ...form, argsText: event.target.value })}
              rows={3}
              placeholder={'-y\n@modelcontextprotocol/server-filesystem\n/Users/me/project'}
              className={`mt-1 resize-y font-mono text-xs ${fieldClass}`}
            />
          </label>
          <label className="block">
            <span className="text-[11px] text-muted">环境变量（每行 KEY=VALUE，值留空保留原值）</span>
            <textarea
              data-testid="mcp-env"
              value={form.envText}
              onChange={(event) => setForm({ ...form, envText: event.target.value })}
              rows={3}
              placeholder={'API_KEY=\nLOG_LEVEL=debug'}
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
              data-testid="mcp-save"
              disabled={saving}
              onClick={() => void save()}
              className="cursor-pointer rounded-md bg-accent px-3 py-1 text-[11px] font-medium text-white transition hover:opacity-90 disabled:opacity-60"
            >
              {saving ? '保存中…' : '保存并重连'}
            </button>
          </div>
        </div>
      )}

      <div className="mt-3 space-y-2">
        {loading && servers.length === 0 && <p className="text-xs text-subtle">正在读取配置…</p>}
        {!loading && servers.length === 0 && !form && (
          <div className="rounded-lg border border-dashed border-line px-4 py-8 text-center">
            <p className="text-sm text-muted">尚未配置 MCP 服务器</p>
            <p className="mt-1 text-[11px] text-subtle">新增后点「重连」即可加载远端工具</p>
          </div>
        )}
        {servers.map((server) => {
          const state = STATE_STYLE[server.state] ?? { label: '未知', dot: 'bg-slate-400' }
          return (
            <div
              key={server.name}
              data-testid={`mcp-server-${server.name}`}
              className="rounded-lg border border-line bg-raised p-3"
            >
              <div className="flex flex-wrap items-center gap-2">
                <span className={`size-2 shrink-0 rounded-full ${state.dot}`} aria-hidden="true" />
                <span className="text-sm font-medium text-fg">{server.name}</span>
                <span className="text-[11px] text-subtle">{state.label}</span>
                <span className="text-[11px] text-subtle">{server.toolCount} 个工具</span>
                <div className="ml-auto flex gap-1.5">
                  <button type="button" onClick={() => startEdit(server)} className={smallButton}>
                    编辑
                  </button>
                  <button
                    type="button"
                    disabled={busyName === server.name}
                    onClick={() => void remove(server)}
                    className={`${smallButton} text-danger-text disabled:opacity-50`}
                  >
                    删除
                  </button>
                </div>
              </div>
              <p className="mt-1.5 break-all font-mono text-[11px] text-muted">
                {server.command} {(server.args ?? []).join(' ')}
              </p>
              {(server.envKeys ?? []).length > 0 && (
                <p className="mt-1 text-[11px] text-subtle">环境变量：{(server.envKeys ?? []).join('、')}</p>
              )}
              {server.error && <p className="mt-1 text-[11px] text-danger-text">{server.error}</p>}
            </div>
          )
        })}
      </div>
    </section>
  )
}
