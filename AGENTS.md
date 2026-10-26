# AGENTS.md

## 项目是什么

`b-desktop` 是桌面 AI 工作台，采用 **Wails v2 + 系统 WebView**：会话、模型调用、工具执行、MCP、存储等能力收敛到一个 Go 二进制里，React 只负责界面。

要复刻的核心能力（按重要性排序）：

1. **Agent 运行时**：模型流式循环、工具调用与编排、权限审批、上下文压缩、会话恢复
2. **Agent 扩展**：SubAgent / Agent Teams、Skills、记忆、MCP、Hooks
3. 多会话工作台、会话历史、全局搜索、分支 / Worktree 启动
4. 工具调用与任务进度展示、Diff 审阅、内置浏览器预览
5. 五档权限模式与图形化审批
6. 模型自选（官方账号、第三方预设、本地端点）
7. MCP / SubAgent / Skills 可视化管理
8. 模型请求追踪、用量统计
9. Computer Use、桌面宠物、多主题
10. H5 远程访问、IM 接入（Telegram / 飞书 / 微信 / 钉钉 / WhatsApp）

当前已实现 Agent 运行时、扩展体系（MCP / Skills / Hooks / SubAgent）、工作台与会话持久化、右侧工作区面板（文件预览 + 语法高亮 + 文件操作 + git 变更审阅）；本地 Server、H5、IM 适配等能力按文末顺序继续补齐。

右侧工作区面板的架构与约定见 `docs/workspace-panel.md`。

## Agent 运行时（项目核心）

本项目的 Agent 不是自研的简单聊天循环，而是按完整编码 Agent 的行为模型实现。同级目录 `../cc` 是**行为参考实现**（Bun + TypeScript）；本项目用 Go 重新实现同样的行为，不逐行翻译其代码。

核心链路（对应 `../cc/src`）：

```text
用户输入
  └─ QueryEngine.submitMessage            会话级编排：系统提示、记忆、hooks、落盘
      └─ query/queryLoop                  单轮循环
          ├─ 每次模型调用前：token 预算检查 → 微压缩/自动压缩
          ├─ callModel 流式请求            text / thinking / tool_use 增量
          ├─ 收集 tool_use
          ├─ runTools                     只读工具并行（默认上限 10），写工具串行
          │    └─ canUseTool              allow / ask / deny，ask 推给前端审批
          ├─ tool_result 回灌消息历史
          └─ 没有新的 tool_use → 本轮结束；否则继续下一轮（受 maxTurns 约束）
```

必须保持的行为约束：

1. **工具调用是循环唯一的推进信号。** 模型返回 `tool_use` 就执行并把 `tool_result` 回灌；没有 `tool_use` 才结束本轮。
2. **只读工具并行、写工具串行。** 按 `isReadOnly` / `isConcurrencySafe` 分组；每个 `tool_use` 必须有且只有一个对应的 `tool_result`，拒绝和异常也要生成结果。
3. **权限检查在工具执行之前。** 决策只有 `allow` / `ask` / `deny`；`ask` 通过 Wails 事件交给前端审批，等待用户选择，禁止默认放行。
4. **错误不能打断循环。** 工具错误、参数校验失败、模型请求失败都转成结构化结果/事件回灌，由模型决定下一步。
5. **流式事件逐条转发。** 文本增量、thinking、工具开始/进度/结果、usage、压缩边界都要实时推给前端，不能等整轮结束再一次性返回。
6. **上下文压缩自动执行。** 每次模型调用前检查 token 预算，超过阈值先微压缩、再自动压缩；压缩边界之后保留最近消息，保证可继续对话。
7. **SubAgent 使用独立上下文。** 子 Agent 有自己的消息历史、工具池和权限范围，结果汇总回父 Agent；支持后台运行、取消和恢复。
8. **会话 transcript 追加持久化。** 支持断点恢复；模型密钥、系统提示等敏感内容不下发前端。

### 模块对照

