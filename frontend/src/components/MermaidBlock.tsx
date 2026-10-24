import { useContext, useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import type { MermaidConfig } from 'mermaid'
import { MarkdownStreamingContext } from './markdownStreaming'

let renderSeq = 0

function ExpandIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-3.5"
      aria-hidden="true"
    >
      <path d="M8 3H5a2 2 0 0 0-2 2v3M16 3h3a2 2 0 0 1 2 2v3M8 21H5a2 2 0 0 1-2-2v-3M16 21h3a2 2 0 0 0 2-2v-3" />
    </svg>
  )
}

function CloseIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      className="size-4"
      aria-hidden="true"
    >
      <path d="M6 6l12 12M18 6 6 18" />
    </svg>
  )
}

function currentTheme(): 'light' | 'dark' {
  return document.documentElement.dataset.theme === 'light' ? 'light' : 'dark'
}

// readVar 从当前主题读取语义色，供 mermaid 的 themeVariables 使用。
function readVar(styles: CSSStyleDeclaration, name: string, fallback: string): string {
  const value = styles.getPropertyValue(name).trim()
  return value || fallback
}

// buildConfig 把应用主题映射成 mermaid 的基础配色。
// securityLevel=strict 会清洗标签内容；同时限制文本与边数量，避免恶意大图卡住渲染。
function buildConfig(theme: 'light' | 'dark'): MermaidConfig {
  const styles = getComputedStyle(document.documentElement)
  const v = (name: string, fallback: string) => readVar(styles, name, fallback)
  const fg = v('--fg', theme === 'light' ? '#0f172a' : '#e2e8f0')
  const raised = v('--raised', theme === 'light' ? '#f1f5f9' : '#0f172a')
  const surface = v('--surface', theme === 'light' ? '#ffffff' : '#020617')
  const panel = v('--panel', theme === 'light' ? '#f8fafc' : '#020617')
  const hover = v('--hover', theme === 'light' ? '#e8eef5' : '#1e293b')
  const code = v('--code', theme === 'light' ? '#f6f8fa' : '#0b1220')
  const line = v('--line', theme === 'light' ? '#e2e8f0' : '#1e293b')
  const lineStrong = v('--line-strong', theme === 'light' ? '#cbd5e1' : '#334155')
  const muted = v('--muted', theme === 'light' ? '#475569' : '#94a3b8')
  const accent = v('--accent', '#0284c7')
  const accentSoft = v('--accent-soft', 'rgba(14,165,233,.16)')
  const warnSoft = v('--warn-soft', 'rgba(245,158,11,.14)')
  const warnLine = v('--warn-line', '#78350f')
  const warnText = v('--warn-text', '#fcd34d')

  return {
    startOnLoad: false,
    securityLevel: 'strict',
    suppressErrorRendering: true,
    maxTextSize: 50000,
    maxEdges: 500,
    theme: 'base',
    themeVariables: {
      fontFamily: 'ui-sans-serif, system-ui, -apple-system, sans-serif',
      fontSize: '13px',
      background: surface,
      primaryColor: raised,
      primaryTextColor: fg,
      primaryBorderColor: lineStrong,
      secondaryColor: hover,
      secondaryTextColor: fg,
      secondaryBorderColor: lineStrong,
      tertiaryColor: code,
      tertiaryTextColor: fg,
      tertiaryBorderColor: line,
      lineColor: muted,
      textColor: fg,
      mainBkg: raised,
      nodeBorder: lineStrong,
      clusterBkg: panel,
      clusterBorder: line,
      edgeLabelBackground: surface,
      // sequenceDiagram
      actorBkg: raised,
      actorBorder: lineStrong,
      actorTextColor: fg,
      actorLineColor: muted,
      signalColor: muted,
      signalTextColor: fg,
      labelBoxBkgColor: raised,
      labelBoxBorderColor: lineStrong,
      labelTextColor: fg,
      loopTextColor: fg,
      noteBkgColor: warnSoft,
      noteTextColor: warnText,
      noteBorderColor: warnLine,
      activationBkgColor: accentSoft,
      activationBorderColor: accent,
      sequenceNumberColor: surface,
      // stateDiagram / classDiagram
      labelBackgroundColor: surface,
      compositeBackground: panel,
      compositeBorder: line,
      compositeTitleBackground: raised,
    },
    flowchart: { htmlLabels: false, useMaxWidth: true },
    sequence: { useMaxWidth: true },
    gantt: { useMaxWidth: true },
    journey: { useMaxWidth: true },
    class: { useMaxWidth: true },
    state: { useMaxWidth: true },
    er: { useMaxWidth: true },
    pie: { useMaxWidth: true },
  }
}

interface MermaidBlockProps {
  code: string
}

