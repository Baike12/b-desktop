package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Baike12/b-desktop/internal/agent"
	"github.com/Baike12/b-desktop/internal/llm"
)

// 抽取器参数。
const (
	// extractTimeout 是单次抽取的模型调用超时。
	extractTimeout = 2 * time.Minute
	// DefaultMinInterval 是同一会话两次抽取的最小间隔。
	DefaultMinInterval = 90 * time.Second
	// defaultMaxTokens 是抽取调用的输出上限。
	defaultMaxTokens = 1024
	// maxPromptRunes / maxReplyRunes 是对话摘录里两段的字符上限。
	maxPromptRunes = 4000
	maxReplyRunes  = 4000
	// minPromptWords / minPromptRunes 是触发抽取的用户输入门槛
	//（≥3 个空格分词或 ≥12 个字符，兼容中文无空格）。
	minPromptWords = 3
	minPromptRunes = 12
)

// Change 是一次自动抽取的结果汇报，供 App 转成前端事件。
type Change struct {
	// SessionID 是触发抽取的会话。
	SessionID string
	// Saved/Updated/Deleted 是各操作生效条数。
	Saved   int
	Updated int
	Deleted int
	// Err 是失败原因；空字符串表示成功（含「无需记忆」）。
	Err string
}

// Extractor 实现轮末自动抽取：每轮正常结束后用一次小输出模型调用
// 决定保存、更新或删除哪些记忆（mem0 合并决策内联 + ZCode 单飞行调度）。
// TurnComplete 必须非阻塞：实现内部自行异步，见 agent.TurnObserver。
type Extractor struct {
	hub         *Hub
	provider    llm.Provider
	model       string
	now         func() time.Time
	notify      func(Change)
	minInterval time.Duration
	maxTokens   int

	mu      sync.Mutex
	last    map[string]time.Time
	busy    map[string]bool
	pending map[string]agent.TurnInfo
	wg      sync.WaitGroup
}

// ExtractorOption 是 Extractor 的可选参数。
type ExtractorOption func(*Extractor)

// WithNow 覆盖时钟（测试注入）。
func WithNow(now func() time.Time) ExtractorOption {
	return func(e *Extractor) { e.now = now }
}

// WithNotify 注册抽取完成回调（App 用它发前端事件）。
func WithNotify(notify func(Change)) ExtractorOption {
	return func(e *Extractor) { e.notify = notify }
}

// WithMinInterval 覆盖同会话抽取最小间隔（测试注入）。
func WithMinInterval(d time.Duration) ExtractorOption {
	return func(e *Extractor) { e.minInterval = d }
}

