// 代码语法高亮：扩展名识别 + highlight.js 分词。
//
// 高亮库与 markdown 代码块共用 highlight.js，token 颜色沿用 style.css 里已有的
// .hljs-* 规则，明暗主题切换时自动跟随，不需要第二套配色。
//
// 这里用 highlight.js 的 core 构建 + 按需注册语言，而不是整包引入：
// 整包含 380 多种语言，注册用不到的部分会白白进入产物。

import hljs from 'highlight.js/lib/core'
import type { LanguageFn } from 'highlight.js'

import bash from 'highlight.js/lib/languages/bash'
import c from 'highlight.js/lib/languages/c'
import clojure from 'highlight.js/lib/languages/clojure'
import cmake from 'highlight.js/lib/languages/cmake'
import cpp from 'highlight.js/lib/languages/cpp'
import csharp from 'highlight.js/lib/languages/csharp'
import css from 'highlight.js/lib/languages/css'
import dart from 'highlight.js/lib/languages/dart'
import diff from 'highlight.js/lib/languages/diff'
import dockerfile from 'highlight.js/lib/languages/dockerfile'
import elixir from 'highlight.js/lib/languages/elixir'
import erlang from 'highlight.js/lib/languages/erlang'
import fsharp from 'highlight.js/lib/languages/fsharp'
import go from 'highlight.js/lib/languages/go'
import graphql from 'highlight.js/lib/languages/graphql'
import groovy from 'highlight.js/lib/languages/groovy'
import haskell from 'highlight.js/lib/languages/haskell'
import http from 'highlight.js/lib/languages/http'
import ini from 'highlight.js/lib/languages/ini'
import java from 'highlight.js/lib/languages/java'
import javascript from 'highlight.js/lib/languages/javascript'
import json from 'highlight.js/lib/languages/json'
import julia from 'highlight.js/lib/languages/julia'
import kotlin from 'highlight.js/lib/languages/kotlin'
import latex from 'highlight.js/lib/languages/latex'
import less from 'highlight.js/lib/languages/less'
import lua from 'highlight.js/lib/languages/lua'
import makefile from 'highlight.js/lib/languages/makefile'
import markdown from 'highlight.js/lib/languages/markdown'
import nginx from 'highlight.js/lib/languages/nginx'
import nim from 'highlight.js/lib/languages/nim'
import objectivec from 'highlight.js/lib/languages/objectivec'
import ocaml from 'highlight.js/lib/languages/ocaml'
import perl from 'highlight.js/lib/languages/perl'
import php from 'highlight.js/lib/languages/php'
import powershell from 'highlight.js/lib/languages/powershell'
import properties from 'highlight.js/lib/languages/properties'
import protobuf from 'highlight.js/lib/languages/protobuf'
import python from 'highlight.js/lib/languages/python'
import r from 'highlight.js/lib/languages/r'
import ruby from 'highlight.js/lib/languages/ruby'
import rust from 'highlight.js/lib/languages/rust'
import scala from 'highlight.js/lib/languages/scala'
import scss from 'highlight.js/lib/languages/scss'
import sql from 'highlight.js/lib/languages/sql'
import swift from 'highlight.js/lib/languages/swift'
import typescript from 'highlight.js/lib/languages/typescript'
import vim from 'highlight.js/lib/languages/vim'
import xml from 'highlight.js/lib/languages/xml'
import yaml from 'highlight.js/lib/languages/yaml'

import type { DiffLine } from './diff'

// 注册需要的语言。语言名与 highlight.js 的 id 保持一致。
const LANGUAGES: Record<string, LanguageFn> = {
  bash,
  c,
  clojure,
  cmake,
  cpp,
  csharp,
  css,
  dart,
  diff,
  dockerfile,
  elixir,
  erlang,
  fsharp,
  go,
  graphql,
  groovy,
  haskell,
  http,
  ini,
  java,
  javascript,
  json,
  julia,
  kotlin,
  latex,
  less,
  lua,
  makefile,
  markdown,
  nginx,
  nim,
  objectivec,
  ocaml,
  perl,
  php,
  powershell,
  properties,
  protobuf,
  python,
  r,
  ruby,
  rust,
  scala,
  scss,
  sql,
  swift,
  typescript,
  vim,
  xml,
  yaml,
}

for (const [name, definition] of Object.entries(LANGUAGES)) {
  hljs.registerLanguage(name, definition)
}

