import { useContext, useEffect, useRef, useState, type PointerEvent as ReactPointerEvent } from 'react'
import { createPortal } from 'react-dom'
import type { MermaidConfig } from 'mermaid'
import { MarkdownStreamingContext } from './markdownStreaming'

let renderSeq = 0

const ZOOM_MIN = 0.4
const ZOOM_MAX = 8
const ZOOM_STEP = 1.25

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value))
}

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

interface Tint {
  fill: string
  stroke: string
}

interface DiagramPalette {
  // 节点按 nth-of-type 轮换，避免整张图只有一种灰色。
  nodes: Tint[]
  // 子图面积大，只用极淡的同族底色区分层次。
  clusters: Tint[]
  // 饼图等按数据分类配色的图形。
  pies: string[]
}

const DARK_PALETTE: DiagramPalette = {
  nodes: [
    { fill: '#2a2415', stroke: '#6f5b2c' },
    { fill: '#152b27', stroke: '#2f6156' },
    { fill: '#1a2434', stroke: '#365378' },
    { fill: '#251e32', stroke: '#513e6d' },
    { fill: '#2c1d25', stroke: '#6b3b4d' },
    { fill: '#232a1a', stroke: '#4e6132' },
    { fill: '#1b242b', stroke: '#3d5367' },
    { fill: '#2c211b', stroke: '#6b4732' },
  ],
  clusters: [
    { fill: 'rgba(233, 201, 110, 0.055)', stroke: 'rgba(233, 201, 110, 0.20)' },
    { fill: 'rgba(76, 211, 154, 0.05)', stroke: 'rgba(76, 211, 154, 0.18)' },
    { fill: 'rgba(127, 168, 255, 0.055)', stroke: 'rgba(127, 168, 255, 0.20)' },
    { fill: 'rgba(192, 140, 245, 0.055)', stroke: 'rgba(192, 140, 245, 0.20)' },
    { fill: 'rgba(255, 138, 157, 0.05)', stroke: 'rgba(255, 138, 157, 0.18)' },
    { fill: 'rgba(185, 207, 107, 0.05)', stroke: 'rgba(185, 207, 107, 0.18)' },
    { fill: 'rgba(143, 182, 207, 0.05)', stroke: 'rgba(143, 182, 207, 0.18)' },
    { fill: 'rgba(240, 164, 92, 0.055)', stroke: 'rgba(240, 164, 92, 0.20)' },
  ],
  pies: ['#d9b45c', '#3fae82', '#6f93d8', '#a678d8', '#d96a7f', '#93a94f', '#6c92a8', '#c9834f'],
}

const LIGHT_PALETTE: DiagramPalette = {
  nodes: [
    { fill: '#f9edc9', stroke: '#c9a54a' },
    { fill: '#e4f2ec', stroke: '#6fae95' },
    { fill: '#e7eefb', stroke: '#7d9fd6' },
    { fill: '#f0eafb', stroke: '#a68bd0' },
    { fill: '#fbe9ee', stroke: '#d4899e' },
    { fill: '#edf3e0', stroke: '#93ac6a' },
    { fill: '#e9eff3', stroke: '#8aa3b3' },
    { fill: '#faece2', stroke: '#cd9976' },
  ],
  clusters: [
    { fill: '#fcf8ea', stroke: '#e3d3a5' },
    { fill: '#f1f9f5', stroke: '#c2e0d5' },
    { fill: '#f2f7fd', stroke: '#c8d9ef' },
    { fill: '#f7f3fc', stroke: '#ddcff0' },
    { fill: '#fdf4f6', stroke: '#efcbd4' },
    { fill: '#f6f9ef', stroke: '#d5e0c0' },
    { fill: '#f4f7f9', stroke: '#cfdbe3' },
    { fill: '#fdf7f1', stroke: '#ecd6c4' },
  ],
  pies: ['#c9a54a', '#3f8f72', '#4f74b8', '#8a63bd', '#b85a70', '#748c3b', '#517b92', '#b07445'],
}

