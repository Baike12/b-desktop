import { Children, isValidElement, memo, useDeferredValue, type ComponentProps } from 'react'
import ReactMarkdown from 'react-markdown'
import rehypeHighlight from 'rehype-highlight'
import remarkGfm from 'remark-gfm'
import type { Components } from 'react-markdown'
import { MermaidBlock } from './MermaidBlock'
import { MarkdownStreamingContext } from './markdownStreaming'

// 插件数组保持模块级常量，避免每次渲染重建 markdown 处理管线。
const REMARK_PLUGINS: NonNullable<ComponentProps<typeof ReactMarkdown>['remarkPlugins']> = [
  remarkGfm,
]

const REHYPE_PLUGINS: NonNullable<ComponentProps<typeof ReactMarkdown>['rehypePlugins']> = [
  [rehypeHighlight, { ignoreMissing: true }],
]

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
    const hasMermaid = Children.toArray(children).some(
      (child) => isValidElement(child) && child.type === MermaidBlock,
    )
    if (hasMermaid) return <>{children}</>
    return <pre {...props}>{children}</pre>
  },
}

interface MarkdownTextProps {
  text: string
  // streaming 为 true 时不解析 Mermaid，避免不完整语法导致反复报错与重绘。
  streaming?: boolean
}

// MarkdownText 渲染助手输出的 GFM 内容（表格、删除线、任务列表）。
// 流式阶段用 useDeferredValue 让高频增量合并成较少的重解析。
export const MarkdownText = memo(function MarkdownText({
  text,
  streaming = false,
}: MarkdownTextProps) {
  const deferred = useDeferredValue(text)
  return (
    <div data-testid="markdown" className="markdown-body">
      <MarkdownStreamingContext.Provider value={streaming}>
        <ReactMarkdown
          remarkPlugins={REMARK_PLUGINS}
          rehypePlugins={REHYPE_PLUGINS}
          components={COMPONENTS}
        >
          {deferred}
        </ReactMarkdown>
      </MarkdownStreamingContext.Provider>
    </div>
  )
})