// MermaidBlock 渲染 ```mermaid 代码块。
// - 流式期间只显示源码，文本结束后才解析；
// - 主题切换后按新配色重绘；
// - 解析失败时回退到源码并展示原因，不打断整条消息。
export function MermaidBlock({ code }: MermaidBlockProps) {
  const streaming = useContext(MarkdownStreamingContext)
  const [svg, setSvg] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [theme, setTheme] = useState<'light' | 'dark'>(currentTheme)
  const [expanded, setExpanded] = useState(false)
  const closeRef = useRef<HTMLButtonElement | null>(null)

  // 放大预览：Esc 关闭并锁定页面滚动，打开时把焦点交给关闭按钮。
  useEffect(() => {
    if (!expanded) return
    const previousOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    closeRef.current?.focus()
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setExpanded(false)
    }
    window.addEventListener('keydown', onKeyDown)
    return () => {
      window.removeEventListener('keydown', onKeyDown)
      document.body.style.overflow = previousOverflow
    }
  }, [expanded])

  // 主题由根节点的 data-theme 决定，跟随设置页与系统外观即时重绘。
  useEffect(() => {
    const root = document.documentElement
    const observer = new MutationObserver(() => {
      setTheme(currentTheme())
    })
    observer.observe(root, { attributes: true, attributeFilter: ['data-theme'] })
    return () => observer.disconnect()
  }, [])

  useEffect(() => {
    if (streaming) return
    let cancelled = false
    const id = `mermaid-svg-${(renderSeq += 1)}`
    setLoading(true)
    setError('')

    void (async () => {
      try {
        const { default: mermaid } = await import('mermaid')
        if (cancelled) return
        mermaid.initialize(buildConfig(theme))
        const result = await mermaid.render(id, code)
        if (cancelled) return
        setSvg(result.svg)
        setLoading(false)
      } catch (err) {
        if (cancelled) return
        setError(err instanceof Error ? err.message : String(err))
        setLoading(false)
      }
    })()

    return () => {
      cancelled = true
    }
  }, [code, theme, streaming])

  if (streaming) {
    return (
      <figure
        data-testid="mermaid-streaming"
        className="my-3 overflow-x-auto border border-line bg-code p-3"
      >
        <pre className="text-[12px] leading-5 text-muted">
          <code>{code}</code>
        </pre>
        <figcaption className="mt-2 text-[11px] text-subtle">正在生成图表…</figcaption>
      </figure>
    )
  }

  if (error) {
    return (
      <div
        data-testid="mermaid-error"
        className="my-3 overflow-hidden border border-danger-line bg-danger-soft"
      >
        <p className="border-b border-danger-line px-3 py-2 text-[11px] text-danger-text">
          Mermaid 渲染失败：{error.split('\n')[0]}
        </p>
        <pre className="overflow-x-auto p-3 text-[12px] leading-5 text-fg">
          <code>{code}</code>
        </pre>
      </div>
    )
  }

  return (
    <figure
      data-testid="mermaid"
      aria-label="Mermaid 图表"
      className="group relative my-3 overflow-x-auto border border-line bg-surface p-3"
    >
      {svg ? (
        <>
          <button
            type="button"
            data-testid="mermaid-expand"
            aria-label="放大图表"
            title="放大图表"
            onClick={() => setExpanded(true)}
            className="absolute right-2 top-2 z-10 cursor-pointer border border-line bg-surface/90 p-1.5 text-muted opacity-70 backdrop-blur transition hover:bg-hover hover:text-fg hover:opacity-100 focus:opacity-100"
          >
            <ExpandIcon />
          </button>
          <div className="mermaid-svg" dangerouslySetInnerHTML={{ __html: svg }} />
        </>
      ) : (
        <p className="py-2 text-center text-[11px] text-subtle">
          {loading ? '正在渲染图表…' : ''}
        </p>
      )}

      {expanded &&
        svg &&
        createPortal(
          <div
            data-testid="mermaid-fullscreen"
            role="dialog"
            aria-modal="true"
            aria-label="Mermaid 图表全屏预览"
            className="fixed inset-0 z-[100] flex flex-col bg-surface"
            onClick={() => setExpanded(false)}
          >
            <div
              className="flex h-14 shrink-0 items-center justify-between border-b border-line px-5"
              onClick={(event) => event.stopPropagation()}
            >
              <div className="flex items-center gap-2 text-sm font-medium text-fg">
                <span className="text-muted" aria-hidden="true">
                  ◇
                </span>
                Mermaid 图表
              </div>
              <div className="flex items-center gap-3">
                <span className="hidden text-[11px] text-subtle sm:inline">Esc 关闭</span>
                <button
                  ref={closeRef}
                  type="button"
                  data-testid="mermaid-close"
                  aria-label="关闭图表预览"
                  onClick={() => setExpanded(false)}
                  className="cursor-pointer p-1.5 text-subtle transition hover:bg-hover hover:text-fg"
                >
                  <CloseIcon />
                </button>
              </div>
            </div>
            <div className="flex min-h-0 flex-1 items-center justify-center overflow-hidden p-6">
              <div
                className="mermaid-fullscreen h-full w-full"
                onClick={(event) => event.stopPropagation()}
                dangerouslySetInnerHTML={{ __html: svg }}
              />
            </div>
          </div>,
          document.body,
        )}
    </figure>
  )
}