| 职责 | TypeScript 参考 | Go 目标包（规划） |
|---|---|---|
| 会话编排 | `src/QueryEngine.ts` | `internal/agent` |
| 单轮循环 / 流式事件 | `src/query.ts` | `internal/agent` |
| Tool 契约与注册 | `src/Tool.ts`、`src/tools.ts` | `internal/tool` |
| 内置工具 | `src/tools/*` | `internal/tool/builtin/*` |
| 权限模式与规则 | `src/types/permissions.ts`、`src/hooks/useCanUseTool.tsx` | `internal/permission` |
| 模型与 Provider | `src/services/api/*` | `internal/llm` |
| SubAgent / Teams | `src/tools/AgentTool/*`、`src/tasks/*`、`src/coordinator/*` | `internal/subagent`、`internal/task` |
| 上下文与压缩 | `src/context.ts`、`src/services/compact/*` | `internal/context` |
| 记忆 | `src/memdir/*` | `internal/memory` |
| Skills | `src/skills/*` | `internal/skill` |
| MCP | `src/services/mcp/*` | `internal/mcp` |
| Hooks | `src/types/hooks.ts`、`src/utils/hooks*` | `internal/hook` |
| 会话持久化 | `src/utils/sessionStorage.ts` | `internal/session` |

### 工具集（按参考实现）

- 文件：`Read`、`Write`、`Edit`、`Glob`、`Grep`、`NotebookEdit`
- 执行：`Bash`、`PowerShell`
- 网络：`WebFetch`、`WebSearch`
- 协作：`Agent`、`SendMessage`、`TeamCreate`、`TeamDelete`、`Task*`、`TodoWrite`、`AskUserQuestion`
- 流程：`EnterPlanMode`、`ExitPlanMode`、`EnterWorktree`、`ExitWorktree`
- 扩展：`Skill`、`MCP`、`ListMcpResources`、`ReadMcpResource`、`ToolSearch`
- 代码智能：`LSP`
- 每个工具都要定义：输入 schema、给模型看的描述、`call`、`checkPermissions`、`isReadOnly`、`isConcurrencySafe`、结果到 API block 的映射。

### 权限与会话

- 权限模式：`default`、`acceptEdits`、`plan`、`bypassPermissions`、`dontAsk`，另有内部 `auto`。桌面端展示为五档，后端按模式 + 规则（工具名、参数模式、工作目录）判定。
- 规则来源：用户设置、项目设置、会话临时授权、always allow / always deny。
- 会话：每个会话一条追加式 transcript，记录用户消息、assistant 消息、tool_use/tool_result、压缩边界、usage；恢复时按边界重放。
- 上下文注入：`AGENTS.md`（用户级 + 项目根到工作目录逐级加载，越近优先级越高）、git 状态与最近提交、可用 Skills 清单、可用 SubAgent 清单、MCP 指令。

### 上下文、记忆与技能

- **压缩**：参考实现有 `autoCompact`、`microCompact`、`sessionMemoryCompact` 三种路径；先用轻量手段删减旧工具结果，再让独立 Agent 生成摘要并写入压缩边界。
- **记忆**：`MEMORY.md` 是索引（最多 200 行 / 25KB），每条记忆一个文件并带 frontmatter；每轮开始用一次侧查询挑出最多 5 条相关记忆注入上下文。
- **Skills**：从 policy/managed、user、project、plugin、bundled、MCP 六类来源加载 `SKILL.md`；frontmatter 提供名称和描述，正文按需加载（渐进披露），通过 `Skill` 工具调用。
- **Hooks**：支持 `PreToolUse`、`PostToolUse`、`PostToolUseFailure`、`UserPromptSubmit`、`SessionStart`、`SubagentStart`、`PermissionRequest`、`PermissionDenied`、`Notification`、`Elicitation`、`FileChanged`、`CwdChanged`、`WorktreeCreate` 等事件。

### 参考实现边界

