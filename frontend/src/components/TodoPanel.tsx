import { useState } from 'react'
import { useWorkbenchStore } from '../stores/workbench'
import type { TodoItemView } from '../types/agent'

// TodoPanel 是会话待办清单的浮动计划面板（cc / ZCode 的 todo 视图）。
// todo_update 事件驱动整体替换；清单为空时不渲染。
export default function TodoPanel() {
  const todos = useWorkbenchStore((state) => state.todos)
  const [collapsed, setCollapsed] = useState(false)

  if (todos.length === 0) return null
  const completed = todos.filter((item) => item.status === 'completed').length
  const active = todos.find((item) => item.status === 'in_progress')

  return (
    <div
      data-testid="todo-panel"
      className="pointer-events-auto absolute right-4 top-3 z-10 w-72 overflow-hidden rounded-lg border border-line bg-raised shadow-pop"
    >
      <button
        type="button"
        onClick={() => setCollapsed((prev) => !prev)}
        className="flex w-full cursor-pointer items-center gap-2 px-3 py-2 text-left transition hover:bg-hover"
      >
        <span className="font-mono text-[10px] uppercase tracking-[0.14em] text-subtle">
          任务清单
        </span>
        <span className="ml-auto shrink-0 font-mono text-[10px] tabular-nums text-muted">
          {completed}/{todos.length}
        </span>
        <span
          aria-hidden="true"
          className="shrink-0 font-mono text-[10px] text-subtle transition"
          style={{ transform: collapsed ? 'rotate(-90deg)' : 'none' }}
        >
          ▾
        </span>
      </button>
      {!collapsed && (
        <ul className="max-h-56 overflow-auto border-t border-line/70 py-1">
          {todos.map((item, index) => (
            <TodoRow key={`${index}-${item.content}`} item={item} active={item === active} />
          ))}
        </ul>
      )}
    </div>
  )
}

function TodoRow({ item, active }: { item: TodoItemView; active: boolean }) {
  const label = item.status === 'in_progress' && item.activeForm ? item.activeForm : item.content
  return (
    <li
      className={`flex items-start gap-2 px-3 py-1.5 ${active ? 'bg-surface/60' : ''}`}
      data-status={item.status}
    >
      <span
        aria-hidden="true"
        className={`mt-0.5 shrink-0 font-mono text-[10px] leading-4 ${
          item.status === 'completed'
            ? 'text-success-text'
            : item.status === 'in_progress'
              ? 'text-accent-text'
              : 'text-subtle'
        }`}
      >
        {item.status === 'completed' ? '✓' : item.status === 'in_progress' ? '▸' : '·'}
      </span>
      <span
        className={`min-w-0 flex-1 text-[11px] leading-4 ${
          item.status === 'completed'
            ? 'text-subtle line-through decoration-line-strong'
            : active
              ? 'text-fg'
              : 'text-muted'
        }`}
      >
        {label}
      </span>
    </li>
  )
}
