# b-desktop 架构图

> 依据当前仓库实际代码（`main.go`、`internal/app/app.go`、`internal/agent/loop.go`、`internal/agent/wiring.go`、`internal/tool/tool.go`、`frontend/src/lib/reduceAgentEvents.ts`）。

## 1. 整体分层

```mermaid
flowchart TB
  subgraph ext["外部资源"]
    API["模型 API<br/>Anthropic / OpenAI / 第三方端点"]
    MCPS["MCP Server"]
    FS["工作目录文件系统"]
    GIT["Git 仓库"]
  end

  subgraph wv["系统 WebView · macOS WKWebView / Windows WebView2"]
    direction TB
    subgraph fe["frontend/src · React 19 + Zustand 5"]
      direction LR
      CHAT["聊天区<br/>MessageList · ToolCallCard<br/>streamingMarkdown"]
      WSP["工作区面板<br/>FileTree · CodeView<br/>DiffView · ReviewDiff"]
      DLG["弹窗与设置<br/>PermissionDialog · McpSettings<br/>SkillSettings · HookSettings"]
      STORE["Zustand stores<br/>app · workbench · preview · settings"]
      RED["lib/reduceAgentEvents.ts<br/>22 种事件 → UI 状态"]
    end
    BINDTS["frontend/wailsjs/go/app/App.ts<br/>Wails 自动生成，禁止手改"]
  end

  subgraph shell["Wails 壳层 · main.go"]
    EMBED["go:embed all:frontend/dist"]
    WRUN["wails.Run · Bind app.App"]
  end

  subgraph glue["绑定层 · internal/app · 60 个导出方法"]
    direction LR
    APPR["App 方法<br/>StartTurn · CancelTurn<br/>SubmitApproval · ListSessions"]
    DTO["参数校验 + DTO 转换<br/>只做编排，不写业务"]
    EMIT["runtime.EventsEmit<br/>事件名 agent:event"]
  end

  subgraph core["运行时 · internal/agent"]
    direction LR
    LOOP["turn.run()<br/>单层状态机<br/>唯一推进信号 = tool_use"]
    REG["Registry<br/>工具注册表 + 分组"]
    EVT["EventSink<br/>单 goroutine 顺序发射"]
  end

  subgraph domain["领域包 · internal/"]
    direction LR
    LLM["llm · Anthropic/OpenAI<br/>SSE 流式 · 重试 · thinking"]
    PERM["permission · 五档模式<br/>allow / ask / deny"]
    TOOL["tool + builtin<br/>Read Write Edit Glob Grep<br/>Bash TodoWrite AskUserQuestion"]
    CTX["context · 系统提示<br/>token 预算 · 微压缩/自动压缩"]
    SESS["session · JSONL transcript<br/>按压缩边界重放恢复"]
    EXTP["mcp · skill · memory<br/>hook · subagent · task"]
    WSPK["workspace · 文件树<br/>预览 · git 变更审阅"]
    TEL["telemetry · 用量统计"]
  end

  EMBED --> WV
  WRUN --> BINDTS
  BINDTS <--> APPR
  APPR --> DTO
  DTO --> LOOP
  LOOP --> LLM
  LOOP --> CTX
  LOOP --> SESS
  LOOP --> REG
  REG --> TOOL
  LOOP --> PERM
  LOOP --> EXTP
  PERM --> APPR
  EXTP --> APPR
  APPR --> WSPK
  APPR --> TEL
  EVT --> EMIT
  EMIT --> RED
  RED --> STORE
  STORE --> CHAT
  STORE --> WSP
  STORE --> DLG
  LLM --> API
  EXTP --> MCPS
  TOOL --> FS
  WSPK --> FS
  WSPK --> GIT
```

## 2. 一次轮次（turn）的内部循环

```mermaid
flowchart TB
  U["用户输入<br/>文本 + 图片"] --> APPEND["写入 transcript<br/>追加消息历史"]
  APPEND --> HOOK1["UserPromptSubmit 钩子<br/>返回文本追加到系统提示"]
  HOOK1 --> LOOP{"for 循环"}

  LOOP --> BUDGET["检查 token 预算<br/>Compactor.Budget"]
  BUDGET --> COMPACT{"超阈值?"}
  COMPACT -->|是| MC["microCompact → autoCompact<br/>写压缩边界"]
  COMPACT -->|否| CALL
  MC --> CALL["callModel 流式请求<br/>text / thinking / tool_use 增量"]

  CALL --> STREAM{"Provider 流"}
  STREAM -->|文本增量| E1["emit text_delta"]
  STREAM -->|思考增量| E2["emit thinking_delta"]
  STREAM -->|工具参数| E3["emit tool_input_delta"]
  E1 --> END
  E2 --> END
  E3 --> END

  CALL --> HASUSE{"本轮有 tool_use?"}
  HASUSE -->|否| FINISH["本轮结束 · emit turn_end"]
  HASUSE -->|是| GROUP["分组：只读并行 / 写工具串行<br/>并发上限 10"]

  GROUP --> PERM["权限判定<br/>allow / ask / deny"]
  PERM -->|ask| WAIT["Approvals.Prepare<br/>阻塞等前端审批"]
  WAIT --> PERM
  PERM -->|deny| ERR["生成错误 tool_result"]
  PERM -->|allow| HOOK2["PreToolUse 钩子"]
  HOOK2 --> RUN["tool.Run 执行"]
  RUN --> HOOK3["PostToolUse 钩子"]
  HOOK3 --> RES["回灌 tool_result<br/>每个 tool_use 恰好一个"]
  ERR --> RES
  RES --> LOOP
```

要点：

- **唯一推进信号是 `tool_use`**。模型不再要工具，本轮就结束；`stop_reason` 不参与控制流。
- **错误不打断循环**。工具失败、参数非法、模型请求失败都转成结构化结果回灌，由模型决定下一步；模型请求连续失败上限 3 次（`maxModelFailureRetries = 2`）。
- **事件只有 turn goroutine 一个发射者**，粒度就是 provider delta，后端不聚合文本，前端自行合帧。
