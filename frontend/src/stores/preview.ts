import { create } from 'zustand'
import {
  DiscardSessionFile,
  GetSessionFileDiff,
  ListSessionChanges,
  ReadSessionFile,
  StageSessionFiles,
  UnstageSessionFiles,
  WriteSessionFile,
} from '../../wailsjs/go/app/App'
import type { app } from '../../wailsjs/go/models'
import { errorMessage, useWorkbenchStore } from './workbench'

// CHANGES_TAB 是右侧面板里「变更审阅」这个特殊 tab 的 id。
// 文件 tab 用工作目录相对路径作 id，两者不会冲突。
export const CHANGES_TAB = '__changes__'

// PreviewFile 是一个已打开文件的预览状态。
export interface PreviewFile {
  path: string
  loading: boolean
  content: string
  size: number
  binary: boolean
  truncated: boolean
  error: string | null
  // draft 是编辑中的内容；null 表示没有未保存的修改。
  draft: string | null
}

// ChangesState 是工作区的 git 变更快照。
export interface ChangesState {
  loading: boolean
  isRepo: boolean
  branch: string
  list: app.FileChangeDTO[]
  error: string | null
}

const EMPTY_CHANGES: ChangesState = {
  loading: false,
  isRepo: false,
  branch: '',
  list: [],
  error: null,
}

// reloadSeq 是刷新文件内容的请求序号，用来丢弃过期的读取结果。
let reloadSeq = 0

export interface PreviewState {
  // tabs 是已打开的 tab id 列表，顺序即展示顺序。
  tabs: string[]
  // active 是当前激活的 tab id。
  active: string | null
  // files 按路径缓存文件内容。
  files: Record<string, PreviewFile>
  // changes 是 git 变更快照。
  changes: ChangesState

  openFile: (path: string) => Promise<void>
  closeTab: (path: string) => void
  renameTab: (from: string, to: string) => void
  setActive: (path: string) => void
  setDraft: (path: string, draft: string) => void
  openChanges: () => Promise<void>
  reloadOpenFiles: () => Promise<void>
  saveFile: (path: string, content: string) => Promise<boolean>
  discardDraft: (path: string) => void
  refreshChanges: () => Promise<void>
  stageChange: (path: string) => Promise<void>
  unstageChange: (path: string) => Promise<void>
  discardChange: (path: string, oldPath: string, staged: boolean) => Promise<void>
}

// sessionContext 返回当前会话的 id 与工作目录；没有会话时返回空值。
function sessionContext(): { sessionId: string; workDir: string } {
  const state = useWorkbenchStore.getState()
  return { sessionId: state.activeId ?? '', workDir: state.workDir }
}

// emptyFile 返回一个处于加载中的空文件状态。
function emptyFile(path: string): PreviewFile {
  return {
    path,
    loading: true,
    content: '',
    size: 0,
    binary: false,
    truncated: false,
    error: null,
    draft: null,
  }
}