| 目录 | 作用 |
|---|---|
| `../cc/src` | Agent 引擎原始实现：循环、工具、权限、压缩、记忆、Skills、MCP、Hooks |

### 建议实现顺序

1. Agent 循环 + 流式事件 + Tool 契约 + 文件/命令工具 + 权限审批
2. 会话持久化/恢复 + 上下文压缩 + 系统提示注入
3. MCP + Skills + 记忆 + Hooks
4. SubAgent / Teams / 后台任务
5. Diff、预览、终端、搜索等桌面 UI 能力
6. 本地 Server、H5 远程访问、IM 适配


## 技术栈

| 层 | 技术 |
|---|---|
| 桌面壳 | Wails v2.17（Go 1.26） |
| 后端 | Go，业务代码放 `internal/` |
| 前端 | React 19 + TypeScript 5.9 + Vite 8 |
| 状态 | Zustand 5 |
| 样式 | Tailwind CSS 4 |
| 依赖 | Go modules；本地执行 `go mod vendor` 生成 `vendor/`（已 gitignore，仅用于阅读 Wails 源码） |

## 进程模型

```text
Wails 应用（单个 Go 进程）
├── Go 后端：会话、模型调用、工具执行、MCP、存储、IM 适配
└── 系统 WebView（macOS WKWebView / Windows WebView2 / Linux WebKitGTK）
    └── React 渲染进程，通过 wailsjs 绑定调用 Go
```

运行时特性：没有 Node runtime、没有 Chromium 副本、没有 sidecar 进程；前端不能使用 Node API。

## 目录结构

```text
main.go                  Wails 入口：窗口配置、资源嵌入、Bind
internal/app/            Wails 绑定对象（前端可调用方法的唯一入口）
internal/agent/          Agent 循环、流式事件、会话编排（规划）
internal/tool/           Tool 契约、注册表与内置工具（规划）
internal/permission/     权限模式、规则与审批（规划）
internal/llm/            模型 Provider 与流式客户端（规划）
internal/context/        系统提示、token 预算、压缩（规划）
internal/memory/         文件型长期记忆：MEMORY.md 索引、双层作用域、召回打分、Memory 工具、轮末抽取（docs/memory.md）
internal/skill/          SKILL.md 加载与执行（规划）
internal/mcp/            MCP 客户端与传输（规划）
internal/subagent/       SubAgent / Teams（规划）
internal/task/           前台/后台任务与取消（规划）
internal/hook/           Hook 事件执行（规划）
internal/session/        transcript 与恢复（规划）
internal/project/        已打开项目列表（projects.json）
internal/workspace/      工作区文件浏览、写操作与 git 变更视图
internal/server/         本地 HTTP / WebSocket / H5（规划）
internal/im/             IM 适配（规划）
frontend/src/            React 源码（components / stores / lib 按需新增）
frontend/wailsjs/        自动生成的 TS 绑定，禁止手改
frontend/dist/           前端构建产物，由 go:embed 打进二进制
vendor/                  Go 依赖源码，含 Wails，便于直接阅读（已 gitignore）
wails.json               Wails 项目配置
```

后续新增后端能力时：Agent 相关实现放上表中的 `internal/` 领域包，在 `internal/app` 增加薄封装方法并绑定；前端通过生成的 `wailsjs/go/app/App` 调用。

## 开发命令

```bash
wails dev                      # 开发模式，热重载并自动生成绑定
wails build                    # 打包当前平台应用
go test ./...                  # Go 测试
go vet ./...                   # Go 静态检查
cd frontend && npm run typecheck && npm run build
go mod tidy && go mod vendor   # 依赖变更后同步 vendor
```

Wails CLI 安装：`go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.0`（默认在 `~/go/bin/wails`）。

`main.go` 用 `go:embed` 嵌入 `frontend/dist`，所以首次执行 `go test ./...` 或 `go build` 前，需要先构建一次前端（`cd frontend && npm install && npm run build`）；直接执行 `wails build` 会自动完成这一步。

