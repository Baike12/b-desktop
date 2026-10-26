# 侧边浏览器面板与 Agent 浏览器操控（设计）

目标：对齐 codex 桌面版 / ZCode 桌面版的「侧边内置浏览器 + agent 操控」能力。
硬约束：b-desktop 是 Wails v2 + 系统 WebView，**没有 Electron 的 `<webview>` 标签、
没有多窗口 API**，因此不能照搬 ZCode 的实现，显示层必须换方案。

## 0. 结论

**Go 后端托管一个真实 Chrome（CDP 控制），侧边面板用 `Page.startScreencast` JPEG
帧流显示画面；agent 走「a11y 快照 + ref」为主、「坐标」兜底的双轨工具；人直接看
帧流，输入事件转发给 Chrome——同一个浏览器实例，人机共用。**

```text
┌───────────────────────── Wails 单窗口（React）─────────────────────────┐
│ 对话区 │ … │ 浏览器面板 BrowserPane：地址栏 / tab 条 / <canvas> 帧流 / 状态 │
└───────────────────────────────┬────────────────────────────────────────┘
        绑定调用（面板控制）      │        EventsEmit 帧流 browser:frame
                                ▼
┌────────────────────────── Go 后端（单进程）────────────────────────────┐
│ internal/app/app_browser.go      薄绑定：面板用的打开/导航/输入转发       │
│ internal/browser/                领域包                                 │
│   manager.go   托管 Chrome 生命周期（spawn/attach/健康/空闲退出）          │
│   tab.go       tab 表、会话归属、激活与驻留                               │
│   command.go   BrowserCommand → BrowserCommandResult 结构化命令层         │
│   snapshot.go  Accessibility.getFullAXTree → ref 文本树 + ref 索引        │
│   screencast.go  Page.startScreencast 帧泵（节流/quality/按需启停）        │
│ internal/tool/builtin/browser_*.go  agent 工具（走权限五档，不经 Wails 绑定）│
└───────────────────────────────┬────────────────────────────────────────┘
                                │ CDP WebSocket（rod / proto 直发）
                                ▼
                    托管 Chrome（独立 user-data-dir，headed）
```

## 1. 参考实现怎么做的（调研结论）

| 项目 | 形态 | 架构 | 关键证据 |
|---|---|---|---|
| ZCode 桌面版 | Electron | renderer `<webview>` 实时合成画面 + main 进程 `webContents.debugger` 挂 CDP 操控；截图走 main↔renderer surface 握手 | `packages/desktop/src/main/browserView/browserGuestManager.ts`（attachGuest :519、CDP 空闲 1.5s 释放 :2969）；pivot 文档 `docs/browser-use/2026-07-08-webview-cdp-on-guest-pivot-spec.md`（弃用原生层视图盖 DOM） |
| openclaw | Node 网关 + macOS 原生壳 | 托管 spawn 独立 Chrome（隔离 profile，CDP 端口段 18800+）；web 控制台用 `Page.startScreencast` 流（50ms 间隔、2MB 缓冲） | `extensions/browser/src/browser/chrome.ts:748`、`screencast/session.ts:6,196`；扩展桥（chrome.debugger relay）为 opt-in |
| agent-browser (Vercel) | Rust CLI | CLI + daemon（Unix socket 换行 JSON），**裸 CDP WebSocket**（无 Playwright）；`Accessibility.getFullAXTree` 快照 + `@eN` ref，stale ref 按 role+name 自愈 | `cli/src/native/cdp/client.rs:47`、`snapshot.rs:308`、`element.rs:172-181`；launch/attach 双模式（`docs/src/app/cdp-mode`） |
| BrowserOS | Chromium fork | 浏览器内核监督 agent sidecar，sidecar 以 CDP **客户端**连回；Rust MCP：tabs/navigate/snapshot/act/screenshot | `chromium_patches/.../browseros_server_manager.cc:453,509`；`crates/browseros-mcp/src/tools/mod.rs:98` |
| codex（桌面版） | 闭源 App | 本仓库只有治理面：`InAppBrowser / BrowserUse / BrowserUseFullCdpAccess / ComputerUse` 是 feature flag，实现在 App 侧 | `codex-rs/features/src/lib.rs:242-277` |
| cc | CLI | 真实浏览器操控走 Chrome 扩展（Claude in Chrome，Native Messaging），非内嵌 | `cc/utils/claudeInChrome/chromeNativeHost.ts:1-31` |
| chrome-devtools-mcp（Google 官方） | MCP/CLI | Puppeteer 操控 live Chrome；`take_snapshot`（a11y 树 + uid）优先于 screenshot；交互工具以 uid 为参数；六条设计原则 | README Key features；`src/tools/snapshot.ts:18-19`、`input.ts:126-140`、`docs/design-principles.md` |

