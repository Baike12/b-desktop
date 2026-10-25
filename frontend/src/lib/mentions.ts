import type { UserMention } from '../types/chat'

// 用户消息的 @ 引用与 / 命令解析。
//
// 后端（internal/app/app_mention.go、app_slash.go）把 @path 与 /command 展开
// 成 system-reminder 附件块追加在用户原文之后；这里提供两个方向的解析：
// parseUserMentions 从用户原文做发送前的乐观展示，
// parseUserAttachmentBlock 从 transcript 的附件块做会话重放展示，
// 两者的输出结构一致。

// atMentionRe 匹配行首或空白后的 @token，与后端正则保持一致；
// token 支持无空格路径或双引号包裹的含空格路径。
const atMentionRe = /(?:^|\s)@("(?:[^"\\]|\\.)*"|[^\s]+)/g

// commandNameRe 校验 / 后第一个 token 形如命令名，与后端 slashCommandNameRe 一致。
const commandNameRe = /^[A-Za-z0-9_][A-Za-z0-9_.\-]*$/

// parseUserMentions 从用户输入原文提取 / 命令与 @ 引用。
// 发送时无法确知路径是文件还是目录，统一按 file 呈现；重放阶段由
// parseUserAttachmentBlock 给出准确分类。
export function parseUserMentions(text: string): UserMention[] {
  const out: UserMention[] = []
  const seen = new Set<string>()
  const trimmed = text.trim()
  if (trimmed.startsWith('/')) {
    const name = trimmed.slice(1).split(/\s/, 1)[0] ?? ''
    if (commandNameRe.test(name)) {
      out.push({ kind: 'command', label: `/${name}` })
      seen.add(name)
    }
  }
  for (const match of trimmed.matchAll(atMentionRe)) {
    const token = match[1]
    const path = unquoteAtToken(token)
    if (!path || seen.has(path)) continue
    seen.add(path)
    out.push({ kind: 'file', label: path })
  }
  return out
}

// parseUserAttachmentBlock 从 user 消息的一个 system-reminder 文本块解析附件
// 摘要；返回 null 表示不是附件块，应并入正文渲染。
export function parseUserAttachmentBlock(text: string): UserMention | null {
  if (!text.startsWith('<system-reminder>')) return null
  const command = /<command-name>\/([^<]+)<\/command-name>/.exec(text)
  if (command) return { kind: 'command', label: `/${command[1]}` }
  const read =
    /Called the "Read" tool with the following input: \{"file_path": "((?:[^"\\]|\\.)*)"\}/.exec(text)
  if (read) return { kind: 'file', label: unescapeJsonString(read[1]) }
  const ls =
    /Called the "Bash" tool with the following input: \{"command": "ls ((?:[^"\\]|\\.)*)"/.exec(text)
  if (ls) return { kind: 'directory', label: unescapeJsonString(ls[1]) }
  return { kind: 'file', label: '附件' }
}

// unquoteAtToken 去掉 @token 的成对双引号并处理转义。
function unquoteAtToken(token: string): string {
  if (token.length >= 2 && token.startsWith('"') && token.endsWith('"')) {
    return token.slice(1, -1).replace(/\\"/g, '"')
  }
  return token
}

// unescapeJsonString 解开 JSON 字符串字面量（后端块里路径经 %q 转义）。
function unescapeJsonString(value: string): string {
  try {
    return JSON.parse(`"${value}"`) as string
  } catch {
    return value
  }
}
