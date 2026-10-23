package subagent

import (
	"strings"
)

// parseAgentMarkdown 解析一个子 Agent 定义文件。
//
// 格式与参考实现一致：文件以 "---" 开始，frontmatter 以一行 "---" 结束，
// 之后是正文。frontmatter 只使用 name 与 description 两个字段（其余字段被忽略，
// 便于与 Claude Code 的 agents 目录共用文件）。缺少任一字段时返回 ok=false，
// 该文件被静默跳过，不影响其他定义。
func parseAgentMarkdown(path string, data []byte) (AgentType, bool) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return AgentType{}, false
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return AgentType{}, false
	}

	fields := make(map[string]string, 8)
	for i := 1; i < end; i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "|" || value == ">" || value == "|-" || value == ">-" {
			block, next := blockScalar(lines, i+1, end)
			i = next - 1
			if value[0] == '|' {
				fields[key] = strings.Join(block, "\n")
			} else {
				fields[key] = strings.Join(block, " ")
			}
			continue
		}
		fields[key] = unquoteFrontmatter(value)
	}

	name := strings.TrimSpace(fields["name"])
	description := strings.TrimSpace(fields["description"])
	if name == "" || description == "" {
		return AgentType{}, false
	}
	return AgentType{
		Name:         name,
		Description:  description,
		SystemPrompt: strings.TrimSpace(strings.Join(lines[end+1:], "\n")),
		Path:         path,
	}, true
}

// blockScalar 读取 YAML 的 "|" / ">" 块标量：收集缩进的后续行直到 frontmatter 结束
// 或遇到未缩进的非空行。返回内容行与下一行的下标。
func blockScalar(lines []string, start, end int) ([]string, int) {
	var raw []string
	i := start
	for ; i < end; i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			raw = append(raw, "")
			continue
		}
		if line[0] != ' ' && line[0] != '\t' {
			break
		}
		raw = append(raw, line)
	}
	// 去掉尾部空行，再按第一行的缩进裁剪每行。
	for len(raw) > 0 && strings.TrimSpace(raw[len(raw)-1]) == "" {
		raw = raw[:len(raw)-1]
	}
	indent := 0
	for _, line := range raw {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent = len(line) - len(strings.TrimLeft(line, " \t"))
		break
	}
	out := make([]string, len(raw))
	for k, line := range raw {
		if len(line) >= indent {
			out[k] = line[indent:]
		} else {
			out[k] = ""
		}
	}
	return out, i
}

// unquoteFrontmatter 去掉成对的引号，并把 \\n 还原成换行
// （参考实现同样对 description 做换行还原）。
func unquoteFrontmatter(value string) string {
	if len(value) >= 2 {
		first, last := value[0], value[len(value)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			value = value[1 : len(value)-1]
		}
	}
	return strings.ReplaceAll(value, `\n`, "\n")
}