业界共识（五条，全部有上述证据）：

1. **显示与控制分离**：画面给「人看」，控制走一条结构化命令通道。
2. **a11y 快照 + ref 是 agent 的主表征**（省 token、可 diff、免视觉模型），截图只做视觉证据。
3. **驱动统一收敛到 CDP**；差别只在谁当宿主（内嵌 webview / 托管 Chrome / 扩展桥）。
4. **默认隔离 profile，attach 用户浏览器显式 opt-in**（openclaw driver 三分、codex `BrowserUseExternal` 独立 flag）。
5. **错误信息教模型下一步**（ZCode "Do not retry the same locator. Take a fresh domSnapshot"）。

## 2. Wails 约束（为何不能照搬 ZCode）

| 约束 | 证据 | 影响 |
|---|---|---|
| 系统 WebView 没有 `<webview>` 标签，无法在 DOM 内嵌第三方网页 | WKWebView/WebView2 无此元素；iframe 被 `X-Frame-Options`/CSP 阻断 | 显示层不能用「真实网页内嵌合成」，只能帧流 |
| Wails v2 只有单窗口 API | `vendor/github.com/wailsapp/wails/v2/pkg/runtime/window.go`（仅 SetTitle/SetSize/…，无创建窗口） | 不能为浏览器开第二个 Wails 窗口；面板放主窗口右侧工作区 |
| WKWebView 无 CDP；WebView2 的 `CallDevToolsProtocolMethod` 只对 App 自己的 WebView 有效 | 平台文档 | 操控对象必须是外部 Chrome，不能是系统 WebView |
| 前端无 Node runtime | AGENTS.md 进程模型 | Puppeteer/Playwright 排除；Go 侧用 rod |

## 3. 关键设计决策

1. **显示层 = screencast 帧流**（openclaw 同款）。
   `Page.startScreencast{format:jpeg, quality:60, maxWidth:1280, maxHeight:800}`，
   Go 侧节流 ≤15fps、面板不可见即停泵；帧 + metadata（`pageScaleFactor/scrollOffset/viewport`）
   经 `EventsEmit("browser:frame")` 推给前端 `<canvas>`。卡顿再降级为 loopback WebSocket。
2. **viewport 固定逻辑尺寸**（1280×800，`Emulation.setDeviceMetricsOverride` + 隐藏滚动条），
   前端 CSS transform fit 面板宽度——帧尺寸稳定、坐标换算简单、agent 看到的布局确定
   （ZCode agent viewport 固定 + zoom:fit 同款）。
3. **浏览器底座 = 托管 Chrome + rod**（三档）：
   - 默认 `launch`：spawn 本机 Chrome/Edge，`--remote-debugging-port=0` + `DevToolsActivePort`
     发现 ws 地址（agent-browser `chrome.rs:269-333` 模式），独立
     `user-data-dir`（隔离登录态），headed（用户看得见、可接管）。
   - `attach`（设置里显式开启 + 每会话确认）：连用户已开的 `--remote-debugging-port`
     Chrome，复用登录态。
   - `headless`：纯后台任务。
   rod 只当 CDP 传输层（README："directly based on DevTools Protocol... low-level use"），
   禁用其自动下载浏览器；命令层自研，对齐 ZCode 的 `ControlledView` 抽象。
4. **命令协议（宿主无关）**：`BrowserCommand{action,params} → BrowserCommandResult
   {ok, image?, snapshot?, tabs?, error{code,message,hint}}`；错误码枚举
   （`ref_not_found/timeout/navigation_blocked/…`）+ hint 文案。
5. **agent 表征双轨**：
   - snapshot 路：`Accessibility.getFullAXTree` → 过滤交互角色 → 分配 `eN` ref →
     缩进文本树；`click/fill` 用 ref → `DOM.getBoxModel` 中心点 → `Input.dispatchMouseEvent`；
     stale ref 按 role+name 重查自愈（agent-browser 模型）。OOPIF 用 frame 级 CDP session 递归。
   - 坐标路：`screenshot`（PNG/JPEG base64）+ 像素坐标 `cua` 动作，兜底 canvas/非语义元素。
   快照与页面文本一律以不可信内容边界包裹（openclaw untrusted-content、
   agent-browser `--content-boundaries` 模式）。
