package claude

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Baike12/b-desktop/internal/acp"
)

// toolView 是一次工具调用的展示信息，移植 claude-agent-acp 的 reporters：
// 每个内置工具给出标题、类别与触达位置。
type toolView struct {
	Title     string
	Kind      acp.ToolKind
	Locations []acp.ToolCallLocation
}

// TODO todos 的输入结构（TodoWrite）。
type todoInput struct {
	Todos []struct {
		Content    string `json:"content"`
		Status     string `json:"status"`
		ActiveForm string `json:"activeForm,omitempty"`
	} `json:"todos"`
}

// displayPath 把文件路径缩写为「相对 cwd 的路径」，过长时只保留文件名。
func displayPath(path, cwd string) string {
	if path == "" {
		return ""
	}
	if cwd != "" {
		if rel, err := filepath.Rel(cwd, path); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return filepath.Base(path)
}

// toolViewFor 按工具名与参数生成展示信息；未知工具回退为「工具名 + JSON 参数」。
// 映射对齐 claude-agent-acp 的 reporters/index.ts。
func toolViewFor(name string, input json.RawMessage, cwd string) toolView {
	fields := map[string]any{}
	_ = json.Unmarshal(input, &fields)
	str := func(keys ...string) string {
		for _, key := range keys {
			if value, ok := fields[key].(string); ok {
				return value
			}
		}
		return ""
	}
	num := func(keys ...string) (float64, bool) {
		for _, key := range keys {
			if value, ok := fields[key].(float64); ok {
				return value, true
			}
		}
		return 0, false
	}
	view := toolView{Kind: acp.ToolOther, Title: name}
	switch name {
	case "Read":
		path := str("file_path", "path")
		view = toolView{Kind: acp.ToolRead, Title: "Read " + displayPath(path, cwd)}
		if offset, ok := num("offset"); ok {
			line := int(offset)
			if line < 1 {
				line = 1
			}
			view.Locations = []acp.ToolCallLocation{{Path: path, Line: line - 1}}
			if limit, ok := num("limit"); ok {
				view.Title += fmt.Sprintf(" (off %d-%d)", line, line+int(limit)-1)
			} else {
				view.Title += fmt.Sprintf(" (from line %d)", line)
			}
		} else if path != "" {
			view.Locations = []acp.ToolCallLocation{{Path: path, Line: 0}}
		}
	case "Edit", "Write", "NotebookEdit":
		path := str("file_path", "path", "notebook_path")
		verb := strings.ToUpper(name[:1]) + name[1:]
		if name == "NotebookEdit" {
			verb = "Edit cell"
			if mode := str("edit_mode"); mode == "insert" {
				verb = "Insert cell"
			} else if mode == "delete" {
				verb = "Delete cell"
			}
			view.Title = fmt.Sprintf("%s %s in %s", verb, str("notebook_id"), displayPath(path, cwd))
		} else {
			view.Title = verb + " " + displayPath(path, cwd)
		}
		view.Kind = acp.ToolEdit
		view.Locations = []acp.ToolCallLocation{{Path: path}}
	case "Bash", "PowerShell":
		command := str("command")
		view.Kind = acp.ToolExecute
		if command != "" {
			view.Title = command
		} else {
			view.Title = "Terminal"
		}
	case "Glob":
		view.Kind = acp.ToolSearch
		pattern := str("pattern")
		path := str("path")
		view.Title = fmt.Sprintf("Find %q %q", path, pattern)
		if path != "" {
			view.Locations = []acp.ToolCallLocation{{Path: path}}
		}
	case "Grep":
		view.Kind = acp.ToolSearch
		view.Title = grepCommandLine(fields)
		if path := str("path", "file_path"); path != "" {
			view.Locations = []acp.ToolCallLocation{{Path: path}}
		}
	case "WebFetch":
		view.Kind = acp.ToolFetch
		url := str("url")
		view.Title = "Fetch " + url
	case "WebSearch":
		view.Kind = acp.ToolFetch
		view.Title = fmt.Sprintf("Search %q", str("query"))
	case "TodoWrite":
		view.Kind = acp.ToolThink
		view.Title = "Update TODOs"
	case "Task", "Agent":
		view.Kind = acp.ToolThink
		if description := str("description"); description != "" {
			view.Title = description
		} else {
			view.Title = "Task"
		}
	case "EnterPlanMode", "ExitPlanMode":
		view.Kind = acp.ToolSwitchMode
		view.Title = name
	case "Skill":
		view.Kind = acp.ToolOther
		view.Title = "Load skill: " + str("skill", "name")
	default:
		if server, ok := strings.CutPrefix(name, "mcp__"); ok {
			_, tool, found := strings.Cut(server, "__")
			if found && tool != "" {
				view.Title = tool
			}
		}
	}
	return view
}

// grepCommandLine 把 Grep 参数还原成等效 grep 命令行，移植适配器的实现。
func grepCommandLine(fields map[string]any) string {
	get := func(key string) string {
		value, _ := fields[key].(string)
		return value
	}
	boolFlag := func(key, flag string) string {
		if value, ok := fields[key].(bool); ok && value {
			return " " + flag
		}
		return ""
	}
	var builder strings.Builder
	builder.WriteString("grep")
	builder.WriteString(boolFlag("case_insensitive", " -i"))
	builder.WriteString(boolFlag("multiline", " -P"))
	builder.WriteString(" -n")
	if output, ok := fields["output_mode"].(string); ok {
		switch output {
		case "files_with_matches":
			builder.WriteString(" -l")
		case "count":
			builder.WriteString(" -c")
		}
	}
	if head, ok := fields["head_limit"].(float64); ok && head > 0 {
		builder.WriteString(fmt.Sprintf(" | head -%d", int(head)))
	}
	for _, key := range []string{"-A", "-B", "-C"} {
		if value, ok := fields[key].(float64); ok && value > 0 {
			builder.WriteString(fmt.Sprintf(" %s %d", key, int(value)))
		}
	}
	if includes, ok := fields["include"].([]any); ok && len(includes) > 0 {
		patterns := make([]string, 0, len(includes))
		for _, item := range includes {
			if s, ok := item.(string); ok {
				patterns = append(patterns, "--include "+s)
			}
		}
		builder.WriteString(" " + strings.Join(patterns, " "))
	}
	pattern := get("pattern")
	builder.WriteString(fmt.Sprintf(" %q", pattern))
	if path := get("path"); path != "" {
		builder.WriteString(" " + path)
	}
	return builder.String()
}

// toolKindFor 只取类别，供权限请求的 ToolCallUpdate 使用。
func toolKindFor(name string) acp.ToolKind {
	return toolViewFor(name, nil, "").Kind
}

// parseTodos 解析 TodoWrite 的 todos 参数；非法输入返回 nil。
func parseTodos(input json.RawMessage) []acp.PlanEntry {
	var todo todoInput
	if err := json.Unmarshal(input, &todo); err != nil || len(todo.Todos) == 0 {
		return nil
	}
	entries := make([]acp.PlanEntry, 0, len(todo.Todos))
	for _, item := range todo.Todos {
		if strings.TrimSpace(item.Content) == "" {
			continue
		}
		entries = append(entries, acp.PlanEntry{
			Content:  item.Content,
			Status:   item.Status,
			Priority: "medium",
		})
	}
	return entries
}