// EXTENSION_LANGUAGES 把文件扩展名映射到 highlight.js 语言名。
// 未命中的扩展名返回 null，按纯文本渲染。
const EXTENSION_LANGUAGES: Record<string, string> = {
  bash: 'bash',
  c: 'c',
  cc: 'cpp',
  cjs: 'javascript',
  clj: 'clojure',
  cljs: 'clojure',
  cmake: 'cmake',
  coffee: 'javascript',
  cpp: 'cpp',
  cs: 'csharp',
  css: 'css',
  cts: 'typescript',
  cxx: 'cpp',
  dart: 'dart',
  diff: 'diff',
  dockerfile: 'dockerfile',
  el: 'elixir',
  elm: 'javascript',
  erl: 'erlang',
  ex: 'elixir',
  exs: 'elixir',
  fs: 'fsharp',
  fsx: 'fsharp',
  go: 'go',
  gql: 'graphql',
  graphql: 'graphql',
  groovy: 'groovy',
  h: 'c',
  hpp: 'cpp',
  hs: 'haskell',
  htm: 'xml',
  html: 'xml',
  hxx: 'cpp',
  ini: 'ini',
  java: 'java',
  js: 'javascript',
  json: 'json',
  jsonc: 'json',
  jsx: 'javascript',
  julia: 'julia',
  kt: 'kotlin',
  kts: 'kotlin',
  less: 'less',
  lua: 'lua',
  m: 'objectivec',
  markdown: 'markdown',
  md: 'markdown',
  mdx: 'markdown',
  ml: 'ocaml',
  mm: 'objectivec',
  mts: 'typescript',
  nim: 'nim',
  patch: 'diff',
  php: 'php',
  pl: 'perl',
  properties: 'properties',
  proto: 'protobuf',
  ps1: 'powershell',
  py: 'python',
  r: 'r',
  rb: 'ruby',
  rs: 'rust',
  scala: 'scala',
  scss: 'scss',
  sh: 'bash',
  sql: 'sql',
  svelte: 'xml',
  swift: 'swift',
  tf: 'ini',
  tfvars: 'ini',
  toml: 'ini',
  ts: 'typescript',
  tsx: 'typescript',
  vim: 'vim',
  vue: 'xml',
  xml: 'xml',
  yaml: 'yaml',
  yml: 'yaml',
  zsh: 'bash',
}

// FILENAME_LANGUAGES 按完整文件名识别没有扩展名的文件。
const FILENAME_LANGUAGES: Record<string, string> = {
  dockerfile: 'dockerfile',
  gemfile: 'ruby',
  makefile: 'makefile',
  rakefile: 'ruby',
}

// detectLanguage 按文件路径推断高亮语言；无法识别时返回 null（按纯文本渲染）。
export function detectLanguage(path: string): string | null {
  const name = path.split(/[\\/]/).pop() ?? path
  const lower = name.toLowerCase()
  const byName = FILENAME_LANGUAGES[lower]
  if (byName) return byName
  // Dockerfile.x 之类的变体。
  if (lower.startsWith('dockerfile.')) return 'dockerfile'
  if (lower.startsWith('makefile.')) return 'makefile'
  const dot = lower.lastIndexOf('.')
  if (dot <= 0 || dot === lower.length - 1) return null
  return EXTENSION_LANGUAGES[lower.slice(dot + 1)] ?? null
}

// HighlightToken 是一个高亮片段。cls 为 null 表示没有语义着色。
export interface HighlightToken {
  text: string
  cls: string | null
}

// HighlightLine 是一行代码的 token 序列。
export interface HighlightLine {
  tokens: HighlightToken[]
}

// MAX_HIGHLIGHT_LINES 限制单次高亮的行数，超出部分按纯文本渲染。
const MAX_HIGHLIGHT_LINES = 20000

// ENTITY_REPLACEMENTS 是 highlight.js 输出里需要还原的 HTML 实体。
const ENTITY_REPLACEMENTS: Record<string, string> = {
  '&amp;': '&',
  '&lt;': '<',
  '&gt;': '>',
  '&quot;': '"',
  '&#x27;': "'",
  '&#39;': "'",
  '&apos;': "'",
}

// decodeEntities 把高亮输出里的 HTML 实体还原成原字符。
// React 渲染文本时会自行转义，这里必须先解码，否则页面上会显示实体本身。
function decodeEntities(text: string): string {
  return text.replace(/&(?:amp|lt|gt|quot|#x27|#39|apos);/g, (entity) => {
    return ENTITY_REPLACEMENTS[entity] ?? entity
  })
}

