package permission

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// errInvalidGlob 表示 glob 模式无法编译。
var errInvalidGlob = errors.New("permission: invalid glob pattern")

// matchState 是一条规则对一次请求的判定结论。
// 三值而不是布尔：prefix 规则遇到含 shell 元字符的命令时必须能表达
// 「无法判定」，让调用方 fail-closed，而不是猜成匹配或不匹配。
type matchState int

const (
	// matchNo 表示规则不适用于本次调用。
	matchNo matchState = iota
	// matchYes 表示规则命中本次调用。
	matchYes
	// matchUndecidable 表示规则适用于该工具，但本次输入无法判定。
	matchUndecidable
)

// toolMatches 报告规则是否覆盖该工具名；规则工具名 "*" 覆盖全部工具。
func toolMatches(r Rule, toolName string) bool {
	return r.Tool == "*" || r.Tool == toolName
}

// matchRule 计算一条规则对请求的匹配结论。workDir 为已解析的绝对工作目录。
//
// 语义：
//   - Pattern 为空：该工具的全部调用（MatchGlob 在规则校验阶段已被拒绝）；
//   - MatchGlob：每一个 req.Paths 相对 workDir 的路径都命中才算匹配，
//     Paths 为空或任一路径在工作目录外都不匹配（fail-closed）；
//   - MatchExact：与输入中的命令字符串整串相等；
//   - MatchPrefix：命令不含 shell 元字符时才判定，否则返回 matchUndecidable；
//   - MatchJSON：Pattern 是输入 JSON 原文的子串。
func matchRule(r Rule, req Request, workDir string) matchState {
	if !toolMatches(r, req.ToolName) {
		return matchNo
	}
	if r.MatchMode == MatchGlob {
		return matchGlobRule(r.Pattern, workDir, req.Paths)
	}
	if r.Pattern == "" {
		return matchYes
	}
	switch r.MatchMode {
	case MatchExact:
		cmd, ok := commandOf(req.Input)
		if !ok {
			return matchUndecidable
		}
		if cmd == r.Pattern {
			return matchYes
		}
		return matchNo
	case MatchPrefix:
		cmd, ok := commandOf(req.Input)
		if !ok {
			return matchUndecidable
		}
		if containsShellMeta(cmd) {
			return matchUndecidable
		}
		if strings.HasPrefix(cmd, r.Pattern) {
			return matchYes
		}
		return matchNo
	case MatchJSON:
		if strings.Contains(string(req.Input), r.Pattern) {
			return matchYes
		}
		return matchNo
	default:
		// 校验阶段已拒绝未知 MatchMode；运行期再次出现按不匹配处理。
		return matchNo
	}
}

// matchGlobRule 判断所有路径是否都命中 glob 模式，全部命中才算匹配。
func matchGlobRule(pattern, workDir string, paths []string) matchState {
	if len(paths) == 0 || workDir == "" {
		return matchNo
	}
	for _, p := range paths {
		rel, ok := relativePath(workDir, p)
		if !ok {
			return matchNo
		}
		if !matchGlob(pattern, rel) {
			return matchNo
		}
	}
	return matchYes
}

