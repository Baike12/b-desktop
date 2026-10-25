# B Desktop

**A desktop AI workbench: one Go binary, the system WebView, and a full coding-agent runtime.**

[English](./README.md) · [简体中文](./README.zh-CN.md)

B Desktop turns a local coding agent into a native desktop app. The agent loop, tool
execution, permission checks, context compaction and session persistence all live in Go;
React only renders the interface. There is no Node runtime, no bundled Chromium and no
sidecar process — the app runs on the OS WebView and stays responsive on modest hardware.

---

## Table of contents

- [Highlights](#highlights)
- [Architecture](#architecture)
- [Tech stack](#tech-stack)
- [Getting started](#getting-started)
- [Configuration](#configuration)
- [Permission modes](#permission-modes)
- [Tool reference](#tool-reference)
- [Extension system](#extension-system)
- [Workspace panel](#workspace-panel)
- [Streaming events](#streaming-events)
- [Data layout](#data-layout)
- [Repository layout](#repository-layout)
- [Development](#development)
- [Project status](#project-status)
- [Credits](#credits)

---

## Highlights

### Agent runtime

- **Tool-driven loop.** A turn ends only when the model stops emitting `tool_use`; every
  tool call is executed and its result is fed back into the message history.
- **Streaming by default.** Text, thinking, tool input, tool progress, tool results, usage
  and compaction boundaries are pushed to the UI one event at a time — never batched at
  the end of a turn.
- **Read-only tools run in parallel, mutating tools run serially** (default concurrency
  limit: 10). Every `tool_use` is guaranteed exactly one matching `tool_result`, even
  when it is rejected, cancelled or crashes.
- **Failures never break the loop.** Tool errors, schema validation failures and model
  request errors are converted into structured results and handed back to the model.
- **Automatic context compaction.** Before every model call the token budget is checked;
  lightweight micro-compaction runs first, then full auto-compaction. The boundary is
  recorded in the transcript so a session can be resumed later.
- **Independent sub-agents.** A sub-agent has its own message history, tool pool and
  permission scope; results are summarised back into the parent. Background runs can be
  cancelled and resumed.
- **Resumable sessions.** Transcripts are append-only JSONL. Reopening a session replays
  from the last compaction boundary and rebuilds tool-call/tool-result pairing.

### Extensions at a glance

| Extension | What it does | Where it lives |
|---|---|---|
| **Skills** | Progressive-disclosure `SKILL.md` playbooks; the model loads the body only when it actually needs it | `~/.b-desktop/skills/`, `<project>/.b-desktop/skills/` |
| **Memory** | `MEMORY.md` index plus one file per memory; relevant memories are recalled and injected each turn | `~/.b-desktop/memory/` |
| **Hooks** | Run your own commands on `SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse` | `~/.b-desktop/hooks.json`, `<project>/.b-desktop/hooks.json` |
| **MCP** | Mount tools from MCP servers over stdio | `~/.b-desktop/mcp.json` |
| **Sub-agents** | Custom agent types defined in Markdown with `name` / `description` frontmatter | `~/.b-desktop/agents/*.md` |
| **Background tasks** | Background shell commands and background sub-agents keep running; their output can be polled, tailed or stopped | in-process task registry |

### Workbench

- Multi-session sidebar with project grouping, global search and per-session history.
- Five permission modes with a graphical approval dialog for every gated tool call.
- Branch / worktree support: each branch gets its own `git worktree` under
  `worktrees/<project>/` next to the repository, and a session can be started inside it.
- Markdown rendering with GFM, syntax highlighting and Mermaid diagrams.
- A right-hand workspace panel: file tree, file preview, in-place editing and a git
  change reviewer with staging and discard.
- Request tracing and usage statistics per model call.
- Light / dark / system themes, a layered mascot on the welcome page.

---

## Architecture

```text
┌──────────────────────── Wails application (single Go process) ───────────────────────┐
│                                                                                      │
│  Go backend                                                                       │
│  ┌────────────┐   ┌───────────────────────────────────────────────────────────────┐   │
│  │ internal/  │   │  agent engine                                                │   │
│  │  app       │◄──┤  ├─ prepare(): system prompt + context providers              │   │
│  │ (bindings) │   │  ├─ check()  : token budget → micro/auto compaction          │   │
│  │ DTO only   │   │  ├─ callModel(): streaming request (anthropic | openai)      │   │
│  └─────┬──────┘   │  ├─ runTools(): read-only ∥, mutating → sequential           │   │
│        │          │  └─ tool_result → message history → next step                 │   │
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

### Request lifecycle

```text
user input
  └─ SendMessage → engine.Run
       ├─ SessionStart hook
       ├─ user message persisted to transcript
       ├─ UserPromptSubmit hook
       └─ loop step
            ├─ token budget check → microCompact / autoCompact
            ├─ callModel  →  text_delta / thinking_delta / tool_input_delta
            ├─ collect tool_use blocks
            ├─ permission check (allow / ask / deny)
            │    └─ ask  →  permission_request → UI dialog → permission_resolved
            ├─ runTools  →  tool_progress → tool_result
            ├─ append tool_result to history
            └─ repeat while the model keeps emitting tool_use
```

---

## Tech stack

| Layer | Technology |
|---|---|
| Desktop shell | [Wails v2.17](https://wails.io) (Go 1.26) |
| Backend | Go — `internal/agent`, `internal/llm`, `internal/tool`, `internal/permission`, `internal/session`, … |
| Frontend | React 19 · TypeScript 5.9 · Vite 8 · Tailwind CSS 4 · Zustand 5 |
| Highlighting | highlight.js (shared by the Markdown renderer and the code viewer) |
| Diagrams | Mermaid |
| Runtime | System WebView (WKWebView / WebView2 / WebKitGTK) — no bundled browser |

---

## Getting started

### Prerequisites

- **Go 1.26+** (`go version`)
- **Node.js 20.19+ or 22.12+** (`node -v`) — required by Vite 8
- **Wails CLI v2.17** — `go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.0`
  (installs to `~/go/bin/wails`; make sure it is on your `PATH`)

### Run it

```bash
git clone https://github.com/Baike12/b-desktop.git
cd b-desktop

# Install the frontend dependencies (once).
cd frontend && npm install && cd ..

# Start the desktop app in development mode with hot reload.
wails dev
```

`wails dev` builds the frontend, generates the `wailsjs` bindings and launches the app.
Bindings are generated code — never edit `frontend/wailsjs/` by hand.

### Build a release binary

```bash
wails build              # current platform, output in build/bin
```

The build embeds `frontend/dist` into the Go binary, so the result is a single
self-contained executable.

### First run

1. Open **Settings** and fill in the provider block:
   - **Provider** — `anthropic` or `openai`
   - **Base URL** — the API root (e.g. `https://api.anthropic.com`)
   - **Model** — the model id
   - **API key** — stored in `~/.b-desktop/config.json` with `0600` permissions
2. Open a project from the sidebar, create a session, and send a message.

---

## Configuration

The application reads `~/.b-desktop/config.json` on startup. Every field can be
overridden by an environment variable, which takes precedence over the file.

| Config field | Env var | Default | Meaning |
|---|---|---|---|
| `provider` | `BDESKTOP_PROVIDER` | `anthropic` | Protocol: `anthropic` or `openai` |
| `apiKey` | `BDESKTOP_API_KEY` | — | API key for the provider |
| `baseUrl` | `BDESKTOP_BASE_URL` | — | API root |
| `model` | `BDESKTOP_MODEL` | — | Model id |
| `permissionMode` | `BDESKTOP_PERMISSION_MODE` | `default` | Initial permission mode |
| `dataDir` | `BDESKTOP_DATA_DIR` | platform config dir | Session transcript storage |
| `workDir` | `BDESKTOP_WORKDIR` | process CWD | Default working directory |
| `contextWindow` | `BDESKTOP_CONTEXT_WINDOW` | `200000` | Context window size in tokens |
| `maxOutputTokens` | `BDESKTOP_MAX_OUTPUT_TOKENS` | `8192` | Max output tokens per request |
| `thinkingEnabled` | `BDESKTOP_THINKING_ENABLED` | `false` | Request extended thinking |
| `thinkingBudget` | `BDESKTOP_THINKING_BUDGET` | provider default | Thinking budget in tokens |
| `maxTurns` | `BDESKTOP_MAX_TURNS` | `100` | Max model calls per turn |
| `maxToolConcurrency` | `BDESKTOP_MAX_TOOL_CONCURRENCY` | `10` | Parallel tool limit |

`BDESKTOP_CONFIG` points at an alternative configuration file — handy for keeping several
profiles side by side:

```bash
BDESKTOP_CONFIG=~/.b-desktop/work.json wails dev
```

The Settings dialog writes the same file back, so you can configure the app from the UI
or from a dotfile.

---

## Permission modes

Every tool call is checked **before** it executes. The verdict is one of `allow`, `ask`
or `deny`; `ask` opens a dialog in the UI and the turn blocks until you answer.

| Mode | Read-only tools | Write / Edit | Bash | Other mutating tools |
|---|---|---|---|---|
| `default` | allow | ask | ask | ask |
| `acceptEdits` | allow | allow inside the work dir, else ask | ask | ask |
| `plan` | allow | deny | deny | deny |
| `bypassPermissions` | allow | allow | allow | allow |
| `dontAsk` | allow | deny | deny | deny |

- `plan` blocks every side effect, which makes it safe for read-only exploration.
- `dontAsk` behaves like `default` but converts every `ask` into a `deny`, so a session
  can never block on a dialog.
- `bypassPermissions` still respects explicit deny rules and built-in hard blocks.

Rules can additionally be attached per tool with `exact`, `prefix`, `glob` or `json`
matchers; deny always wins over allow. See `docs/agent-architecture.md` for the full
decision order.

---

## Tool reference

### Built-in

| Tool | Read-only | Concurrency-safe | Notes |
|---|---|---|---|
| `Read` | yes | yes | Offset/limit windowing, line numbers, rejects files over 256 KB, truncates output at the token budget |
| `Write` | no | no | Atomic write via temp file + rename; creates parent directories |
| `Edit` | no | no | `old_string` must match exactly once unless `replace_all` is set |
| `Glob` | yes | yes | `**` patterns, skips `.git` and `node_modules`, mtime-ordered |
| `Grep` | yes | yes | RE2 regex, glob include filter, skips binaries, context lines |
| `Bash` | no | no | `/bin/bash -lc` (POSIX) or `%COMSPEC% /d /s /c` (Windows), process-group kill on cancel |
| `TodoWrite` | no | no | In-memory task list; always allowed |
| `AskUserQuestion` | no | no | Structured multiple-choice question rendered in the UI |

All file tools resolve paths against the session working directory and refuse to
escape it, including through symlinks.

### Provided by extensions

| Tool | Source |
|---|---|
| `Skill` | Skills — loads the body of a `SKILL.md` on demand |
| `MemorySave` | Memory — appends a new memory and updates the index |
| `Agent` | Sub-agents — runs a sub-agent with its own context |
| `SendMessage` | Sub-agents — delivers a message to a running sub-agent |
| `TaskList` / `TaskOutput` / `TaskStop` | Background tasks — poll, tail and stop long-running commands |
| *(remote tools)* | MCP — every tool advertised by a configured MCP server |

---

## Extension system

### Skills

A skill is a directory containing `SKILL.md` with `name` and `description` in the
frontmatter. Only the description is injected into the context; the body is read on
demand when the model invokes the `Skill` tool.

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

`MEMORY.md` is the index; every memory also lives in its own file. At the start of a
turn, up to five memories whose keywords overlap the prompt are recalled and injected
into the context.

### Hooks

```json
{
  "hooks": {
    "PreToolUse": [{ "matcher": "Bash", "command": "./scripts/check.sh" }],
    "PostToolUse": [{ "matcher": "Write", "command": "./scripts/format.sh" }]
  }
}
```

A `PreToolUse` hook may escalate `allow` to `ask` or to `deny`; it can never relax a
decision that was already made. Hook failures are reported but never abort the turn.

### MCP

```json
{
  "mcpServers": {
    "filesystem": { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"] }
  }
}
```

MCP servers are started lazily on first use and stopped when the application shuts
down. The stdio transport is supported today.

### Sub-agents

```markdown
---
name: test-runner
description: Writes and runs tests for the current package.
---

You are a test specialist. Focus on the smallest failing test…
```

Sub-agent definitions are discovered from `~/.b-desktop/agents/*.md`. They run with
their own history, tool set and permission scope.

---

## Workspace panel

The right-hand side of the window is a first-class workspace, not an afterthought:

- **File tree** — lazy loading, right-click menu, create / rename / move / delete.
- **File preview** — tabbed, resizable, syntax highlighting, in-place editing.
- **Change review** — a git-backed diff view with staging, unstaging and discard.
  Untracked files are synthesised into an "all new" diff.
- **Search** — filename and content search inside the working directory.

All file mutations are confined to the session working directory: lexical escapes and
symlinks pointing outside it are rejected. Writes are atomic and preserve the original
file mode. See `docs/workspace-panel.md` for the details.

---

## Streaming events

The backend pushes a typed event per meaningful transition. The UI never waits for a
turn to finish to render output.

| Event | Meaning |
|---|---|
| `turn_queued` / `turn_start` / `turn_end` | Turn lifecycle, including queued turns |
| `step_start` | A model call is about to happen |
| `message_start` / `message_stop` | Assistant message boundaries |
| `block_start` | A text, thinking or tool block begins |
| `text_delta` | Incremental assistant text |
| `thinking_delta` | Incremental reasoning text |
| `tool_call_start` / `tool_input_delta` / `tool_call_ready` | Tool call starts, streaming arguments, complete arguments |
| `tool_progress` | Intermediate progress reported by a running tool |
| `permission_request` / `permission_resolved` | Approval dialog raised and answered |
| `tool_result` | A tool finished (emitted per tool, not batched) |
| `usage` | Token usage for the finished message |
| `compact_start` / `compact_end` | Context compaction boundaries |
| `retry` | The provider is retrying a request |
| `error` / `message_discard` | Failures, including discarding a partially streamed message |

---

## Data layout

```text
~/.b-desktop/                        # user settings (0700)
├── config.json                      # provider, permission mode, token budgets (0600)
├── hooks.json                       # user-level hooks
├── mcp.json                         # MCP server definitions
├── agents/                          # sub-agent definitions (*.md)
├── skills/                          # user-level skills
└── memory/
    ├── MEMORY.md                    # memory index
    └── <memory>.md                  # one file per memory

<platform config dir>/b-desktop/     # session transcripts (JSONL, one file per session)
└── <sessionID>.jsonl

<project>/.b-desktop/                # project-level overrides
├── hooks.json
└── skills/
```

---

## Repository layout

```text
main.go                  Wails entry point: window options, asset embedding, bindings
internal/app/            Wails bindings — the only surface the frontend can call
internal/agent/          Agent loop, streaming events, session orchestration
internal/llm/            Anthropic / OpenAI streaming clients, model presets
internal/tool/           Tool contract, registry and built-in tools
internal/permission/     Permission modes, rule matching, decision engine
internal/context/        System prompt assembly, token budget, compaction
internal/session/        Append-only transcript, replay, titles
internal/memory/         Long-term memory store and recall
internal/skill/          SKILL.md discovery and loading
internal/mcp/            MCP client, transports, tool bridging
internal/subagent/       Sub-agent types, runners, messaging
internal/task/           Background task registry and control
internal/hook/           Hook configuration and execution
internal/telemetry/      Request tracing and usage statistics
internal/project/        Opened project list and worktrees
internal/workspace/      File browsing, writes and git change views
frontend/src/            React application (components, stores, lib)
frontend/wailsjs/        Generated TypeScript bindings — never edit by hand
docs/                    Architecture notes and extension contracts
```

---

## Development

```bash
wails dev                      # desktop app with hot reload

cd frontend
npm run typecheck              # tsc -b
npm run build                  # tsc -b && vite build
npm run harness                # browser harness on http://localhost:5173/harness.html
```

The **harness** mounts the real `<App/>` in a browser and replaces only the two Wails
layers (`window.go.app.App.*` and `window.runtime.*`) with in-memory fakes. It lets you
exercise the UI without launching the desktop app or touching the real screen — see
`frontend/HARNESS.md` for the assertion hooks.

```bash
go test ./...                  # Go tests (all offline)
go vet ./...
gofmt -l .                     # formatting check
go mod tidy && go mod vendor   # after dependency changes (vendor/ is gitignored)
```

---

## Project status

**Implemented:** the agent runtime and tool loop, streaming transport for Anthropic and
OpenAI, five permission modes with graphical approval, sessions with persistence and
resume, automatic compaction, skills, memory, hooks, MCP (stdio), sub-agents,
background tasks, request tracing and usage stats, the multi-session workbench, and the
workspace panel with git change review.

**Next, in order:** the local HTTP/WebSocket server for H5 remote access, then IM
adapters (Telegram / Lark / WeChat / DingTalk / WhatsApp).

---

## Credits

B Desktop reimplements the behaviour model of several excellent coding agents in Go.
The reference implementations that shaped its semantics are documented under
`docs/research/` — in particular the agent loop of Claude Code, the permission model of
Zed, and the extension contracts of Codex.

---

## License

This repository does not currently ship a license file. Add one before publishing the
code publicly.
