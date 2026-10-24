import { memo, useMemo } from 'react'

import { highlightCode, type HighlightLine } from '../lib/highlight'

// CodeView 渲染带语法高亮与行号的代码。
//
// 每行是一个块级元素：块级元素之间在复制时会保留换行，行号列用 select-none
// 排除在选区之外，复制到剪贴板的只有代码本身。

export interface CodeViewProps {
  // content 是代码正文。
  content: string
  // language 是 highlight.js 语言名；null 表示纯文本。
  language: string | null
  // showLineNumbers 控制是否显示行号列。
  showLineNumbers?: boolean
  // wrap 为 true 时长行折行显示。
  wrap?: boolean
  // className 追加到最外层容器。
  className?: string
  // testId 便于 harness 断言。
  testId?: string
}

export const CodeView = memo(function CodeView({
  content,
  language,
  showLineNumbers = true,
  wrap = false,
  className = '',
  testId,
}: CodeViewProps) {
  const lines: HighlightLine[] = useMemo(
    () => highlightCode(content, language),
    [content, language],
  )

  return (
    <div className={`min-h-0 flex-1 overflow-auto bg-code ${className}`}>
      <div className="flex min-w-max py-3 font-mono text-xs leading-5">
        {showLineNumbers && (
          <div
            aria-hidden="true"
            data-testid="code-gutter"
            className="sticky left-0 z-10 shrink-0 select-none border-r border-line bg-code px-3 text-right text-subtle"
          >
            {lines.map((_, index) => (
              <div key={index} className="min-h-5">
                {index + 1}
              </div>
            ))}
          </div>
        )}
        <div
          data-testid={testId ?? 'code-content'}
          className={wrap ? 'whitespace-pre-wrap break-words px-3 text-fg' : 'whitespace-pre px-3 text-fg'}
        >
          {lines.map((line, index) => (
            <div key={index} className="min-h-5">
              {line.tokens.map((token, tokenIndex) => (
                <span key={tokenIndex} className={token.cls ?? undefined}>
                  {token.text}
                </span>
              ))}
            </div>
          ))}
        </div>
      </div>
    </div>
  )
})

export default CodeView