export const usePreviewStore = create<PreviewState>((set, get) => ({
  tabs: [],
  active: null,
  files: {},
  changes: EMPTY_CHANGES,

  // openFile 打开一个文件 tab：已打开时只切换过去，否则新建 tab 并读取内容。
  openFile: async (path) => {
    if (!path) return
    const { sessionId, workDir } = sessionContext()
    if (!sessionId || !workDir) return
    const known = get().files[path]
    set((state) => ({
      tabs: state.tabs.includes(path) ? state.tabs : [...state.tabs, path],
      active: path,
      files: {
        ...state.files,
        // 首次打开先放入加载态，界面立刻有反馈。
        [path]: known ?? emptyFile(path),
      },
    }))
    if (known && !known.error && known.draft === null) return
    await get().reloadOpenFiles()
  },

  // closeTab 关闭一个 tab；关掉当前 tab 后激活相邻的一个。
  closeTab: (path) => {
    set((state) => {
      const index = state.tabs.indexOf(path)
      if (index < 0) return state
      const tabs = state.tabs.filter((item) => item !== path)
      const files = { ...state.files }
      delete files[path]
      let active = state.active
      if (active === path) {
        active = tabs[index] ?? tabs[index - 1] ?? null
      }
      return { tabs, files, active }
    })
  },

  // renameTab 在文件被重命名后同步已打开的标签，避免预览指向不存在的路径。
  renameTab: (from, to) =>
    set((state) => {
      if (!state.tabs.includes(from)) return state
      const files = { ...state.files }
      const file = files[from]
      delete files[from]
      files[to] = file ? { ...file, path: to } : emptyFile(to)
      return {
        tabs: state.tabs.map((tab) => (tab === from ? to : tab)),
        files,
        active: state.active === from ? to : state.active,
      }
    }),

  setActive: (path) => set({ active: path }),

  // setDraft 记录编辑中的内容；内容与磁盘一致时视为没有未保存修改。
  // 比较前统一换行符：textarea 里只有 LF，CRLF 文件会因此永远显示「已修改」。
  setDraft: (path, draft) =>
    set((state) => {
      const current = state.files[path]
      if (!current) return state
      const normalized = draft.replace(/\r\n|\r/g, '\n')
      const original = current.content.replace(/\r\n|\r/g, '\n')
      return {
        files: {
          ...state.files,
          [path]: { ...current, draft: normalized === original ? null : draft },
        },
      }
    }),

  // openChanges 打开「变更审阅」tab 并拉取最新的 git 状态。
  openChanges: async () => {
    set((state) => ({
      tabs: state.tabs.includes(CHANGES_TAB) ? state.tabs : [...state.tabs, CHANGES_TAB],
      active: CHANGES_TAB,
    }))
    await get().refreshChanges()
  },

  // reloadOpenFiles 重新读取所有已打开文件的磁盘内容。
  // 文件被 Agent 或用户改动后调用；编辑中的草稿会被保留。
  reloadOpenFiles: async () => {
    const { sessionId, workDir } = sessionContext()
    if (!sessionId || !workDir) return
    const paths = get().tabs.filter((tab) => tab !== CHANGES_TAB)
    if (paths.length === 0) return
    // 序号守卫：只有最后一次刷新的结果会被采纳。
    // 否则「保存后触发的刷新」可能被更早发起的读取覆盖，把旧内容写回去。
    const seq = (reloadSeq += 1)
    await Promise.all(
      paths.map(async (path) => {
        try {
          const file = await ReadSessionFile(sessionId, workDir, path)
          set((state) => {
            if (seq !== reloadSeq) return state
            // 读取期间标签被关掉了：不要把已关闭的文件写回状态。
            if (!state.tabs.includes(path)) return state
            const current = state.files[path]
            // 有未保存草稿时不覆盖正文，避免把用户的编辑冲掉。
            if (current?.draft !== null && current?.draft !== undefined) return state
            return {
              files: {
                ...state.files,
                [path]: {
                  path: file.path || path,
                  loading: false,
                  content: file.content ?? '',
                  size: file.size ?? 0,
                  binary: Boolean(file.binary),
                  truncated: Boolean(file.truncated),
                  error: null,
                  draft: null,
                },
              },
            }
          })
        } catch (err) {
          set((state) => {
            if (seq !== reloadSeq) return state
            if (!state.tabs.includes(path)) return state
            const current = state.files[path] ?? emptyFile(path)
            return {
              files: {
                ...state.files,
                [path]: { ...current, loading: false, error: errorMessage(err) },
              },
            }
          })
        }
      }),
    )
  },

  // saveFile 保存文件内容，成功后清掉草稿并刷新变更列表。
  // 落盘前按原文件的换行风格还原行尾：编辑器里的内容是 LF，
  // 直接写回会把 CRLF 文件悄悄改成 LF。
  saveFile: async (path, content) => {
    const { sessionId, workDir } = sessionContext()
    if (!sessionId || !workDir) return false
    const current = get().files[path]
    const original = current?.content ?? ''
    const usesCRLF = original.includes('\r\n')
    const payload = usesCRLF
      ? content.replace(/\r\n|\r|\n/g, '\n').replace(/\n/g, '\r\n')
      : content.replace(/\r\n|\r/g, '\n')
    try {
      await WriteSessionFile(sessionId, workDir, path, payload)
      set((state) => {
        const file = state.files[path]
        if (!file) return state
        return {
          files: {
            ...state.files,
            [path]: { ...file, content: payload, draft: null, error: null },
          },
        }
      })
      await get().refreshChanges()
      return true
    } catch (err) {
      set((state) => {
        const current = state.files[path]
        if (!current) return state
        return {
          files: {
            ...state.files,
            [path]: { ...current, error: errorMessage(err) },
          },
        }
      })
      return false
    }
  },

  // discardDraft 放弃某个文件尚未保存的编辑。
  discardDraft: (path) =>
    set((state) => {
      const current = state.files[path]
      if (!current) return state
      return { files: { ...state.files, [path]: { ...current, draft: null } } }
    }),

  // refreshChanges 重新拉取工作区的 git 变更。
  refreshChanges: async () => {
    const { sessionId, workDir } = sessionContext()
    if (!sessionId || !workDir) {
      set({ changes: EMPTY_CHANGES })
      return
    }
    set((state) => ({ changes: { ...state.changes, loading: true, error: null } }))
    try {
      const result = await ListSessionChanges(sessionId, workDir)
      set({
        changes: {
          loading: false,
          isRepo: Boolean(result.isRepo),
          branch: result.branch ?? '',
          list: result.changes ?? [],
          error: null,
        },
      })
    } catch (err) {
      set((state) => ({
        changes: { ...state.changes, loading: false, error: errorMessage(err) },
      }))
    }
  },

  // stageChange 把一个文件的变更加入暂存区。
  stageChange: async (path) => {
    const { sessionId, workDir } = sessionContext()
    if (!sessionId || !workDir) return
    try {
      await StageSessionFiles(sessionId, workDir, [path])
      await get().refreshChanges()
    } catch (err) {
      set((state) => ({ changes: { ...state.changes, error: errorMessage(err) } }))
    }
  },

  // unstageChange 把一个文件的变更移出暂存区。
  unstageChange: async (path) => {
    const { sessionId, workDir } = sessionContext()
    if (!sessionId || !workDir) return
    try {
      await UnstageSessionFiles(sessionId, workDir, [path])
      await get().refreshChanges()
    } catch (err) {
      set((state) => ({ changes: { ...state.changes, error: errorMessage(err) } }))
    }
  },

  // discardChange 放弃一个文件的改动；调用方需要先向用户确认。
  // staged 为 true 时连暂存区一起放弃（回到 HEAD），为 false 时只放弃工作区改动。
  // oldPath 是重命名前的路径；非空表示这是一个重命名，会把源路径一并还原。
  discardChange: async (path, oldPath, staged) => {
    const { sessionId, workDir } = sessionContext()
    if (!sessionId || !workDir) return
    try {
      await DiscardSessionFile(sessionId, workDir, path, oldPath ?? '', staged)
      await get().refreshChanges()
      await get().reloadOpenFiles()
    } catch (err) {
      set((state) => ({ changes: { ...state.changes, error: errorMessage(err) } }))
    }
  },
}))

// 工作区文件发生变化时重新读取已打开的文件：
// Agent 写文件、用户在树上新建/重命名/删除，都会递增 workbench 的 filesRevision。
useWorkbenchStore.subscribe((state, previous) => {
  if (state.filesRevision === previous.filesRevision) return
  void usePreviewStore.getState().reloadOpenFiles()
})

// 载入已打开文件的辅助函数放在 store 外部，便于单测直接调用。
export async function readPreviewFile(
  sessionId: string,
  workDir: string,
  path: string,
): Promise<PreviewFile> {
  try {
    const file = await ReadSessionFile(sessionId, workDir, path)
    return {
      path: file.path || path,
      loading: false,
      content: file.content ?? '',
      size: file.size ?? 0,
      binary: Boolean(file.binary),
      truncated: Boolean(file.truncated),
      error: null,
      draft: null,
    }
  } catch (err) {
    return { ...emptyFile(path), loading: false, error: errorMessage(err) }
  }
}

// readFileDiff 读取单个文件的 unified diff，供变更面板展开时调用。
export async function readFileDiff(
  sessionId: string,
  workDir: string,
  path: string,
  staged: boolean,
): Promise<app.FileDiffDTO> {
  return GetSessionFileDiff(sessionId, workDir, path, staged)
}
