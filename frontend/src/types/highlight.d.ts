// highlight.js 的语言模块没有随包发布类型声明，这里按通配符补一个。
// 只声明用到的形态：语言定义是一个接收 hljs 实例并返回语言描述的函数。
declare module 'highlight.js/lib/languages/*' {
  import type { LanguageFn } from 'highlight.js'

  const language: LanguageFn
  export default language
}