// relativePath 把绝对路径转成相对 workDir 的斜杠路径。
// 路径不在工作目录内、或任一侧不是绝对路径时返回 false（fail-closed）。
func relativePath(workDir, p string) (string, bool) {
	if !filepath.IsAbs(workDir) || !filepath.IsAbs(p) {
		return "", false
	}
	rel, err := filepath.Rel(filepath.Clean(workDir), filepath.Clean(p))
	if err != nil {
		return "", false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// pathsWithin 报告 paths 是否全部位于 workDir 内。
// 用于 acceptEdits：无法证明全部在目录内时返回 false，让调用方继续走 ask。
func pathsWithin(workDir string, paths []string) bool {
	if workDir == "" || len(paths) == 0 {
		return false
	}
	for _, p := range paths {
		if _, ok := relativePath(workDir, p); !ok {
			return false
		}
	}
	return true
}

// commandOf 取出输入 JSON 中的命令字符串。
// 权限层不认识工具名，只识别通用的 "command" 字段；字段缺失表示无法判定。
func commandOf(input json.RawMessage) (string, bool) {
	if len(input) == 0 {
		return "", false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(input, &obj); err != nil {
		return "", false
	}
	raw, ok := obj["command"]
	if !ok {
		return "", false
	}
	var cmd string
	if err := json.Unmarshal(raw, &cmd); err != nil {
		return "", false
	}
	return cmd, true
}

// shellMetaChars 是 shell 元字符集合。含这些字符的复合命令不能被前缀规则判定。
const shellMetaChars = "&|;<>$`\\(){}*?[]~#\n\r"

// containsShellMeta 报告命令是否含 shell 元字符。
func containsShellMeta(s string) bool {
	return strings.ContainsAny(s, shellMetaChars)
}

// matchGlob 报告斜杠路径 path 是否匹配 glob 模式。
// 支持 `**`（跨任意层目录，含零层）、`*`（不跨 `/`）、`?` 与字符类；
// 模式非法时返回 false。
func matchGlob(pattern, path string) bool {
	re, err := compileGlob(pattern)
	if err != nil {
		return false
	}
	return re.MatchString(path)
}

// validateGlobPattern 在规则写入时提前拒绝非法 glob。
func validateGlobPattern(pattern string) error {
	_, err := compileGlob(pattern)
	return err
}

// compileGlob 把 glob 模式编译成锚定的正则表达式。
func compileGlob(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); {
		switch c := pattern[i]; c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				j := i + 2
				if j < len(pattern) && pattern[j] == '/' {
					// `**/` 匹配零层或多层目录。
					b.WriteString("(?:[^/]*/)*")
					i = j + 1
				} else {
					b.WriteString(".*")
					i = j
				}
				continue
			}
			b.WriteString("[^/]*")
			i++
		case '?':
			b.WriteString("[^/]")
			i++
		case '[':
			class, next, err := globCharClass(pattern, i)
			if err != nil {
				return nil, err
			}
			b.WriteString(class)
			i = next
		case '\\':
			if i+1 >= len(pattern) {
				return nil, fmt.Errorf("%w: trailing escape", errInvalidGlob)
			}
			b.WriteString(regexp.QuoteMeta(pattern[i+1 : i+2]))
			i += 2
		default:
			b.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
			i++
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errInvalidGlob, err)
	}
	return re, nil
}

// globCharClass 解析从 pattern[start]（'['）开始的字符类，返回正则片段与下一个下标。
func globCharClass(pattern string, start int) (string, int, error) {
	j := start + 1
	if j >= len(pattern) {
		return "", 0, fmt.Errorf("%w: unterminated character class", errInvalidGlob)
	}
	negate := false
	if pattern[j] == '!' || pattern[j] == '^' {
		negate = true
		j++
	}
	classStart := j
	for j < len(pattern) && pattern[j] != ']' {
		j++
	}
	if j >= len(pattern) {
		return "", 0, fmt.Errorf("%w: unterminated character class", errInvalidGlob)
	}
	if j == classStart {
		return "", 0, fmt.Errorf("%w: empty character class", errInvalidGlob)
	}
	content := pattern[classStart:j]
	content = strings.ReplaceAll(content, `\`, `\\`)
	content = strings.ReplaceAll(content, `]`, `\]`)
	var b strings.Builder
	b.WriteByte('[')
	if negate {
		b.WriteByte('^')
	}
	b.WriteString(content)
	b.WriteByte(']')
	return b.String(), j + 1, nil
}

// builtinDeny 是纵深防御用的内置硬拒绝表，不构成安全边界。
// 只识别常见字面形式：rm -rf /、mkfs*、dd of=/dev/*；不解析 shell。
func builtinDeny(input json.RawMessage) (string, bool) {
	cmd, ok := commandOf(input)
	if !ok {
		return "", false
	}
	fields := strings.Fields(strings.ToLower(cmd))
	if len(fields) == 0 {
		return "", false
	}
	if fields[0] == "sudo" {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return "", false
	}
	switch name := path.Base(fields[0]); {
	case strings.HasPrefix(name, "mkfs"):
		return "builtin:mkfs", true
	case name == "rm":
		if isRecursiveForceRoot(fields[1:]) {
			return "builtin:rm-root", true
		}
	case name == "dd":
		for _, arg := range fields[1:] {
			if strings.HasPrefix(arg, "of=/dev/") {
				return "builtin:dd-device", true
			}
		}
	}
	return "", false
}

// isRecursiveForceRoot 报告 rm 参数是否同时包含递归与强制标志，且目标为根目录。
func isRecursiveForceRoot(args []string) bool {
	recursive, force := false, false
	target := ""
	for _, arg := range args {
		switch {
		case arg == "--recursive":
			recursive = true
		case arg == "--force":
			force = true
		case strings.HasPrefix(arg, "--"):
			// 其他长选项与目标无关。
		case strings.HasPrefix(arg, "-") && len(arg) > 1:
			flags := arg[1:]
			if strings.ContainsAny(flags, "rR") {
				recursive = true
			}
			if strings.Contains(flags, "f") {
				force = true
			}
		default:
			target = arg
		}
	}
	if !recursive || !force {
		return false
	}
	return target == "/" || target == "/*" || target == "//"
}