6. **agent 工具面 = 6 个细粒度工具**（chrome-devtools-mcp 多工具路线，贴合本项目
   「按工具名 + 参数」的权限模型与 isReadOnly 并行编排）：
   `browser_navigate`（只读）、`browser_snapshot`（只读）、`browser_screenshot`（只读）、
   `browser_tabs`（list/new/switch/close，只读为主）、`browser_act`（click/type/press/
   scroll/hover/select，写）、`browser_evaluate`（任意 JS，高危，default 模式必 ask）。
   配套 bundled skill 教「snapshot → ref → act → verify」循环。
7. **交互转发**：面板上的指针/滚轮/键盘事件按帧 metadata 换算 CSS 像素后
   `Input.dispatchMouseEvent / dispatchKeyEvent / insertText`——人可接管，与 agent 共用实例
   （ZCode human/agent 同一 webview 的哲学，在 Wails 下用事件转发实现）。
8. **权限与安全**：
   - 工具级：只读工具可并行，`browser_act/evaluate` 串行 + 走现有五档权限（default = ask）。
   - 站点权限（摄像头/通知/剪贴板读等）：CDP 侧 fail-closed 全拒绝，白名单除外
     （ZCode 三分类 + 默认拒绝模型）。
   - attach 模式与 `browser_evaluate` 都是显式高风险项，复用图形化审批。
9. **生命周期**：App 级单例 Chrome；tab 归属会话（`sessionId+tabId` 作用域，ZCode
   BrowserGuestExecutionContext 模型），每会话 tab 上限 16；同一时刻仅当前 tab 有实时帧，
   后台 tab 的操作用按需 screenshot；面板关闭且无 agent 动作 10 分钟 → 停泵，Chrome 空闲
   30 分钟 → 退出（agent-browser idle timeout 模式）；Chrome 崩溃 → 状态重置 + 面板提示重启。

## 4. 落地模块

| 层 | 位置 | 内容 |
|---|---|---|
| 领域包 | `internal/browser/` | manager / tab / command / snapshot / screencast（不依赖 Wails，可单测：CDP 接口打桩） |
| 工具 | `internal/tool/builtin/browser_navigate.go` 等 6 个 | 实现 Tool 契约（schema/描述/call/checkPermissions/isReadOnly），call 直接调 `internal/browser` |
| 绑定 | `internal/app/app_browser.go` | 面板专用薄绑定：BrowserEnsure/BrowserNavigate/BrowserTabs/BrowserInput/帧订阅开关 |
| 事件 | `runtime.EventsEmit` | `browser:frame`（帧）、`browser:state`（tab/加载/连接状态、agent 操作指示） |
| 前端 | `frontend/src/components/BrowserPane.tsx`、`stores/browser.ts` | 右侧工作区新增「浏览器」面板类型（与文件 tab 并列，agent 操作时自动打开）；canvas 渲染 + 事件回传 + 地址栏/tab 条 |
| Skill | `internal/skill` bundled | `browser` SKILL.md：快照循环、ref 生命周期、verify |

## 5. 分期

- **P0（可用）**：托管 launch 模式 + screencast 面板（帧流 + 输入转发）+ 6 工具 + 快照
  自愈 + 五档权限接入 + harness 假实现（静态帧）。
- **P1**：attach 用户 Chrome（显式开启 + 确认）、域名白名单设置、console/网络事件收集
  （`browser_console` 工具）、headless 模式设置项。
- **P2**：操作录制（screencast 帧落 WebM，chrome-devtools-mcp screencast.ts 同思路）、
  H5 远程看浏览器流（`internal/server` 复用同一帧泵）、多 viewport 档位。

## 6. 风险与对策

| 风险 | 对策 |
|---|---|
| 后台 tab 合成器停帧（headed Chrome 节流） | 面板只渲染当前 tab；操作后台 tab 前 `Target.activateTarget`，或按需 screenshot（ZCode 活性泵思路在独立 Chrome 下简化） |
| Wails 事件通道推帧压力大 | 15fps + quality 60 上限；实测不足改 loopback WS（`internal/server` 雏形） |
| spawn Chrome 抢焦点/闪窗口 | `--no-first-run --no-default-browser-check`；headed 窗口本就是「人机共用」特性，headless 档位可完全隐藏 |
| rod launcher 行为不可控 | 禁用自动下载；浏览器发现顺序：环境变量 `BDESK_CHROME_PATH` → 系统 Chrome → Edge → Chromium |
| a11y 树过大撑爆上下文 | 复用结果裁剪链路（`contentLimit` 中段截断）；快照按交互角色过滤 + 可选 CSS selector 裁剪（agent-browser 同款） |
