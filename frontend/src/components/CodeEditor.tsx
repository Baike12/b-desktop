import { useMemo, useRef, useState } from 'react'

// CodeEditor 是预览面板里的轻量文本编辑器：左侧行号列 + 右侧 textarea。
//
// 用 textarea 而不是自研 contenteditable：原生输入、撤销、输入法、选区行为都
// 白拿，代价是没有语法着色（编辑态以可读性优先）。

export interface CodeEditorProps {
  // value 是当前文本。
  value: string
  // onChange 在输入时触发。
  onChange: (value: string) => void
  // onSave 在按下 Cmd/Ctrl+S 时触发。
  onSave: () => void
  // onCancel 在按下 Esc 时触发。
  onCancel: () => void
}

export default function CodeEditor({ value, onChange, onSave, onCancel }: CodeEditorProps) {
  const gutterRef = useRef<HTMLDivElement>(null)
  const [scrollTop, setScrollTop] = useState(0)

  const lineCount = useMemo(() => value.split('\n').length, [value])

  return (
    <div
      data-testid="code-editor"
      className="flex min-h-0 flex-1 overflow-hidden bg-code font-mono text-xs leading-5"
    >
      <div
        aria-hidden="true"
        className="shrink-0 select-none overflow-hidden border-r border-line bg-code px-3 text-right text-subtle"
      >
        <div ref={gutterRef} style={{ transform: `translateY(${-scrollTop}px)` }}>
          {Array.from({ length: lineCount }, (_, index) => (
            <div key={index} className="min-h-5">
              {index + 1}
            </div>
          ))}
        </div>
      </div>
      <textarea
        data-testid="code-editor-input"
        value={value}
        spellCheck={false}
        autoCapitalize="off"
        autoCorrect="off"
        onChange={(event) => onChange(event.target.value)}
        onScroll={(event) => setScrollTop(event.currentTarget.scrollTop)}
        onKeyDown={(event) => {
          if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 's') {
            event.preventDefault()
            onSave()
            return
          }
          if (event.key === 'Escape') {
            event.preventDefault()
            onCancel()
          }
        }}
        className="min-h-0 flex-1 resize-none overflow-auto whitespace-pre bg-transparent px-3 text-fg outline-none"
      />
    </div>
  )
}
