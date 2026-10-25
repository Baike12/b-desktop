package app

import (
	"fmt"
	"os"

	"github.com/Baike12/b-desktop/internal/agent"
	"github.com/Baike12/b-desktop/internal/mcp"
	"github.com/Baike12/b-desktop/internal/subagent"
	"github.com/Baike12/b-desktop/internal/task"
	"github.com/Baike12/b-desktop/internal/telemetry"
)

// buildExtensions 汇总一个会话可用的扩展组件（工具、钩子、上下文提供者、指标、子 Agent）。
//
// 各组件经 extensionStore 惰性创建并复用单例。单个组件初始化失败不阻断会话：
// 错误写到 os.Stderr 后跳过该组件，其余扩展照常装配，引擎以可用的子集启动。
// 约定与签名见 docs/extensions-contract.md。
func (a *App) buildExtensions(workDir string) (agent.Extensions, error) {
	var ext agent.Extensions

	// Skills：清单注入上下文 + Skill 工具按需读取正文。
	if loader, err := a.skillsComponent(workDir); err != nil {
		reportExtensionError("skills", err)
	} else {
		ext.Tools = append(ext.Tools, loader.Tools()...)
		ext.Context = append(ext.Context, loader)
	}

	// 记忆：相关记忆注入上下文 + MemorySave 工具。
	if store, err := a.memoryComponent(); err != nil {
		reportExtensionError("memory", err)
	} else {
		ext.Tools = append(ext.Tools, store.Tools()...)
		ext.Context = append(ext.Context, store)
	}

	// Hooks：用户级 + 项目级 hooks.json。
	if hooks, err := a.hooksComponent(workDir); err != nil {
		reportExtensionError("hooks", err)
	} else {
		ext.Hooks = hooks
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