## 前端验证（不要操作真实屏幕）

UI 改动一律通过浏览器 harness 验证，不使用系统级鼠标/键盘事件或截图工具，避免干扰用户正在进行的工作。

```bash
cd frontend && npm run harness   # 5173，strictPort；已占用则直接复用
# 打开 http://localhost:5173/harness.html
```

`harness.html` + `src/dev/harness.tsx` 挂载真实的 `<App/>`，只把 `window.go.app.App.*` 与 `window.runtime.*` 换成内存假实现；断言通过 `window.__bdeskReady`、`window.__bdesk.metrics()`、`__bdesk.send()`、`__bdesk.setTheme()` 等钩子完成。浏览器交互与截图使用 browseros。详见 `frontend/HARNESS.md`。`harness.html` 不进构建产物。

## 前端视觉规范

界面语言是「暖调工作台」：纸面底色、圆角分层、发丝描边、等宽元信息、单一强调色。规则写在 `frontend/src/style.css` 顶部，新增界面必须沿用：

1. **圆角按层级递进。** 容器 16px（`rounded-xl`）、卡片 12px（`rounded-lg`）、控件 8px（`rounded-md`）、标签 4px（`rounded-xs`）；只有按钮与状态药丸用 `rounded-full`。圆角尺度定义在 `style.css` 的 `@theme`，不要写任意 px 圆角。
2. **阴影只给浮层。** 工作区整体浮层用 `shadow-pane`、弹窗/菜单用 `shadow-pop`、内部小卡片用 `shadow-card`；聊天区与右侧预览、文件树始终属于同一个浮层，只用发丝分隔线分栏，内容区（消息、Markdown、表格、行内元素）不加阴影。
3. **只用语义色。** 颜色一律走 `rail / surface / panel / raised / hover / code / line / line-strong / fg / muted / subtle / accent / danger / warn / success` 这组语义类名，不写死具体色值。
4. **元信息用等宽。** 路径、计数、时间、状态标签、工具名统一 `font-mono` + `text-[10px]`，正文才用无衬线。
5. **强调色只用于当前项、主按钮、链接、焦点环。** 当前项是「抬起的小卡片」（`bg-surface` + `shadow-card` + 细描边），不要再用左侧竖线；主按钮用 `rounded-full` + 强调色填充。
6. **正文区留白。** 对话区与欢迎页使用纯底色留白，不加网格底纹；层级仍由圆角、描边和底色差表达。

## 前后端边界（Wails 约定）

1. 前端只通过 `frontend/wailsjs/go/**` 调用 Go；`wailsjs/` 由 Wails 生成，不手改。
2. 绑定方法只做参数校验、DTO 转换和调用编排；业务逻辑放 `internal/` 下的领域包。
3. 绑定方法的参数和返回值必须可 JSON 序列化；不要返回 `context.Context`、channel、函数或带锁的结构体。
4. 后端主动推送用 `runtime.EventsEmit`，前端用 `runtime.EventsOn` 订阅；不要轮询。
5. 前端不要引入依赖 Node/Electron API 的库；浏览器能力以 WebView 为准。
6. 绑定方法不要直接暴露内部类型；入参出参用 `internal/app` 中的 DTO。

---

## Go 最佳实践（本项目采用 7 条）

