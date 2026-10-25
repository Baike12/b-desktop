# 右侧工作区面板（文件预览 / 文件树 / 变更审阅）

右侧区域是桌面端的「工作区面板」：左边是文件预览，右边是文件树。中间对话区始终保持挂载，
打开文件不会把对话挤走。

```text
┌──────────┬────────────────┬──────────────────────┬──────────────┐
│ 项目列表 │     对话区      │      文件预览        │   文件树     │
│          │                │  [变更] [go.mod ×]    │  搜索…        │
│          │                │  ┌──────────────────┐ │  ▸ src       │
│          │                │  │ 1 package main   │ │    main.go   │
│          │                │  │ 2 import "fmt"   │ │    app.tsx   │
└──────────┴────────────────┴──────────────────────┴──────────────┘
                            ↕ 可拖动            ↕ 可拖动
```

## 模块划分

| 职责 | 位置 |
|---|---|
| 面板布局（预览 + 文件树） | `frontend/src/App.tsx` |
| 预览面板：tab 栏、宽度拖动、Markdown 渲染/原文切换、编辑态 | `frontend/src/components/PreviewPane.tsx` |
| 文件树：懒加载、右键菜单、拖拽移动 | `frontend/src/components/FileTree.tsx` |
| 变更列表与单文件差异 | `frontend/src/components/ChangesPanel.tsx` |
| 带高亮的 diff 渲染 | `frontend/src/components/ReviewDiff.tsx` |
| 代码渲染 / 编辑 | `frontend/src/components/CodeView.tsx`、`CodeEditor.tsx` |
| 语法高亮 | `frontend/src/lib/highlight.ts` |
| diff 解析与行级比较 | `frontend/src/lib/diff.ts` |
| 面板状态（tab、文件内容、变更） | `frontend/src/stores/preview.ts` |
| 文件读写与 git 视图 | `internal/workspace/mutate.go`、`changes.go`、`search_files.go` |
| 绑定层 | `internal/app/app_files.go`、`internal/app/app_changes.go` |

## 语法高亮

用 highlight.js 的 **core + 按需注册语言**，与 markdown 代码块（`rehype-highlight`）共用同一套
token 配色（`frontend/src/style.css` 里的 `.hljs-*` 规则），明暗主题自动跟随。

- 语言识别：`lib/highlight.ts` 的 `EXTENSION_LANGUAGES` / `FILENAME_LANGUAGES`，覆盖 Go、TS/TSX、
  JS、Python、Java、Rust、C/C++、C#、Kotlin、Swift、Ruby、PHP、Lua、Shell、SQL、JSON/YAML/TOML、
  HTML/XML、CSS/SCSS、Markdown、Dockerfile、Makefile 等主流语言；识别不到时按纯文本渲染。
- 渲染结构：整段代码一次性分词，再按行切开，每行是一个块级 `<div>`，token 用 `<span>` 承载。
  块级元素复制时会保留换行，行号列 `select-none` 不会进剪贴板。
- diff 高亮：先把 diff 还原成「改动前」「改动后」两段完整文本分别整体高亮，再按增删关系把 token
  映射回每一行。跨行的块注释与模板字符串因此不会断裂（逐行匹配会断）。

## Markdown 预览

`.md` / `.markdown` 文件默认按渲染态打开，直接复用会话侧的
`frontend/src/components/MarkdownText.tsx`：GFM、表格、代码高亮、Mermaid 的行为与对话里完全一致，
不维护第二套 Markdown 管线。

- 顶部工具栏的「渲染 / 原文」分段按钮切换当前 tab；「原文」走带行号的 `CodeView`，编辑入口也直接进源码。
- 显示方式只保存在 `PreviewPane` 的内存状态里：切换 tab 保留，关闭 tab 或切换会话后恢复默认渲染。
- `.mdx` 不参与渲染，避免把 JSX 当正文；仍按 `markdown` 语言做源码高亮。

## 变更审阅

后端全部走 `git` CLI（`internal/workspace/changes.go`）：

| 操作 | 命令 |
|---|---|
| 变更列表 | `git status --porcelain=v1 -z --untracked-files=all` |
| 行数统计 | `git diff --numstat -z`（工作区）/ `git diff --cached --numstat -z`（暂存区） |
| 单文件差异 | `git diff --no-color --no-ext-diff --find-renames -U3 -- <path>` |
| 暂存 / 取消暂存 | `git add -A -- <path>` / `git restore --staged -- <path>` |
| 放弃改动（暂存区 + 工作区） | `git restore --source=HEAD --staged --worktree -- <path>` |
| 放弃改动（仅工作区） | `git restore --worktree -- <path>` |

- git 输出的路径始终相对仓库根，`pathPrefix` 负责换算成相对会话工作目录的路径。
- 未跟踪文件没有 git 差异，按「全部新增」合成一份 unified diff。
- 交互：点文件展开差异；「暂存 / 取消暂存」就地完成；「放弃」需要二次确认（不可撤销）。
- 放弃的粒度跟着行走的区段走：已暂存区的行放弃后回到 HEAD，未暂存的行只把工作区改回暂存区内容。
- HEAD 里没有的文件（新增文件）无法「回到某个版本」：移出暂存区并从磁盘删除。

## 文件写操作

`internal/workspace/mutate.go` 提供新建、重命名、移动、删除与写入。所有路径都限制在工作目录内：
词法越界、符号链接指向根外、目标已存在都会被拒绝。

- 新建/重命名/移动用的是 `O_EXCL` / `Lstat` 预检，**不会覆盖**既有内容。
- 写入先写临时文件再 `os.Rename` 替换，并保留原文件权限位（可执行脚本不会被改成 0600）。
- 删除是**永久删除**，UI 上必须二次确认；目录会连同内容一起删除，符号链接只删链接本身。

## 状态同步

workbench store 的 `filesRevision` 是唯一的失效信号：

```text
Agent 写文件 / 用户在树上新建·重命名·移动·删除
  └─ bumpFilesRevision()
       ├─ 文件树：重载根目录与已展开目录
       └─ 预览面板：重新读取已打开的文件（有未保存草稿的文件不覆盖）
```

## 与参考实现（ZCode）的差异

- ZCode 用 shiki + `@pierre/diffs`（Worker 内跑 oniguruma WASM）；本项目用 highlight.js，与已有
  的 markdown 高亮共用配色与依赖，产物更小、同步渲染、无需 Worker。
- ZCode 的文件树是**只读**的（没有新建/重命名/删除/拖拽移动）；本项目按编辑器惯例补齐了这。
- ZCode 的变更审阅是独立 tab（GitPane），且**没有** hunk 级接受/拒绝；本项目同样是 tab 形态，
  接受/拒绝的粒度是文件级（暂存 / 放弃）。
