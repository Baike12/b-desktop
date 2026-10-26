package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Baike12/b-desktop/internal/agent"
	"github.com/Baike12/b-desktop/internal/llm"
	"github.com/Baike12/b-desktop/internal/mcp"
	"github.com/Baike12/b-desktop/internal/memory"
	"github.com/Baike12/b-desktop/internal/permission"
	"github.com/Baike12/b-desktop/internal/subagent"
	"github.com/Baike12/b-desktop/internal/task"
	"github.com/Baike12/b-desktop/internal/telemetry"
	"github.com/Baike12/b-desktop/internal/tool"
)

// buildExtensions 汇总一个会话可用的扩展组件（工具、钩子、上下文提供者、指标、子 Agent）。
//
// 各组件经 extensionStore 惰性创建并复用单例。单个组件初始化失败不阻断会话：
// 错误写到 os.Stderr 后跳过该组件，其余扩展照常装配，引擎以可用的子集启动。
// 约定与签名见 docs/extensions-contract.md。
// provider 与 model 供轮末记忆抽取复用当前会话配置；gen 是配置代数，
// 抽取器跨会话共享、按代数重建。
func (a *App) buildExtensions(workDir string, provider llm.Provider, cfg Config, gen int64) (agent.Extensions, error) {
	var ext agent.Extensions
	var chain hooksChain

	// Skills：清单注入上下文 + Skill 工具按需读取正文。
	if loader, err := a.skillsComponent(workDir); err != nil {
		reportExtensionError("skills", err)
	} else {
		ext.Tools = append(ext.Tools, loader.Tools()...)
		ext.Context = append(ext.Context, loader)
	}

	// 记忆：索引与相关记忆注入上下文 + Memory 工具 + 轮末自动抽取。
	if hub, err := a.memoryComponent(); err != nil {
		reportExtensionError("memory", err)
	} else {
		ext.Tools = append(ext.Tools, hub.ToolsFor(workDir)...)
		ext.Context = append(ext.Context, memoryContext{hub: hub})
		if cfg.MemoryAutoExtract && provider != nil && strings.TrimSpace(cfg.Model) != "" {
			if extractor := a.memoryExtractor(hub, provider, cfg.Model, gen); extractor != nil {
				chain = append(chain, memoryHooks{extractor: extractor})
			}
		}
	}

	// Hooks：用户级 + 项目级 hooks.json。
	if hooks, err := a.hooksComponent(workDir); err != nil {
		reportExtensionError("hooks", err)
	} else {
		chain = append(chain, hooks)
	}
	if len(chain) > 0 {
		ext.Hooks = chain
	}

	// MCP：远端工具包装；Tools 首次调用会惰性启动服务器。
	if manager, err := a.mcpComponent(); err != nil {
		reportExtensionError("mcp", err)
	} else {
		ext.Tools = append(ext.Tools, manager.Tools()...)
	}

	// SubAgent：Agent 工具与子 Agent 执行器。
	if manager, err := a.subagentComponent(workDir); err != nil {
		reportExtensionError("subagent", err)
	} else {
		ext.Tools = append(ext.Tools, manager.Tools()...)
		ext.Subagent = manager
	}

	// 后台任务：TaskOutput / TaskStop / TaskList 三个查询与控制工具。
	if reg, err := a.taskRegistryComponent(); err != nil {
		reportExtensionError("tasks", err)
	} else {
		ext.Tools = append(ext.Tools,
			task.NewTaskOutput(reg), task.NewTaskStop(reg), task.NewTaskList(reg))
	}

	// 用量追踪：模型调用指标落盘。
	if sink, err := a.telemetryComponent(); err != nil {
		reportExtensionError("telemetry", err)
	} else {
		ext.Metrics = sink
	}

	return ext, nil
}

// memoryContext 把记忆 Hub 适配成 agent.ContextProvider。
// 会话工作目录来自引擎调用参数，项目级记忆按会话解析。
type memoryContext struct {
	hub *memory.Hub
}

// ExtraContext 返回索引与相关记忆注入文本；空库返回空字符串。
func (m memoryContext) ExtraContext(ctx context.Context, sessionID, workDir, prompt string) string {
	if strings.TrimSpace(workDir) == "" {
		return ""
	}
	return m.hub.ContextText(ctx, workDir, prompt)
}

// memoryHooks 把 memory.Extractor 适配成 agent.Hooks：只承担轮末通知，
// 其余钩子为空操作。
type memoryHooks struct {
	extractor *memory.Extractor
}

// SessionStart 实现 Hooks：记忆抽取不关心会话开始。
func (h memoryHooks) SessionStart(context.Context, string, string) error { return nil }

// UserPromptSubmit 实现 Hooks：不追加文本。
func (h memoryHooks) UserPromptSubmit(context.Context, string, string) (string, error) {
	return "", nil
}

// PreToolUse 实现 Hooks：不改变权限判定。
func (h memoryHooks) PreToolUse(context.Context, agent.ToolEvent) (permission.Effect, string, error) {
	return permission.EffectAllow, "", nil
}

