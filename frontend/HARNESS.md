# 前端 harness（浏览器验证）

不启动 Wails，也不操作真实鼠标键盘。Vite 直接提供 `harness.html`，页面挂载**真实的 `<App/>`**（组件、store、reducer 全是生产代码），只把两层 Wails 接口替换成内存假实现：

- `window.go.app.App.*`：`ListSessions` / `ListProjects` / `OpenProject` / `AddProject` / `LoadSession` / `CreateSession` / `SendMessage` / `CancelTurn` / `ApproveTool` / `SetPermissionMode` / `GetProviderConfig`
- 右侧文件面板：`ListSessionFiles` / `ReadSessionFile` / `ReadSessionImage` / `SearchSessionFiles` / `CreateSessionFile` / `RenameSessionFile` / `MoveSessionFile` / `DeleteSessionFile` / `WriteSessionFile` / `ListSessionChanges` / `GetSessionFileDiff` / `StageSessionFiles` / `UnstageSessionFiles` / `DiscardSessionFile`
- `window.runtime.*`：`EventsOnMultiple` / `EventsOff` / `WindowSet*Theme` / `Environment`

假后端带三个项目（`bdesk-harness` 有会话、`bdesk-empty` 空分组、`bdesk-demo` 由 `OpenProject` 动态加入）、一份固定的 markdown 会话和一份空会话；`SendMessage` 会按 16ms 一帧推送 `text_delta`，走真实的事件归并链路。

文件面板的假后端维护一份内存目录树与文件内容：新建、重命名、移动、删除会真实改这份假树，搜索按文件名子串匹配，变更列表固定两条（`src/App.tsx` 修改 + `main.go` 未跟踪）。

## 启动

```bash
cd frontend
npm run harness        # 5173 是 strictPort；先探测端口，已占用就直接复用
# 打开 http://localhost:5173/harness.html
```

产物构建（`npm run build`）只以 `index.html` 为入口，`harness.html` 不会进入 `frontend/dist`，不会打进 Wails 二进制。

## 断言钩子

| 钩子 | 说明 |
|---|---|
| `await window.__bdeskReady` | 等 `init()` 与首个会话加载完成 |
| `window.__bdesk.metrics()` | 一次取回 DOM/状态指标（见下） |
| `window.__bdesk.send(text)` | 走真实 `SendMessage`，触发模拟流式回复 |
| `window.__bdesk.emit(events)` | 注入任意 `agent:event`（数组或单条） |
| `window.__bdesk.select(id)` | 切换会话 |
| `window.__bdesk.openProject()` | 走真实 `OpenProject`，模拟选中 `/tmp/bdesk-demo` |
| `window.__bdesk.newSession(path?)` | 在指定项目里新建会话 |
| `window.__bdesk.setTheme('light'｜'dark'｜'system')` | 切换主题 |
| `window.__bdesk.openSettings()` / `closeSettings()` | 打开/关闭设置页 |
| `window.__bdesk.toggleSidebar()` | 折叠/展开侧边栏 |
| `window.__bdesk.setPermissionMode(mode)` | 切换会话权限模式 |
| `window.__bdesk.openFile(path)` | 在右侧预览面板打开一个文件 |
| `window.__bdesk.closePreview()` | 关闭预览面板的当前标签 |
| `window.__bdesk.openChanges()` | 打开「变更」审阅标签 |
| `window.__bdesk.text()` / `bootErrors()` | 页面文本 / 启动期错误 |

`metrics()` 覆盖：`theme`、`settingsOpen`、`usage`、`usageTooltip`、`usageInsideComposer`、`statusBarPresent`、`markdownRoots`、`headings`、`codeBlocks`、`highlightedTokens`、`tables`、`inlineCode`、`links`、`toolCards`、`items`、`running`、`projectCount`、`projectNames`、`projectSessionCounts`、`orphanRows`、`hasOpenProjectButton`、`hasNewTaskButton`、`welcome`、`heroComposer`、`modelContext`、`modelContextTitle`、`contextTokens`、`contextWindow`、`model`、`headerText`、`enterHintPresent`、`sidebarCollapsed`、`sidebarWidth`、`settingsModelValue`、`settingsContextValue`、`settingsSaveDisabled`、`mermaidBlocks`、`mermaidSvgs`、`mermaidErrors`、`mermaidStreaming`、`mermaidErrorText`、`mermaidFullscreen`、`mermaidFullscreenSvg`。

