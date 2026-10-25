# B Desktop

**桌面 AI 工作台：一个 Go 二进制、系统 WebView，以及一套完整的编码 Agent 运行时。**

[English](./README.md) · [简体中文](./README.zh-CN.md)

B Desktop 把本地编码 Agent 做成原生桌面应用。Agent 循环、工具执行、权限校验、上下文压缩、
会话持久化全部在 Go 侧完成，React 只负责渲染界面。运行时不带 Node、不打包 Chromium、没有
sidecar 进程——直接跑在系统 WebView 上，低性能机器上也能保持流畅。

---

## 目录

- [核心能力](#核心能力)
- [架构](#架构)
- [技术栈](#技术栈)
- [快速开始](#快速开始)
- [配置](#配置)
- [权限模式](#权限模式)
- [工具清单](#工具清单)
- [扩展体系](#扩展体系)
- [右侧工作区面板](#右侧工作区面板)
- [流式事件](#流式事件)
- [数据存放位置](#数据存放位置)
- [目录结构](#目录结构)
- [开发](#开发)
- [项目进展](#项目进展)
- [致谢](#致谢)

---

## 核心能力

### Agent 运行时

- **工具驱动循环。** 只有模型不再发出 `tool_use` 才算一轮结束；每个工具调用都会执行，
  结果回灌进消息历史。
- **默认逐条流式。** 正文、思考过程、工具入参、工具进度、工具结果、用量、压缩边界都是
  一条条推到前端，绝不在轮次结束时一次性返回。
- **只读工具并行、写工具串行**（默认并发上限 10）。每个 `tool_use` 必定有且只有一个对应的
  `tool_result`，被拒绝、被取消或工具崩溃时同样如此。
- **错误不打断循环。** 工具报错、参数校验失败、模型请求失败都会转成结构化结果交回模型。
- **上下文自动压缩。** 每次模型调用前检查 token 预算，先做轻量微压缩，再做自动压缩；压缩
  边界写进 transcript，重开会话时可以从边界继续。
- **SubAgent 独立上下文。** 子 Agent 有自己的消息历史、工具池和权限范围，结果汇总回父
  Agent；后台运行的任务可以取消与恢复。
- **会话可断点恢复。** transcript 是追加式 JSONL。重开会话时从最后一个压缩边界重放，并
  重建 tool_use / tool_result 的配对关系。

### 扩展一览

| 扩展 | 作用 | 存放位置 |
|---|---|---|
| **Skills** | 渐进披露的 `SKILL.md` 操作手册，只在模型真正需要时才读取正文 | `~/.b-desktop/skills/`、`<项目>/.b-desktop/skills/` |
| **Memory** | `MEMORY.md` 作索引，每条记忆一个文件；每轮召回相关记忆注入上下文 | `~/.b-desktop/memory/` |
| **Hooks** | 在 `SessionStart`、`UserPromptSubmit`、`PreToolUse`、`PostToolUse` 时执行你的命令 | `~/.b-desktop/hooks.json`、`<项目>/.b-desktop/hooks.json` |
| **MCP** | 通过 stdio 挂载 MCP 服务器提供的工具 | `~/.b-desktop/mcp.json` |
| **SubAgent** | 用 Markdown frontmatter（`name` / `description`）定义自定义 Agent 类型 | `~/.b-desktop/agents/*.md` |
| **后台任务** | 后台 shell 命令与后台子 Agent 持续执行，可轮询、查看尾部输出、随时停止 | 进程内任务注册表 |

### 工作台

- 多会话侧边栏，按项目分组，支持全局搜索与会话历史。
- 五档权限模式，所有需要审批的工具调用都有图形化审批对话框。
- 分支 / worktree 支持：每个分支在仓库同级的 `worktrees/<项目名>/` 下有独立的 `git worktree`，
  可以直接在里面开启会话。
- Markdown 渲染，支持 GFM、语法高亮与 Mermaid 图。
- 右侧工作区面板：文件树、文件预览、就地编辑，以及带暂存与放弃的 git 变更审阅。
- 按模型调用维度的请求追踪与用量统计。
- 亮色 / 暗色 / 跟随系统主题，欢迎页有分层动画吉祥物。

---

## 架构

```text
┌──────────────────────── Wails 应用（单个 Go 进程） ──────────────────────────────────┐
│                                                                                      │
│  Go 后端                                                                        │
│  ┌────────────┐   ┌───────────────────────────────────────────────────────────────┐   │
│  │ internal/  │   │  agent engine                                                │   │
│  │  app       │◄──┤  ├─ prepare(): 系统提示 + 上下文提供者                       │   │
│  │ (绑定层)   │   │  ├─ check()  : token 预算 → 微压缩/自动压缩                   │   │
│  │ 只做 DTO   │   │  ├─ callModel(): 流式请求 (anthropic | openai)               │   │
│  └─────┬──────┘   │  ├─ runTools(): 只读并行, 写工具串行                          │   │
│        │          │  └─ tool_result → 消息历史 → 下一轮                            │   │
│        │          └───────────────┬───────────────────────────────────────────────┘   │
│        │                          │                                                   │
│        │   ┌──────────────────────┴────────────────────────────┐                   │
│        │   │  internal/llm · tool · permission · context       │                   │
│        │   │  internal/session · memory · skill · mcp          │                   │
│        │   │  internal/subagent · task · hook · telemetry       │                   │
│        │   └───────────────────────────────────────────────────┘                   │
│        │                                                                          │
│  runtime.EventsEmit  ─────────────►  React 19 UI  ◄──────  wailsjs/go/app/App     │
└──────────────────────────────────────────────────────────────────────────────────────┘
```

### 一次请求的生命周期

```text
用户输入
  └─ SendMessage → engine.Run
       ├─ SessionStart 钩子
       ├─ 用户消息写入 transcript
       ├─ UserPromptSubmit 钩子
       └─ 循环步骤
            ├─ token 预算检查 → microCompact / autoCompact
            ├─ callModel  →  text_delta / thinking_delta / tool_input_delta
            ├─ 收集 tool_use
            ├─ 权限判定 (allow / ask / deny)
            │    └─ ask  →  permission_request → 前端对话框 → permission_resolved
            ├─ runTools  →  tool_progress → tool_result
            ├─ tool_result 回灌历史
            └─ 只要模型还在发 tool_use 就继续下一轮
```

---

## 技术栈

| 层 | 技术 |
|---|---|
| 桌面壳 | [Wails v2.17](https://wails.io)（Go 1.26） |
| 后端 | Go —— `internal/agent`、`internal/llm`、`internal/tool`、`internal/permission`、`internal/session` 等 |
| 前端 | React 19 · TypeScript 5.9 · Vite 8 · Tailwind CSS 4 · Zustand 5 |
| 代码高亮 | highlight.js（Markdown 渲染与代码查看器共用一套配色） |
| 图表 | Mermaid |
| 运行时 | 系统 WebView（WKWebView / WebView2 / WebKitGTK），不打包浏览器 |

---

## 快速开始

### 前置依赖

- **Go 1.26+**（`go version`）
- **Node.js 20.19+ 或 22.12+**（`node -v`）—— Vite 8 的要求
- **Wails CLI v2.17** —— `go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.0`
  （装在 `~/go/bin/wails`，记得加进 `PATH`）

### 运行

```bash
git clone https://github.com/Baike12/b-desktop.git
cd b-desktop

# 安装前端依赖（只需一次）。
cd frontend && npm install && cd ..

# 启动桌面应用开发模式，带热重载。
wails dev
```

`wails dev` 会构建前端、重新生成 `wailsjs` 绑定并拉起应用。绑定是生成代码，
**不要手改 `frontend/wailsjs/`**。

### 打包

```bash
wails build              # 打当前平台，产物在 build/bin
```

构建会把 `frontend/dist` 嵌进 Go 二进制，最终产物是单文件可执行程序。

### 首次使用

1. 打开**设置**，填写模型服务：
   - **Provider** —— `anthropic` 或 `openai`
   - **Base URL** —— API 根地址（例如 `https://api.anthropic.com`）
   - **Model** —— 模型 id
   - **API Key** —— 存在 `~/.b-desktop/config.json`，权限 `0600`
2. 从侧边栏打开一个项目，新建会话，然后发消息。

---

## 配置

应用启动时读取 `~/.b-desktop/config.json`。每个字段都能用环境变量覆盖，环境变量优先级更高。

| 配置字段 | 环境变量 | 默认值 | 含义 |
|---|---|---|---|
| `provider` | `ANC_PROVIDER` | `anthropic` | 协议：`anthropic` 或 `openai` |
| `apiKey` | `ANC_API_KEY` | — | API Key |
| `baseUrl` | `ANC_BASE_URL` | — | API 根地址 |
| `model` | `ANC_MODEL` | — | 模型 id |
| `permissionMode` | `ANC_PERMISSION_MODE` | `default` | 初始权限模式 |
| `dataDir` | `ANC_DATA_DIR` | 平台配置目录 | 会话 transcript 存放位置 |
| `workDir` | `ANC_WORKDIR` | 进程 CWD | 默认工作目录 |
| `contextWindow` | `ANC_CONTEXT_WINDOW` | `200000` | 上下文窗口（token） |
| `maxOutputTokens` | `ANC_MAX_OUTPUT_TOKENS` | `8192` | 单次请求最大输出（token） |
| `thinkingEnabled` | `ANC_THINKING_ENABLED` | `false` | 是否请求扩展思考 |
| `thinkingBudget` | `ANC_THINKING_BUDGET` | 供应商默认 | 思考预算（token） |
| `maxTurns` | `ANC_MAX_TURNS` | `100` | 单轮最大模型调用次数 |
| `maxToolConcurrency` | `ANC_MAX_TOOL_CONCURRENCY` | `10` | 工具并发上限 |

`ANC_CONFIG` 可以指定另一份配置文件，方便并排放多套配置：

```bash
ANC_CONFIG=~/.b-desktop/work.json wails dev
```

设置页写回的是同一份文件，所以用界面配置或用 dotfile 配置都可行。

---

## 权限模式

每次工具调用都在**执行之前**做校验，结论只有 `allow` / `ask` / `deny` 三种。`ask` 会弹出
界面对话框，本轮阻塞直到你做出选择。

| 模式 | 只读工具 | Write / Edit | Bash | 其他写工具 |
|---|---|---|---|---|
| `default` | allow | ask | ask | ask |
| `acceptEdits` | allow | 工作目录内 allow，否则 ask | ask | ask |
| `plan` | allow | deny | deny | deny |
| `bypassPermissions` | allow | allow | allow | allow |
| `dontAsk` | allow | deny | deny | deny |

- `plan` 禁止一切副作用，适合只读探索。
- `dontAsk` 判定逻辑同 `default`，但把所有 `ask` 收口成 `deny`，会话永远不会卡在对话框上。
- `bypassPermissions` 仍然受显式 deny 规则和内置硬拒绝约束。

规则还可以按工具挂载，用 `exact`、`prefix`、`glob`、`json` 四种匹配方式；deny 永远优先于
allow。完整判定顺序见 `docs/agent-architecture.md`。

---

## 工具清单

### 内置工具

| 工具 | 只读 | 并发安全 | 说明 |
|---|---|---|---|
| `Read` | 是 | 是 | 支持 offset/limit 区间读取与行号；超过 256 KB 的文件直接拒绝，输出按 token 预算截断 |
| `Write` | 否 | 否 | 临时文件 + rename 原子写入，自动创建父目录 |
| `Edit` | 否 | 否 | 未设 `replace_all` 时 `old_string` 必须唯一命中 |
| `Glob` | 是 | 是 | 支持 `**`，跳过 `.git` 与 `node_modules`，按 mtime 倒序 |
| `Grep` | 是 | 是 | RE2 正则、glob 过滤、跳过二进制、支持上下文行 |
| `Bash` | 否 | 否 | POSIX 走 `/bin/bash -lc`，Windows 走 `%COMSPEC% /d /s /c`；取消时按进程组整体杀掉 |
| `TodoWrite` | 否 | 否 | 内存态任务清单，始终放行 |
| `AskUserQuestion` | 否 | 否 | 结构化选择题，在界面里渲染 |

所有文件工具都以会话工作目录为基准解析路径，越界（含经由软链接）一律拒绝。

### 扩展提供的工具

| 工具 | 来源 |
|---|---|
| `Skill` | Skills —— 按需读取 `SKILL.md` 正文 |
| `MemorySave` | Memory —— 追加一条记忆并更新索引 |
| `Agent` | SubAgent —— 以独立上下文运行子 Agent |
| `SendMessage` | SubAgent —— 向运行中的子 Agent 投递消息 |
| `TaskList` / `TaskOutput` / `TaskStop` | 后台任务 —— 轮询、查看尾部输出、停止长任务 |
| （远端工具） | MCP —— 已配置 MCP 服务器提供的全部工具 |

---

## 扩展体系

### Skills

一个技能是一个包含 `SKILL.md` 的目录，frontmatter 写 `name` 和 `description`。注入上下文的
只有 description；正文在模型调用 `Skill` 工具时才读取。

```text
~/.b-desktop/skills/review-checklist/
└── SKILL.md
```

```markdown
---
name: review-checklist
description: Run this before declaring a task complete.
---

1. `go test ./...`
2. `go vet ./...`
3. Confirm no secrets are printed.
```

### Memory

`MEMORY.md` 是索引，每条记忆另存一个文件。每轮开始时按关键词重合度召回最多 5 条相关记忆
注入上下文。

### Hooks

```json
{
  "hooks": {
    "PreToolUse": [{ "matcher": "Bash", "command": "./scripts/check.sh" }],
    "PostToolUse": [{ "matcher": "Write", "command": "./scripts/format.sh" }]
  }
}
```

`PreToolUse` 钩子可以把 `allow` 升级成 `ask` 或 `deny`，但不能放宽已有判定。钩子自身失败
只会上报，不会中断本轮。

### MCP

```json
{
  "mcpServers": {
    "filesystem": { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"] }
  }
}
```

MCP 服务器首次使用时才惰性启动，应用退出时统一关闭。目前支持 stdio 传输。

### SubAgent

```markdown
---
name: test-runner
description: Writes and runs tests for the current package.
---

You are a test specialist. Focus on the smallest failing test…
```

子 Agent 定义从 `~/.b-desktop/agents/*.md` 发现，拥有独立的历史、工具集与权限范围。

---

## 右侧工作区面板

窗口右侧是一等公民的工作区：

- **文件树** —— 懒加载、右键菜单、新建 / 重命名 / 移动 / 删除。
- **文件预览** —— 多标签、可拖动调宽、语法高亮、可就地编辑。
- **变更审阅** —— 基于 git CLI 的差异视图，支持暂存、取消暂存与放弃；未跟踪文件会合成一份
  「全部新增」的 diff。
- **搜索** —— 在工作目录内按文件名与内容搜索。

所有文件写操作都被限制在会话工作目录内：词法越界与指向外部的软链接一律拒绝；写入是原子的
并保留原文件权限位。细节见 `docs/workspace-panel.md`。

---

## 流式事件

后端在每个关键状态切换时推一条类型化事件，前端不需要等整轮结束就能渲染。

| 事件 | 含义 |
|---|---|
| `turn_queued` / `turn_start` / `turn_end` | 轮次生命周期，含排队轮次 |
| `step_start` | 即将发起一次模型调用 |
| `message_start` / `message_stop` | 助手消息边界 |
| `block_start` | 正文 / 思考 / 工具块开始 |
| `text_delta` | 正文增量 |
| `thinking_delta` | 思考过程增量 |
| `tool_call_start` / `tool_input_delta` / `tool_call_ready` | 工具调用开始、参数流式、参数完整 |
| `tool_progress` | 工具运行中的中间进度 |
| `permission_request` / `permission_resolved` | 审批弹窗弹出与应答 |
| `tool_result` | 某个工具结束（逐个发，不批量聚集） |
| `usage` | 该条消息的 token 用量 |
| `compact_start` / `compact_end` | 上下文压缩边界 |
| `retry` | Provider 正在重试请求 |
| `error` / `message_discard` | 失败，包括作废已流式输出的半截消息 |

---

## 数据存放位置

```text
~/.b-desktop/                        # 用户配置目录（0700）
├── config.json                      # 模型服务、权限模式、token 预算（0600）
├── hooks.json                       # 用户级 hooks
├── mcp.json                         # MCP 服务器配置
├── agents/                          # 子 Agent 定义（*.md）
├── skills/                          # 用户级技能
└── memory/
    ├── MEMORY.md                    # 记忆索引
    └── <memory>.md                  # 每条记忆一个文件

<平台配置目录>/b-desktop/            # 会话 transcript（JSONL，每个会话一个文件）
└── <sessionID>.jsonl

<项目>/.b-desktop/                   # 项目级覆盖
├── hooks.json
└── skills/
```

---

## 目录结构

```text
main.go                  Wails 入口：窗口配置、资源嵌入、绑定
internal/app/            Wails 绑定层——前端唯一可调用的入口
internal/agent/          Agent 循环、流式事件、会话编排
internal/llm/            Anthropic / OpenAI 流式客户端、模型预设
internal/tool/           工具契约、注册表与内置工具
internal/permission/     权限模式、规则匹配与判定引擎
internal/context/        系统提示组装、token 预算、压缩
internal/session/        追加式 transcript、重放、会话标题
internal/memory/         长期记忆存储与召回
internal/skill/          SKILL.md 发现与加载
internal/mcp/            MCP 客户端、传输与工具桥接
internal/subagent/       子 Agent 类型、执行器与消息
internal/task/           后台任务注册表与控制
internal/hook/           钩子配置与执行
internal/telemetry/      请求追踪与用量统计
internal/project/        已打开项目列表与 worktree
internal/workspace/      文件浏览、写操作与 git 变更视图
frontend/src/            React 应用（components、stores、lib）
frontend/wailsjs/        自动生成的 TS 绑定——禁止手改
docs/                    架构说明与扩展契约
```

---

## 开发

```bash
wails dev                      # 带热重载的桌面应用

cd frontend
npm run typecheck              # tsc -b
npm run build                  # tsc -b && vite build
npm run harness                # 浏览器 harness：http://localhost:5173/harness.html
```

**harness** 在浏览器里挂载真实的 `<App/>`，只把两层 Wails 接口（`window.go.app.App.*` 与
`window.runtime.*`）换成内存假实现，不用启动桌面应用、不操作真实屏幕就能验证界面——断言钩子
见 `frontend/HARNESS.md`。

```bash
go test ./...                  # Go 测试（全部离线）
go vet ./...
gofmt -l .                     # 格式检查
go mod tidy && go mod vendor   # 依赖变更后同步（vendor/ 已 gitignore）
```

---

## 项目进展

**已实现：** Agent 运行时与工具循环、Anthropic 与 OpenAI 的流式传输、五档权限模式与图形化
审批、会话持久化与断点恢复、上下文自动压缩、Skills、Memory、Hooks、MCP（stdio）、SubAgent、
后台任务、请求追踪与用量统计、多会话工作台，以及带 git 变更审阅的右侧工作区面板。

**接下来的顺序：** 先做本地 HTTP / WebSocket 服务以支持 H5 远程访问，再做 IM 适配
（Telegram / 飞书 / 微信 / 钉钉 / WhatsApp）。

---

## 致谢

B Desktop 用 Go 复刻了若干优秀编码 Agent 的行为模型，影响其语义的参考实现记录在
`docs/research/` 下——尤其是 Claude Code 的 Agent 循环、Zed 的权限模型与 Codex 的扩展契约。

---

## 许可证

仓库当前没有附带许可证文件，公开发布前请先补上。