// PostToolUse 实现 Hooks：空操作。
func (h memoryHooks) PostToolUse(context.Context, agent.ToolEvent, tool.Result) {}

// TurnComplete 转发给抽取器。
func (h memoryHooks) TurnComplete(info agent.TurnInfo) { h.extractor.TurnComplete(info) }

// hooksChain 组合多个 Hooks 实现：引擎只接受一个 Hooks 值，而 hooks.json
// 管理器与轮末记忆抽取需要同时启用。各方法逐个调用并按最保守语义合并。
type hooksChain []agent.Hooks

// SessionStart 依次调用，错误聚合返回。
func (c hooksChain) SessionStart(ctx context.Context, sessionID, workDir string) error {
	var errs []error
	for _, hooks := range c {
		if err := hooks.SessionStart(ctx, sessionID, workDir); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// UserPromptSubmit 依次调用，非空返回值按序拼接。
func (c hooksChain) UserPromptSubmit(ctx context.Context, sessionID, prompt string) (string, error) {
	var (
		parts []string
		errs  []error
	)
	for _, hooks := range c {
		text, err := hooks.UserPromptSubmit(ctx, sessionID, prompt)
		if strings.TrimSpace(text) != "" {
			parts = append(parts, strings.TrimSpace(text))
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return strings.Join(parts, "\n"), errors.Join(errs...)
}

// PreToolUse 依次调用；deny 覆盖一切，ask 只能升级 allow，与单钩子语义一致。
// 钩子错误不改变既有判定。
func (c hooksChain) PreToolUse(ctx context.Context, ev agent.ToolEvent) (permission.Effect, string, error) {
	effect := permission.EffectAllow
	reason := ""
	for _, hooks := range c {
		hookEffect, hookReason, err := hooks.PreToolUse(ctx, ev)
		if err != nil {
			continue
		}
		switch hookEffect {
		case permission.EffectDeny:
			return permission.EffectDeny, hookReason, nil
		case permission.EffectAsk:
			if effect != permission.EffectDeny {
				effect = permission.EffectAsk
				reason = hookReason
			}
		}
	}
	return effect, reason, nil
}

// PostToolUse 依次调用。
func (c hooksChain) PostToolUse(ctx context.Context, ev agent.ToolEvent, res tool.Result) {
	for _, hooks := range c {
		hooks.PostToolUse(ctx, ev, res)
	}
}

// TurnComplete 转发给实现了 agent.TurnObserver 的成员。
func (c hooksChain) TurnComplete(info agent.TurnInfo) {
	for _, hooks := range c {
		if observer, ok := hooks.(agent.TurnObserver); ok {
			observer.TurnComplete(info)
		}
	}
}

// memoryExtractor 返回跨会话共享的轮末抽取器；配置代数变化时重建
// （provider 持有密钥与端点，必须跟随设置刷新）。
func (a *App) memoryExtractor(hub *memory.Hub, provider llm.Provider, model string, gen int64) *memory.Extractor {
	a.extractMu.Lock()
	defer a.extractMu.Unlock()
	if a.extractor != nil && a.extractorGen == gen {
		return a.extractor
	}
	extractor := memory.NewExtractor(hub, provider, model, memory.WithNotify(func(change memory.Change) {
		a.emitter.EmitMemoryChanged(a.lifeCtx(), MemoryChangedDTO{
			SessionID: change.SessionID,
			Saved:     change.Saved,
			Updated:   change.Updated,
			Deleted:   change.Deleted,
			Error:     change.Err,
		})
	}))
	a.extractor = extractor
	a.extractorGen = gen
	return extractor
}

// reportExtensionError 把组件初始化失败写到 stderr；扩展失败不阻断会话。
func reportExtensionError(component string, err error) {
	fmt.Fprintf(os.Stderr, "app: extension %s disabled: %v\n", component, err)
}

// closeExtensions 关闭并释放全部扩展组件。按依赖顺序执行：
// 子 Agent（取消后台任务）→ 后台任务注册表（杀后台进程）→ MCP（结束服务器进程）→ 用量存储。
// 每个组件的 Close 都要求可重复调用；extData 在关闭前摘空，避免重复关闭。
func (a *App) closeExtensions() {
	a.extMu.Lock()
	values := make([]any, 0, len(a.extData))
	for _, value := range a.extData {
		values = append(values, value)
	}
	a.extData = nil
	a.extMu.Unlock()

	for _, value := range values {
		if manager, ok := value.(*subagent.Manager); ok {
			_ = manager.Close()
		}
	}
	for _, value := range values {
		if reg, ok := value.(*task.Registry); ok {
			_ = reg.Close()
		}
	}
	for _, value := range values {
		if manager, ok := value.(*mcp.Manager); ok {
			_ = manager.Close()
		}
	}
	for _, value := range values {
		if store, ok := value.(*telemetry.Store); ok {
			_ = store.Close()
		}
	}
}