// parseHighlightHtml 把 highlight.js 的 HTML 输出切成带类名的 token 序列。
// 只解析 span 标签：highlight.js 的输出除 span 外不含其它标签，文本已被转义。
function parseHighlightHtml(html: string): HighlightToken[] {
  // 正则带 g 标志，必须每次调用新建，避免 lastIndex 在多次调用间残留。
  const spanRe = /<span class="([^"]*)">|<\/span>/g
  const tokens: HighlightToken[] = []
  const stack: string[] = []
  let cursor = 0
  let match = spanRe.exec(html)
  while (match !== null) {
    if (match.index > cursor) {
      pushToken(tokens, html.slice(cursor, match.index), stack)
    }
    if (match[1] !== undefined) {
      stack.push(match[1])
    } else {
      stack.pop()
    }
    cursor = match.index + match[0].length
    match = spanRe.exec(html)
  }
  if (cursor < html.length) {
    pushToken(tokens, html.slice(cursor), stack)
  }
  return tokens
}

// pushToken 追加一个 token；与前一个 token 类名相同时合并，避免产生碎 span。
function pushToken(tokens: HighlightToken[], raw: string, stack: string[]): void {
  const text = decodeEntities(raw)
  if (text === '') return
  const cls = stack.length > 0 ? stack.join(' ') : null
  const last = tokens[tokens.length - 1]
  if (last && last.cls === cls) {
    last.text += text
    return
  }
  tokens.push({ text, cls })
}

// splitTokensIntoLines 把 token 序列按换行拆成行。
function splitTokensIntoLines(tokens: HighlightToken[]): HighlightLine[] {
  const lines: HighlightLine[] = [{ tokens: [] }]
  for (const token of tokens) {
    const parts = token.text.split('\n')
    for (let i = 0; i < parts.length; i += 1) {
      if (i > 0) lines.push({ tokens: [] })
      if (parts[i] !== '') {
        lines[lines.length - 1].tokens.push({ text: parts[i], cls: token.cls })
      }
    }
  }
  return lines
}

// plainLines 把纯文本按行切开。
function plainLines(code: string): HighlightLine[] {
  return code.split('\n').map((text) => ({ tokens: text === '' ? [] : [{ text, cls: null }] }))
}

// highlightCode 对代码分词并按行返回。language 为 null 时按纯文本处理。
export function highlightCode(code: string, language: string | null): HighlightLine[] {
  // CRLF / CR 统一成 LF：按行渲染时，行尾的 CR 会被当成额外的换行，行距会翻倍。
  const normalized = code.replace(/\r\n?/g, '\n')
  if (language === null || language === '') return plainLines(normalized)
  if (!hljs.getLanguage(language)) return plainLines(normalized)
  if (normalized.split('\n').length > MAX_HIGHLIGHT_LINES) return plainLines(normalized)
  try {
    const result = hljs.highlight(normalized, { language, ignoreIllegals: true })
    return splitTokensIntoLines(parseHighlightHtml(result.value))
  } catch {
    // 高亮失败不应让预览整体报错：退回纯文本。
    return plainLines(normalized)
  }
}

// highlightDiff 对 unified diff 的每一行给出高亮 token。
//
// 做法：先把 diff 还原成「旧版本」与「新版本」两段完整文本，分别整体高亮，
// 再按 diff 行的增删关系把 token 映射回每一行。这样块注释、模板字符串等
// 跨行结构也能正确着色，而不是逐行匹配导致的断裂。
export function highlightDiff(lines: DiffLine[], language: string | null): (HighlightToken[] | null)[] {
  const out: (HighlightToken[] | null)[] = lines.map(() => null)
  if (language === null || language === '') return out

  const oldParts: string[] = []
  const newParts: string[] = []
  const oldAt: number[] = new Array(lines.length).fill(-1)
  const newAt: number[] = new Array(lines.length).fill(-1)
  for (let i = 0; i < lines.length; i += 1) {
    const line = lines[i]
    if (line.kind === 'context') {
      oldAt[i] = oldParts.length
      oldParts.push(line.text.slice(1))
      newAt[i] = newParts.length
      newParts.push(line.text.slice(1))
    } else if (line.kind === 'del') {
      oldAt[i] = oldParts.length
      oldParts.push(line.text.slice(1))
    } else if (line.kind === 'add') {
      newAt[i] = newParts.length
      newParts.push(line.text.slice(1))
    }
  }

  const oldLines = highlightCode(oldParts.join('\n'), language)
  const newLines = highlightCode(newParts.join('\n'), language)
  for (let i = 0; i < lines.length; i += 1) {
    const line = lines[i]
    if (line.kind === 'context') {
      out[i] = oldLines[oldAt[i]]?.tokens ?? newLines[newAt[i]]?.tokens ?? null
    } else if (line.kind === 'del') {
      out[i] = oldLines[oldAt[i]]?.tokens ?? null
    } else if (line.kind === 'add') {
      out[i] = newLines[newAt[i]]?.tokens ?? null
    }
  }
  return out
}