参考：[Google Go Style Best Practices](https://google.github.io/styleguide/go/best-practices)、[Go Code Review Comments](https://go.dev/wiki/CodeReviewComments)、[Effective Go](https://go.dev/doc/effective_go)、[Profiling Go Programs](https://go.dev/blog/pprof)。

1. **错误是值，必须显式处理。** 用 `fmt.Errorf("...: %w", err)` 包装并保留错误链，用 `errors.Is/As` 判断；不用 `_` 丢弃 error，不用 panic 处理普通错误。
   - 来源：Google Go Style Best Practices「Error handling」、Go Code Review Comments「Handle Errors」
2. **`context.Context` 作为内部方法第一个参数。** 请求级 ctx 沿调用链显式传递，支持取消与超时；不要把它塞进结构体。`internal/app.App` 只保留 Wails 生命周期 ctx，供后台任务统一取消。
   - 来源：Go Code Review Comments「Contexts」
3. **goroutine 的生命周期必须明确。** 每个后台任务都要能通过 ctx 退出；用 `errgroup` 收敛一组并发任务；优先用 channel 传递数据而不是共享内存。
   - 来源：Go Code Review Comments「Goroutine Lifetimes」、Effective Go「Concurrency」
4. **接口由使用方定义，按需最小化。** 先用具体类型实现行为，出现第二个实现或确实需要解耦时再抽接口；不要为“service / repository”这类概念预建空接口。
   - 来源：Google Go Style Best Practices「Interfaces」
5. **导出标识符必须有文档注释。** 注释以标识符名开头、写完整句子；错误字符串小写且不带结尾标点；提交前必须通过 `gofmt`。
   - 来源：Go Code Review Comments「Doc Comments」「Error Strings」、Effective Go「Commentary」
6. **避免包级可变全局状态。** 依赖通过构造函数或 `App` 字段显式注入，便于测试、并发控制和生命周期管理。
   - 来源：Google Go Style Best Practices「Global state」
7. **性能优化先测量。** 用 `go test -bench` 和 `pprof` 定位热点后再改；已知容量时给 slice/map 预分配，循环内拼接字符串用 `strings.Builder`。
   - 来源：Google Go Style Best Practices「Size hints」「String concatenation」、Go 官方博客《Profiling Go Programs》

## React 最佳实践（本项目采用 7 条）

参考：[Rules of React](https://react.dev/reference/rules)、[You Might Not Need an Effect](https://react.dev/learn/you-might-not-need-an-effect)、[Thinking in React](https://react.dev/learn/thinking-in-react)、[Rendering Lists](https://react.dev/learn/rendering-lists)、[useMemo](https://react.dev/reference/react/useMemo)、[useDeferredValue](https://react.dev/reference/react/useDeferredValue)、[React Compiler](https://react.dev/learn/react-compiler)。

1. **组件和 Hook 必须纯。** 渲染期间不做副作用，不修改 props、state 或传给 Hook/JSX 的值；副作用放到事件处理或 Effect 中。
   - 来源：React 官方文档「Rules of React」
2. **Hook 只在顶层调用，且只在 React 函数中调用。** 不放进条件、循环、嵌套函数或 `try/catch`；用 `<StrictMode>` 和 `eslint-plugin-react-hooks` 兜底。
   - 来源：React 官方文档「Rules of Hooks」
3. **不要用 Effect 处理派生数据或用户事件。** 能从 props/state 算出来的值直接在渲染时计算；用户点击后的请求写在事件处理函数里。Effect 只用于同步外部系统。
   - 来源：React 官方文档「You Might Not Need an Effect」
4. **状态保持最小且就近。** 只存无法计算的数据；临时 UI 状态留在组件内，只有跨页面共享的状态才放 Zustand。
   - 来源：React 官方文档「Thinking in React」Step 3、「useMemo」深挖
5. **列表 key 必须稳定唯一。** 使用数据自带的 ID；不要用数组下标或随机数，否则重排、插入、删除时组件状态会错位并触发全量重建。
   - 来源：React 官方文档「Rendering Lists」
6. **memo 优化必须有测量依据。** `useMemo` 只用于“计算确实慢且依赖很少变化”或 `memo` 子组件 props 的场景；优先启用 React Compiler 自动 memo，避免手写一堆 `useMemo/useCallback`。
   - 来源：React 官方文档「useMemo」「React Compiler」
7. **长列表和高频输入用并发特性保交互。** 搜索过滤等非紧急更新用 `useDeferredValue` / `useTransition` 降低输入阻塞；消息流等超长列表再叠加虚拟滚动（项目约定）。
   - 来源：React 官方文档「useDeferredValue」
