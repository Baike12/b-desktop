# b-desktop 记忆机制设计（v2）

本文是 `internal/memory` 的设计文档。调研证据见 `deps/notes/`（mem0/langmem、
MemOS/A-MEM/Graphiti 代码考古，8 篇论文精读，cc/ZCode/codex 本地实现对照），
源码快照在 `deps/<项目名>/`，论文在 `deps/pdfs/`（均不入库）。

## 0. 设计目标与约束

b-desktop 是单进程 Wails 桌面应用：没有向量数据库服务、没有图数据库、
LLM 调用按 token 计费。因此：

1. **存储是纯 markdown 文件**：用户可直接阅读、编辑、备份、同步。
2. **召回默认零模型成本**：确定性打分；唯一一次额外模型调用发生在轮末
   自动抽取（可选、限频、小输出）。
3. **模型不能直接写记忆目录**：记忆目录在用户主目录，超出会话 Write/Edit
   的 workDir 沙箱；唯一写路径是专用工具，与项目权限模型一致。
4. **失败不影响会话**：抽取失败、召回失败都静默降级，绝不阻塞主循环。

## 1. 调研结论 → 设计决策对照

| 决策 | 来源 |
|---|---|
| 条目 = 一个 markdown 文件 + frontmatter（name/description/type/keywords/importance/时间戳/sources） | cc memdir、A-MEM MemoryNote（deps/notes/local-cc-zcode-codex.md、memos-amem-graphiti.md） |
| MEMORY.md 索引常驻系统提示，200 行 / 25KB 截断，写入后全量重建 | cc 与 ZCode 的 default-index 分支（上限数字完全一致） |
| 相关记忆按轮注入正文，带 staleness 标注 | cc findRelevantMemories（>1 天标注） |
| 打分 = 相关度 + 新近性指数衰减 + 重要性 | Generative Agents memory stream（0.995^t 形状，归一化权重） |
| 写入去重与合并：同 hash 幂等、同名提示更新、抽取时显式给 target 合并 | mem0 V3（ADD-only + linked ids）、MemOS 向量粗筛 + 合并 |
| 冲突与删除：软删 archived，不物理删除 | Zep 双时间线（失效不删除）、MemOS 状态机 |
| 轮末自动抽取：每轮完成后触发、单飞行 + 合并、失败不重试 | ZCode TurnComplete 调度、cc 轮末 fork 兜底 |
| 抽取用一次 LLM 调用同时输出 save/update/delete 与 target | mem0（合并决策内联）、A-MEM（一次演化新记忆与邻居） |
| 抽取门槛：prompt ≥3 词、≥90s 限频、仅 StopCompleted | ZCode（≥3 词、单飞行）、cc（仅成功轮推进） |
| 侧查询精选（findRelevantMemories 那种每轮一次小模型调用） | 明确不采纳：桌面场景下每轮多一次模型调用的延迟与成本高于收益；索引常驻 + 确定性召回已覆盖主需求，未来可作为设置项 |

明确不采纳（对单机桌面过重）：向量库（mem0/MemOS/A-MEM 的嵌入检索）、
知识图谱与时序失效边（Graphiti/Neo4j）、MemCube 调度器全家桶、
A-MEM 每次写入都演化邻居（成本不可控）。

## 2. 存储模型

```text
~/.b-desktop/memory/MEMORY.md                    用户级索引（自动重建）
~/.b-desktop/memory/<slug>.md                    用户级记忆条目
~/.b-desktop/memory/projects/<slug>-<sha16>/     项目级记忆目录
~/.b-desktop/memory/projects/<slug>-<sha16>/MEMORY.md
~/.b-desktop/memory/projects/<slug>-<sha16>/<slug>.md
```

- `<slug>-<sha16>`：项目名 + 绝对路径 SHA-256 前 16 位（hex），同一项目
  的不同 worktree/子目录各自一档（简单、确定性、不依赖 git）。
- 条目文件格式（frontmatter 字段全部向后兼容缺省，v1 文件无需迁移）：

```markdown
---
name: 偏好-深色主题
description: 用户所有界面偏好深色主题
type: user                # user | feedback | project | reference
keywords: ["主题", "深色"]   # 召回加权字段，抽取时由模型生成
importance: 8             # 1-10，写入时定，召回时参与打分
sources: ["turn:xxx"]     # 溯源（会话轮次），可空
createdAt: 2026-10-26T20:00:00+08:00
updatedAt: 2026-10-26T20:00:00+08:00
lastAccessedAt: 2026-10-26T20:00:00+08:00
accessCount: 3
status: active            # active | archived（软删，不参与召回与注入）
hash: <md5(body)>         # 内容指纹，写入幂等去重
---

记忆正文……
```