// buildPaletteCss 生成注入到 SVG 内的样式。
// mermaid 的节点规则带 #svg-id 前缀，这里用 !important 覆盖填充色；
// 同时关掉 neo 外观的投影，保持与工作台一致的扁平层级。
function buildPaletteCss(palette: DiagramPalette): string {
  const shapeTags = ['rect', 'polygon', 'circle', 'ellipse', 'path']
  const rules: string[] = []

  palette.nodes.forEach((tint, index) => {
    const selectors = shapeTags
      .map((tag) => `.node:nth-of-type(${palette.nodes.length}n+${index + 1}) > ${tag}`)
      .join(',')
    rules.push(`${selectors}{fill:${tint.fill}!important;stroke:${tint.stroke}!important;}`)
  })

  palette.clusters.forEach((tint, index) => {
    rules.push(
      `.cluster:nth-of-type(${palette.clusters.length}n+${index + 1}) > rect{fill:${tint.fill}!important;stroke:${tint.stroke}!important;}`,
    )
  })

  rules.push(
    '.node rect,.node polygon,.node circle,.node ellipse,.node path,.cluster rect{filter:none!important;stroke-width:1px!important;}',
    '.cluster-label span p{background:transparent!important;}',
  )

  return rules.join('\n')
}

// buildConfig 把应用主题映射成 mermaid 的配色。
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
  const palette = theme === 'light' ? LIGHT_PALETTE : DARK_PALETTE
  const pies = Object.fromEntries(palette.pies.map((color, index) => [`pie${index + 1}`, color]))

  return {
    startOnLoad: false,
    securityLevel: 'strict',
    suppressErrorRendering: true,
    maxTextSize: 50000,
    maxEdges: 500,
    theme: 'base',
    themeCSS: buildPaletteCss(palette),
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
      ...pies,
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
// - 解析失败时回退到源码并展示原因，不打断整条消息；
// - 图表本体不带外框，点击任意位置进入可缩放/拖动的预览。
export function MermaidBlock({ code }: MermaidBlockProps) {
  const streaming = useContext(MarkdownStreamingContext)
  const [svg, setSvg] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [theme, setTheme] = useState<'light' | 'dark'>(currentTheme)
  const [expanded, setExpanded] = useState(false)
  const [view, setView] = useState({ scale: 1, x: 0, y: 0 })
  const closeRef = useRef<HTMLButtonElement | null>(null)
  const viewportRef = useRef<HTMLDivElement | null>(null)
  const dragRef = useRef<{
    pointerId: number
    startX: number
    startY: number
    originX: number
    originY: number
  } | null>(null)

  const openViewer = () => {
    setView({ scale: 1, x: 0, y: 0 })
    setExpanded(true)
  }

  const resetView = () => setView({ scale: 1, x: 0, y: 0 })

  const zoomBy = (factor: number) => {
    setView((prev) => {
      const scale = clamp(prev.scale * factor, ZOOM_MIN, ZOOM_MAX)
      const ratio = scale / prev.scale
      return { scale, x: prev.x * ratio, y: prev.y * ratio }
    })
  }

  // 滚轮以指针为锚点缩放；必须用非 passive 监听，React 的 onWheel 拦不住页面滚动。
  useEffect(() => {
    if (!expanded) return
    const element = viewportRef.current
    if (!element) return
    const onWheel = (event: WheelEvent) => {
      event.preventDefault()
      const rect = element.getBoundingClientRect()
      const pointerX = event.clientX - rect.left - rect.width / 2
      const pointerY = event.clientY - rect.top - rect.height / 2
      const factor = Math.exp(-clamp(event.deltaY, -120, 120) * 0.0015)
      setView((prev) => {
        const scale = clamp(prev.scale * factor, ZOOM_MIN, ZOOM_MAX)
        const ratio = scale / prev.scale
        return {
          scale,
          x: pointerX - (pointerX - prev.x) * ratio,
          y: pointerY - (pointerY - prev.y) * ratio,
        }
      })
    }
    element.addEventListener('wheel', onWheel, { passive: false })
    return () => element.removeEventListener('wheel', onWheel)
  }, [expanded])

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
      dragRef.current = null
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

  const handlePointerDown = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (event.button !== 0) return
    event.currentTarget.setPointerCapture(event.pointerId)
    dragRef.current = {
      pointerId: event.pointerId,
      startX: event.clientX,
      startY: event.clientY,
      originX: view.x,
      originY: view.y,
    }
  }

  const handlePointerMove = (event: ReactPointerEvent<HTMLDivElement>) => {
    const drag = dragRef.current
    if (!drag || drag.pointerId !== event.pointerId) return
    setView((prev) => ({
      ...prev,
      x: drag.originX + event.clientX - drag.startX,
      y: drag.originY + event.clientY - drag.startY,
    }))
  }

  const handlePointerEnd = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (dragRef.current?.pointerId !== event.pointerId) return
    dragRef.current = null
    if (event.currentTarget.hasPointerCapture(event.pointerId)) {
      event.currentTarget.releasePointerCapture(event.pointerId)
    }
  }

  if (streaming) {
    return (
      <figure
        data-testid="mermaid-streaming"
        className="my-3 overflow-x-auto rounded-xl border border-line bg-code p-3"
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
        className="my-3 overflow-hidden rounded-xl border border-danger-line bg-danger-soft"
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
      className="group relative my-3 overflow-x-auto"
    >
      {svg ? (
        <>
          <div
            role="button"
            tabIndex={0}
            data-testid="mermaid-canvas"
            aria-label="放大 Mermaid 图表"
            title="点击放大"
            onClick={openViewer}
            onKeyDown={(event) => {
              if (event.key === 'Enter' || event.key === ' ') {
                event.preventDefault()
                openViewer()
              }
            }}
            className="mermaid-svg cursor-zoom-in select-none rounded-lg outline-offset-4 focus-visible:outline-2 focus-visible:outline-accent"
            dangerouslySetInnerHTML={{ __html: svg }}
          />
          <button
            type="button"
            data-testid="mermaid-expand"
            aria-label="放大图表"
            title="放大图表"
            onClick={openViewer}
            className="absolute right-2 top-2 z-10 cursor-pointer rounded-lg border border-line bg-surface/90 p-1.5 text-muted opacity-70 shadow-card backdrop-blur transition hover:bg-hover hover:text-fg hover:opacity-100 focus:opacity-100"
          >
            <ExpandIcon />
          </button>
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
          >
            <div className="flex h-14 shrink-0 items-center justify-between gap-4 border-b border-line/70 px-5">
              <div className="flex min-w-0 items-center gap-2 text-sm font-medium text-fg">
                <span className="text-muted" aria-hidden="true">
                  ◇
                </span>
                <span className="truncate">Mermaid 图表</span>
              </div>
              <div className="flex shrink-0 items-center gap-3">
                <span className="hidden text-[11px] text-subtle md:inline">
                  滚轮缩放 · 拖动平移 · 双击复位 · Esc 关闭
                </span>
                <div className="flex items-center gap-0.5 rounded-lg border border-line/70 bg-panel px-1 py-0.5">
                  <button
                    type="button"
                    aria-label="缩小图表"
                    title="缩小"
                    onClick={() => zoomBy(1 / ZOOM_STEP)}
                    className="cursor-pointer rounded-md px-2 py-1 font-mono text-[12px] text-muted transition hover:bg-hover hover:text-fg"
                  >
                    −
                  </button>
                  <button
                    type="button"
                    data-testid="mermaid-zoom"
                    aria-label="重置缩放"
                    title="重置缩放"
                    onClick={resetView}
                    className="min-w-12 cursor-pointer rounded-md px-1 py-1 font-mono text-[11px] text-muted transition hover:bg-hover hover:text-fg"
                  >
                    {Math.round(view.scale * 100)}%
                  </button>
                  <button
                    type="button"
                    aria-label="放大图表"
                    title="放大"
                    onClick={() => zoomBy(ZOOM_STEP)}
                    className="cursor-pointer rounded-md px-2 py-1 font-mono text-[12px] text-muted transition hover:bg-hover hover:text-fg"
                  >
                    +
                  </button>
                </div>
                <button
                  ref={closeRef}
                  type="button"
                  data-testid="mermaid-close"
                  aria-label="关闭图表预览"
                  onClick={() => setExpanded(false)}
                  className="cursor-pointer rounded-lg p-1.5 text-subtle transition hover:bg-hover hover:text-fg"
                >
                  <CloseIcon />
                </button>
              </div>
            </div>
            <div
              ref={viewportRef}
              data-testid="mermaid-viewport"
              data-scale={view.scale.toFixed(2)}
              onPointerDown={handlePointerDown}
              onPointerMove={handlePointerMove}
              onPointerUp={handlePointerEnd}
              onPointerCancel={handlePointerEnd}
              onDoubleClick={resetView}
              className="relative min-h-0 flex-1 cursor-grab touch-none select-none overflow-hidden active:cursor-grabbing"
            >
              <div
                className="absolute inset-0 origin-center"
                style={{ transform: `translate(${view.x}px, ${view.y}px) scale(${view.scale})` }}
              >
                <div
                  className="mermaid-fullscreen h-full w-full"
                  dangerouslySetInnerHTML={{ __html: svg }}
                />
              </div>
            </div>
          </div>,
          document.body,
        )}
    </figure>
  )
}