右侧文件面板另有：`fileRows`、`filePaths`、`filePreview`、`filePreviewImage`、`filePreviewPath`、`filePreviewMarkdown`、`markdownViewMode`、`filePreviewLines`、`previewPaneWidth`、`previewTabs`、`previewTokens`、`changesPanel`、`changeRows`、`reviewDiff`、`diffTokens`。

## 已验证的基线

- Markdown：`headings` / `codeBlocks` / `tables` / `inlineCode` / `links` 均大于 0，代码块含高亮 token。
- Mermaid：合法代码块渲染为 `[data-testid="mermaid"] .mermaid-svg > svg`（`mermaidSvgs === 1`），非法代码块回退为 `mermaid-error`（`mermaidErrors === 1`）；流式期间只出现 `mermaid-streaming` 占位、不产生 SVG，结束后才渲染；切换明暗主题会按新配色重绘。点击图表任意位置后 `mermaidFullscreen === true`，图表按 SVG 逻辑尺寸（1:1）渲染、字号与正文一致，支持滚轮缩放、拖动平移与双击复位，按 Esc 关闭并恢复页面滚动。
- Markdown 文件预览：打开 `README.md` 默认渲染（`filePreviewMarkdown === true`、`markdownViewMode === 'rendered'`），标题、列表与代码块复用会话的 `MarkdownText`；切到「原文」后 `markdownViewMode === 'source'` 并出现 `file-preview-content`；关闭标签再打开恢复默认渲染。
- 输入框位置：新会话 `welcome === true` 且 `heroComposer === true`，卡片中心与内容区中心偏差小于 10px；`send()` 后 `welcome === false`，输入框贴底。
- 模型与上下文：模型名只在输入框内（`headerText` 不含模型），`modelContext` 形如 `deepseek-flash32.4k/128k`，`contextTokens` 来自会话最后一次调用；`enterHintPresent === false`。
- 设置页：打开后回填 `settingsModelValue` / `settingsContextValue`；修改并保存后 `model`、`contextWindow`、`modelContext` 同步更新。
- 侧边栏：`toggleSidebar()` 后宽度 71px，仍保留展开、新建任务、设置按钮；刷新后折叠状态保持。拖动 `sidebar-resize` 或双击后宽度变化并写入 `b-desktop:sidebar-width`。
- 主题：`setTheme('dark')` 后 `document.documentElement.dataset.theme === 'dark'`；设置页文本包含三档外观。
- 流式：`send()` 中 `running === true`，结束后 `running === false` 且累计用量与 `contextTokens` 更新。
- 项目：初始 `projectNames === ['bdesk-harness', 'bdesk-empty']`、`projectSessionCounts === [2, 0]`，会话按 workDir 归组，未命中项目的会话出现在“其他会话”（`orphanRows === true`）；`openProject()` 后新增 `bdesk-demo` 并自动建会话；`newSession('/tmp/bdesk-empty')` 后该组会话数变为 1。
- 右侧面板：预览在左、文件树在右，`previewPaneWidth` 大于 0 且对话区仍可见；点文件后 `filePreviewPath` 为该路径、`previewTabs` 加一；打开 `assets/logo.png` 时 `filePreviewImage === true`。
- 语法高亮：`main.go` / `src/main.rs` / `src/widget.c` / `src/App.tsx` 打开后 `previewTokens` 均大于 0（关键字、字符串、标题、注释分别着色）。
- 变更审阅：`openChanges()` 后 `changesPanel === true`、`changeRows === 2`；展开某个文件后 `reviewDiff === true` 且 `diffTokens` 大于 0。
- 文件操作：新建文件后树里出现新条目并自动打开；重命名、移动、删除都会刷新树并给出提示；删除需要二次确认。
- 启动期 `bootErrors()` 为空。

## 约束

- 浏览器交互与截图一律走 browseros；不要用系统级鼠标/键盘事件验证，会和用户正在做的工作冲突。
- 新增断言钩子加在 `src/dev/harness.tsx`，模式照抄现有钩子。