## 3. 注入与召回

每轮用户输入到达时，记忆扩展（`ContextProvider`）产出两段文本：

1. `## 长期记忆索引`：用户级 + 当前项目级的 MEMORY.md 合并
   （各 200 行 / 25KB 截断），让模型知道「有哪些记忆存在」；
2. `## 相关记忆`：对 prompt 打分取 top-5 的正文
   （单条 ≤2000 字符、总计 ≤8000 字符，>1 天的条目标注
   「（更新于 N 天前）」）。

打分公式（`score.go`）：

```text
score = relevance + recency + importance
relevance = 3×|prompt∩(name∪description∪keywords)| + 1×|prompt∩body|
recency   = 3×exp(-days(lastAccessedAt)/14)
importance= 0.15×importance
```

- 无词元重合的条目不进入候选（索引已兜底可见性）。
- 命中后节流更新 `lastAccessedAt`/`accessCount`（每条 >1 小时才落盘一次）。

## 4. 写入路径

### 4.1 模型显式工具（主轨）

| 工具 | 语义 |
|---|---|
| `MemorySave` | 新建记忆；body hash 与现有条目相同则幂等返回；同名则生成 `-2` 后缀新文件 |
| `MemoryUpdate` | 按 fileName 局部更新字段；内容未变则 no-op |
| `MemoryDelete` | 软删：`status: archived`，从索引移除，文件保留 |

三个工具都带 `scope`（user/project）参数；工具实例绑定会话 workDir，
project 路由到对应项目库。工具描述内嵌「该记什么/不该记什么」清单
（cc 四类型 + 不存清单）。

### 4.2 轮末自动抽取（辅轨，可关闭）

引擎新增可选扩展接口 `agent.TurnObserver`：轮次以 `StopCompleted`
结束时，引擎调用一次（实现必须自行异步，不得阻塞循环）。
App 层把 `memory.Extractor` 与 hooks.json 钩子经 `hooksChain` 组合注入。

`memory.Extractor.TurnComplete` 流程：

```text
门槛检查（completed / prompt≥3 词 / 距上次≥90s / 设置开启）
  → 单飞行 + 合并（同一会话有进行中的抽取则记 pending，跑完立即补一轮）
  → 组装输入：本轮 prompt + 助手最终回复 + 工具名清单（≤8k 字符）
              + 既有记忆清单（name+description，≤60 条）
  → 一次 LLM 调用（max_tokens 1024，temperature 0，无工具）
  → 解析 JSON ops（容忍代码栅栏）：
       {"ops":[{"op":"save|update|delete", "target":..., "scope":..., ...}]}
  → 执行：save 先查重（同名/同 hash 转合并）；update/delete 按 fileName 定位
  → EmitMemoryChanged 事件（前端刷新）
```

抽取 prompt 的过滤规则（mem0 抽取 prompt 要点）：只保存无法从代码、
git 历史、文档直接得到的稳定事实（用户偏好、纠正反馈、项目背景、外部
资源位置）；每条 15-80 词、自包含；不抽客套话、过程性细节、一次性任务。

生命周期：抽取 goroutine 注册到 App 级 WaitGroup，Shutdown 时与轮次
一起等待（超时 60s 后放弃）。

## 5. App 绑定与前端

- 绑定（`internal/app/app_memory.go`）：`ListMemories(scope, workDir)`、
  `ReadMemory(scope, workDir, fileName)`、`SaveMemory`、`UpdateMemory`、
  `DeleteMemory`。
- 事件：`memory:changed`（scope + 计数），前端订阅后刷新列表。
- 设置：`config.json` 增加 `memoryAutoExtract`（默认 true）。
- 设置对话框新增「记忆」页：轮末自动抽取开关、用户级/项目级切换、列表、
  查看/编辑正文、新建、删除。视觉沿用暖调工作台规范（语义色、等宽
  元信息、圆角分层）。

## 6. 验证

- `internal/memory`：存储往返、v1 兼容、去重幂等、索引重建与截断、
  打分（衰减/重要性/staleness）、抽取 ops 解析与门槛、单飞行合并。
- `internal/agent`：TurnObserver 只在 StopCompleted 触发一次。
- `internal/app`：绑定与 DTO、hooksChain 组合、事件负载。
- 前端：`npm run typecheck`、`npm run build`、harness 冒烟
  （设置页记忆列表增删改、事件刷新）。