// NewExtractor 创建抽取器。provider 与 model 来自当前会话配置；
// hub 是双层记忆库。
func NewExtractor(hub *Hub, provider llm.Provider, model string, opts ...ExtractorOption) *Extractor {
	e := &Extractor{
		hub:         hub,
		provider:    provider,
		model:       model,
		now:         time.Now,
		minInterval: DefaultMinInterval,
		maxTokens:   defaultMaxTokens,
		last:        make(map[string]time.Time),
		busy:        make(map[string]bool),
		pending:     make(map[string]agent.TurnInfo),
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// TurnComplete 接收轮次完成通知。门槛不满足时直接丢弃；
// 同会话已有进行中的抽取时合并（保留最新一轮），当前轮跑完立即补一轮。
func (e *Extractor) TurnComplete(info agent.TurnInfo) {
	if e == nil || e.hub == nil || e.provider == nil || e.model == "" {
		return
	}
	if !extractWorthy(info) {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.busy[info.SessionID] {
		e.pending[info.SessionID] = info
		return
	}
	e.busy[info.SessionID] = true
	e.last[info.SessionID] = e.now()
	e.wg.Add(1)
	go e.loop(info)
}

// loop 执行抽取并消费合并的后续轮次。
func (e *Extractor) loop(info agent.TurnInfo) {
	defer e.wg.Done()
	for {
		e.run(info)
		e.mu.Lock()
		next, ok := e.pending[info.SessionID]
		if ok {
			delete(e.pending, info.SessionID)
		} else {
			e.busy[info.SessionID] = false
		}
		e.mu.Unlock()
		if !ok {
			return
		}
		info = next
	}
}

// Wait 等待所有进行中的抽取结束（Shutdown 时带超时调用）。
func (e *Extractor) Wait() { e.wg.Wait() }

// extractWorthy 判断一轮是否值得抽取。
func extractWorthy(info agent.TurnInfo) bool {
	if info.StopReason != agent.StopCompleted {
		return false
	}
	prompt := strings.TrimSpace(info.Prompt)
	if prompt == "" {
		return false
	}
	if len(strings.Fields(prompt)) >= minPromptWords || utf8.RuneCountInString(prompt) >= minPromptRunes {
		return true
	}
	return false
}

// run 执行一次完整抽取：组装输入 → 模型调用 → 解析 ops → 应用 → 回调。
// 任何失败只记录日志并回调 Err，不重试。
func (e *Extractor) run(info agent.TurnInfo) {
	notice := Change{SessionID: info.SessionID}
	defer func() {
		if e.notify != nil {
			e.notify(notice)
		}
	}()
	if rateLimited := e.rateLimited(info.SessionID); rateLimited {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), extractTimeout)
	defer cancel()

	// 召回命中条目已在会话内注入过，这里只带清单供模型引用合并。
	summary := e.hub.indexSummary(ctx, info.WorkDir)
	user := buildExtractUser(summary, info)
	text, err := e.callModel(ctx, user)
	if err != nil {
		notice.Err = err.Error()
		log.Printf("memory: extract session %s: %v", info.SessionID, err)
		return
	}
	ops, err := parseOps(text)
	if err != nil {
		notice.Err = err.Error()
		log.Printf("memory: parse extract ops session %s: %v", info.SessionID, err)
		return
	}
	saved, updated, deleted, applyErr := e.apply(ctx, info.WorkDir, ops)
	notice.Saved, notice.Updated, notice.Deleted = saved, updated, deleted
	if applyErr != nil {
		notice.Err = applyErr.Error()
	}
}

// rateLimited 报告该会话是否仍在最小间隔内；是则跳过本次。
func (e *Extractor) rateLimited(sessionID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if now := e.now(); now.Sub(e.last[sessionID]) < e.minInterval {
		return true
	}
	e.last[sessionID] = e.now()
	return false
}

// callModel 发起一次无工具的小输出模型调用，返回拼接后的文本。
func (e *Extractor) callModel(ctx context.Context, user string) (string, error) {
	temperature := 0.0
	stream, err := e.provider.Stream(ctx, llm.Request{
		Model:  e.model,
		System: extractSystemPrompt,
		Messages: []llm.Message{{
			Role:   llm.RoleUser,
			Blocks: []llm.ContentBlock{{Type: llm.BlockText, Text: user}},
		}},
		MaxTokens:   e.maxTokens,
		Temperature: &temperature,
	})
	if err != nil {
		return "", fmt.Errorf("start extraction request: %w", err)
	}
	defer stream.Close()
	var text strings.Builder
	for {
		ev, err := stream.Recv(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("extraction stream: %w", err)
		}
		if ev.Type == llm.EventTextDelta {
			text.WriteString(ev.Text)
		}
	}
	return text.String(), nil
}

// extractOp 是抽取模型输出的一条操作。
type extractOp struct {
	Op          string   `json:"op"`
	Scope       string   `json:"scope"`
	Target      string   `json:"target"`
	Type        string   `json:"type"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Keywords    []string `json:"keywords"`
	Importance  int      `json:"importance"`
	Content     string   `json:"content"`
	Reason      string   `json:"reason"`
}

// extractPayload 是抽取模型输出的顶层结构。
type extractPayload struct {
	Ops []extractOp `json:"ops"`
}

// parseOps 从模型输出中解析 ops JSON，容忍 markdown 代码栅栏与前后噪声。
func parseOps(text string) ([]extractOp, error) {
	trimmed := strings.TrimSpace(text)
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON object in extraction output")
	}
	var payload extractPayload
	if err := json.Unmarshal([]byte(trimmed[start:end+1]), &payload); err != nil {
		return nil, fmt.Errorf("decode extraction ops: %w", err)
	}
	return payload.Ops, nil
}

// apply 执行 ops；单个 op 失败跳过并继续，返回生效条数与最后一个错误。
func (e *Extractor) apply(ctx context.Context, workDir string, ops []extractOp) (saved, updated, deleted int, err error) {
	for _, op := range ops {
		scope := scopeOf(op.Scope)
		var opErr error
		switch strings.ToLower(strings.TrimSpace(op.Op)) {
		case "save":
			var created bool
			_, created, opErr = e.hub.Save(ctx, scope, workDir, EntryInput{
				Name:        op.Name,
				Description: op.Description,
				Type:        op.Type,
				Keywords:    op.Keywords,
				Importance:  clampImportance(op.Importance),
				Body:        op.Content,
			})
			if opErr == nil {
				if created {
					saved++
				} else {
					updated++
				}
			}
		case "update":
			fileName, lookupErr := e.resolveTarget(ctx, scope, workDir, op.Target)
			if lookupErr != nil {
				continue
			}
			_, changed, opErr := e.hub.Update(ctx, scope, workDir, fileName, EntryInput{
				Name:        op.Name,
				Description: op.Description,
				Type:        op.Type,
				Keywords:    op.Keywords,
				Importance:  clampImportance(op.Importance),
				Body:        op.Content,
			})
			if opErr == nil && changed {
				updated++
			}
		case "delete":
			fileName, lookupErr := e.resolveTarget(ctx, scope, workDir, op.Target)
			if lookupErr != nil {
				continue
			}
			if _, opErr = e.hub.Archive(ctx, scope, workDir, fileName); opErr == nil {
				deleted++
			}
		}
		if opErr != nil {
			err = opErr
		}
	}
	return saved, updated, deleted, err
}

// resolveTarget 把模型给的 target（文件名或记忆名）解析成文件名。
func (e *Extractor) resolveTarget(ctx context.Context, scope, workDir, target string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", fmt.Errorf("memory: empty extract target")
	}
	if _, err := e.hub.Get(scope, workDir, target); err == nil {
		return target, nil
	}
	entries, err := e.hub.List(scope, workDir)
	if err != nil {
		return "", err
	}
	for _, mem := range entries {
		if mem.Name == target {
			return mem.FileName, nil
		}
	}
	return "", fmt.Errorf("memory: extract target %q not found", target)
}

// clampImportance 把重要性限制到 0-10（0 表示未评分）。
func clampImportance(value int) int {
	if value < 0 {
		return 0
	}
	if value > 10 {
		return 10
	}
	return value
}

// extractSystemPrompt 是抽取调用的系统提示。
// 规则对齐 mem0 抽取 prompt 的要点（只抽稳定事实、自包含、引用既有条目合并）。
const extractSystemPrompt = `你是记忆管理员，负责从一段 agent 工作对话中提取值得长期保存的记忆，并维护记忆库。

规则：
1. 只提取无法从代码、git 历史、项目文档直接得到的稳定事实，按类型划分：
   - user：用户偏好与背景（技术栈偏好、沟通语言、角色）
   - feedback：用户对工作方式的纠正与偏好（「以后先写测试」「不要自动提交」）
   - project：项目背景、目标、约定、外部系统位置
   - reference：相关资源与链接位置
2. 不提取：一次性任务细节、执行过程、客套话、可以从当前仓库推导的内容。
3. 每条记忆 content 为 15-80 字，自包含，脱离本对话也能读懂；包含具体名称而不是「那个项目」「他说」。
4. 先看「既有记忆」清单：与某条既有记忆相关时用 update（target=文件名）合并修正，不要新增重复条目；既有记忆已确认过时且无法修正时用 delete。
5. 确实是全新事实才 save；scope：跨项目有用的用 user，仅与当前项目相关的用 project。
6. importance 取 1-10：用户明确纠正与强偏好 8-10，一般事实 4-7，背景信息 1-3。
7. 没有值得保存的内容时输出 {"ops":[]}。只输出 JSON，不要输出其他文字。

输出格式：
{"ops":[{"op":"save","scope":"user","type":"feedback","name":"提交前需确认","description":"用户要求 git 提交前必须先展示 diff","keywords":["git","提交","确认"],"importance":8,"content":"用户要求：git 提交前必须先展示完整 diff 并获得确认，不要自动提交。"}]}`

// buildExtractUser 组装抽取调用的用户消息。
func buildExtractUser(summary string, info agent.TurnInfo) string {
	var b strings.Builder
	b.WriteString("# 既有记忆\n")
	if strings.TrimSpace(summary) == "" {
		b.WriteString("（暂无记忆）\n\n")
	} else {
		b.WriteString(summary)
		b.WriteString("\n\n")
	}
	b.WriteString("# 本轮对话\n")
	b.WriteString("用户：" + truncateRunes(strings.TrimSpace(info.Prompt), maxPromptRunes) + "\n")
	if reply := strings.TrimSpace(info.FinalText); reply != "" {
		b.WriteString("助手：" + truncateRunes(reply, maxReplyRunes) + "\n")
	}
	if len(info.ToolNames) > 0 {
		b.WriteString("调用过的工具：" + strings.Join(info.ToolNames, ", ") + "\n")
	}
	return b.String()
}
