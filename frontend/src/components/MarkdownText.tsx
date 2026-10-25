import {
  Children,
  isValidElement,
  memo,
  useDeferredValue,
  useMemo,
  type ComponentProps,
  type ReactNode,
} from 'react'
import ReactMarkdown from 'react-markdown'
import rehypeHighlight from 'rehype-highlight'
import remarkGfm from 'remark-gfm'
import type { Components } from 'react-markdown'
import { MermaidBlock } from './MermaidBlock'
import { MarkdownStreamingContext } from './markdownStreaming'
import { splitStablePrefix } from './streamingMarkdown'

// 插件数组保持模块级常量，避免每次渲染重建 markdown 处理管线。
const REMARK_PLUGINS: NonNullable<ComponentProps<typeof ReactMarkdown>['remarkPlugins']> = [
  remarkGfm,
]

const REHYPE_PLUGINS: NonNullable<ComponentProps<typeof ReactMarkdown>['rehypePlugins']> = [
  [rehypeHighlight, { ignoreMissing: true }],
]

// isMermaidChild 判断 pre 的子节点是否会被渲染成 Mermaid 图表。
// react-markdown 传给 pre 的是 code 元素本身，此时还没有渲染成 MermaidBlock，
// 所以除了组件类型，还要按 language-mermaid 类名兜底。
function isMermaidChild(child: ReactNode): boolean {
  if (!isValidElement(child)) return false
  if (child.type === MermaidBlock) return true
  const className = (child.props as { className?: unknown }).className
  return typeof className === 'string' && /(^|\s)language-mermaid(\s|$)/.test(className)
}

const COMPONENTS: Components = {
  // 外链交给系统浏览器；不覆盖会让 WKWebView 直接跳走整个应用。
  a: ({ node: _node, ...props }) => <a {...props} target="_blank" rel="noreferrer noopener" />,
  code: ({ node: _node, className, children, ...props }) => {
    const language = /language-([\w-]+)/.exec(className ?? '')?.[1]
    if (language === 'mermaid') {
      return <MermaidBlock code={String(children ?? '').replace(/\n$/, '')} />
    }
    return (
      <code className={className} {...props}>
        {children}
      </code>
    )
  },
  // Mermaid 块自带 figure 容器，不能再套一层 <pre>。
  pre: ({ node: _node, children, ...props }) => {
    if (Children.toArray(children).some(isMermaidChild)) return <>{children}</>
    return <pre {...props}>{children}</pre>
  },
}

interface MarkdownTextProps {
  text: string
  // streaming 为 true 时不解析 Mermaid，避免不完整语法导致反复报错与重绘。
  streaming?: boolean
}

// MarkdownBody 渲染一段 Markdown。分段渲染让已稳定的前缀不再随增量重解析。
const MarkdownBody = memo(function MarkdownBody({ text }: { text: string }) {
  return (
    <ReactMarkdown
      remarkPlugins={REMARK_PLUGINS}
      rehypePlugins={REHYPE_PLUGINS}
      components={COMPONENTS}
    >
      {text}
    </ReactMarkdown>
  )
})

// MarkdownText 渲染助手输出的 GFM 内容（表格、删除线、任务列表）。
//
// 流式阶段用 useDeferredValue 让高频增量合并成较少的重解析；并把文本切成
// 「已稳定前缀 + 增长后缀」两段：前缀定型后不再解析，只有后缀跟随增量更新。
export const MarkdownText = memo(function MarkdownText({
  text,
  streaming = false,
}: MarkdownTextProps) {
  const deferred = useDeferredValue(text)
  const split = useMemo(() => splitStablePrefix(deferred), [deferred])
  return (
    <div data-testid="markdown" className="markdown-body">
      <MarkdownStreamingContext.Provider value={streaming}>
        {split.stable !== '' && <MarkdownBody text={split.stable} />}
        {split.unstable !== '' && <MarkdownBody text={split.unstable} />}
      </MarkdownStreamingContext.Provider>
    </div>
  )
})
