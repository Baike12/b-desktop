# AGENTS.md

## 项目是什么

`anc-dsk` 是桌面 AI 工作台 `anc-desktop` 的 Go 重写版。原项目基于 Electron + React，在低性能电脑上内存占用高、启动和交互卡顿；本项目改用 **Wails v2 + 系统 WebView**，把原本分散在 Electron 主进程、Node sidecar、Bun server 中的能力收敛到一个 Go 二进制里，React 只负责界面。

对标项目位于同级目录 `../anc-desktop`，功能与交互以它为准，实现方式以本文件为准。

要复刻的核心能力：

- 多会话工作台、会话历史、全局搜索、分支 / Worktree 启动
- 工具调用与任务进度展示、Diff 审阅、内置浏览器预览
- 五档权限模式与图形化审批
- 模型自选（官方账号、第三方预设、本地端点）、图片生成
- MCP / SubAgent / Skills 可视化管理与技能市场
- 模型请求追踪、用量统计、定时任务
- Computer Use、桌面宠物、多主题
- H5 远程访问、IM 接入（Telegram / 飞书 / 微信 / 钉钉 / WhatsApp）

当前阶段只搭好开发框架：Wails 窗口、React 页面、前后端绑定链路和一个示例方法 `Greet`。业务功能尚未实现。

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

与 Electron 版的关键差异：没有 Node runtime、没有 Chromium 副本、没有 sidecar 进程；前端不能使用 Node API。

## 目录结构

```text
main.go                  Wails 入口：窗口配置、资源嵌入、Bind
internal/app/            Wails 绑定对象（前端可调用方法的唯一入口）
frontend/src/            React 源码（components / stores / lib 按需新增）
frontend/wailsjs/        自动生成的 TS 绑定，禁止手改
frontend/dist/           前端构建产物，由 go:embed 打进二进制
vendor/                  Go 依赖源码，含 Wails，便于直接阅读
wails.json               Wails 项目配置
```

后续新增后端能力时：业务实现放 `internal/<domain>/`，在 `internal/app` 增加薄封装方法并绑定；前端通过生成的 `wailsjs/go/app/App` 调用。

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
