import remarkGfm from 'remark-gfm'
import remarkParse from 'remark-parse'
import { unified } from 'unified'

// stableSplit 是把流式文本切成「已稳定前缀」与「仍在增长的后缀」的结果。
export interface StableSplit {
  stable: string
  unstable: string
}

// processor 只用于切分边界，不负责渲染。
// 用 remark-parse + remark-gfm（与 MarkdownText 的渲染管线同源）解析，
// 保证切点落在合法的顶层块边界上：已定型的内容单独渲染不会改变语义。
const processor = unified().use(remarkParse).use(remarkGfm)

// splitStablePrefix 在最后一个顶层块的起始位置切开流式文本。
//
// 流式输出时只有最后一个块还在增长，前面的块已经定型。已定型的部分不再
// 重新解析，只有增长中的后缀随增量重解析，避免长回复时每帧重解析全文。
//
// 未闭合的代码围栏会被 remark 解析成一个代码块节点，切点落在围栏之前，
// 整个代码块随增量重解析，不会把围栏内部切成两段。
export function splitStablePrefix(text: string): StableSplit {
  if (text === '') return { stable: '', unstable: '' }

  const root = processor.parse(text)
  const children = root.children
  if (children.length === 0) return { stable: '', unstable: text }

  // 最后一个顶层节点是正在增长的部分；它之前的内容都已定型。
  const last = children[children.length - 1]
  const start = last.position?.start?.offset
  if (typeof start !== 'number' || start <= 0) {
    return { stable: '', unstable: text }
  }
  return { stable: text.slice(0, start), unstable: text.slice(start) }
}
