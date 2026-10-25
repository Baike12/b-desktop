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
// 伪造 Read 工具对里的输入是紧凑 JSON（json.Marshal 产物），带 offset/limit 时
// 还原成 cc 风格的 @path#L10-20 标签。
export function parseUserAttachmentBlock(text: string): UserMention | null {
  if (!text.startsWith('<system-reminder>')) return null
  const command = /<command-name>\/([^<]+)<\/command-name>/.exec(text)
  if (command) return { kind: 'command', label: `/${command[1]}` }
  const read =
    /Called the "Read" tool with the following input: (\{[^\n]*\})/.exec(text)
  if (read) {
    const input = parseJsonObject(read[1])
    if (input && typeof input.file_path === 'string') {
      return { kind: 'file', label: readFileLabel(input) }
    }
    return { kind: 'file', label: '附件' }
  }
  const ls =
    /Called the "Bash" tool with the following input: (\{[^\n]*\})/.exec(text)
  if (ls) {
    const input = parseJsonObject(ls[1])
    const commandText = typeof input?.command === 'string' ? input.command : ''
    const target = commandText.replace(/^ls\s+/, '')
    if (target) return { kind: 'directory', label: unescapeJsonString(target) }
  }
  return { kind: 'file', label: '附件' }
}

// parseJsonObject 安全解析紧凑 JSON 对象。
function parseJsonObject(value: string): Record<string, unknown> | null {
  try {
    const parsed = JSON.parse(value) as unknown
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
      return parsed as Record<string, unknown>
    }
  } catch {
    // 非法 JSON 走兜底标签。
  }
  return null
}

// readFileLabel 按 offset/limit 还原带行范围的展示标签。
// offset 是 0 起始行：#L10-20 对应 offset=9、limit=11。
function readFileLabel(input: Record<string, unknown>): string {
  const path = unescapeJsonString(String(input.file_path))
  const offset = typeof input.offset === 'number' ? input.offset : -1
  const limit = typeof input.limit === 'number' ? input.limit : 0
  if (offset < 0) return path
  const start = offset + 1
  const end = limit > 0 ? offset + limit : null
  return end === null ? `${path}#L${start}` : `${path}#L${start}-${end}`
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
