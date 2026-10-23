import { createContext } from 'react'

// MarkdownStreamingContext 告诉代码块：当前是否仍在流式接收文本。
// Mermaid 在文本完整前不解析，避免每个增量都触发一次昂贵且有语法错误的渲染。
export const MarkdownStreamingContext = createContext(false)
