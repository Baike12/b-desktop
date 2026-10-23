// Package builtin 实现内置工具。
package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Baike12/b-desktop/internal/tool"
)

// defaultMaxResults 是 Glob 与 Grep 在未指定 max_results 时的结果上限。
const defaultMaxResults = 200

// globSchema 是 Glob 的输入 schema。
const globSchema = `{
  "type": "object",
  "properties": {
    "pattern": {
      "type": "string",
      "description": "Glob pattern relative to path. Supports ** (any number of directories), *, ?, and character classes such as [a-z]."
    },
    "path": {
      "type": "string",
      "description": "Directory to search. Defaults to the workspace root."
    },
    "max_results": {
      "type": "integer",
      "minimum": 1,
      "description": "Maximum number of paths to return. Defaults to 200."
    }
  },
  "required": ["pattern"],
  "additionalProperties": false
}`

// globTool 按 glob 模式遍历目录。它不持有可变状态，可并发调用。
type globTool struct {
	root string // 已解析符号链接的绝对工作目录
}

// NewGlob 创建 Glob 工具。root 是会话工作目录，所有搜索路径都被限制在它之下。
// 返回的实例只读且并发安全。
func NewGlob(root string) tool.Tool {
	return &globTool{root: globResolveRoot(root)}
}

// Name 返回工具名。
func (g *globTool) Name() string { return "Glob" }

// Description 返回工具说明。
func (g *globTool) Description() string {
	return "Find files by glob pattern. Supports ** for recursive matching and * / ? / [abc] within a path segment. " +
		"Skips .git, node_modules and vendor directories. Results are ordered by modification time (newest first) " +
		"with a stable path tie-break, and paths are returned relative to the workspace root."
}

// InputSchema 返回输入 JSON Schema。
func (g *globTool) InputSchema() json.RawMessage { return json.RawMessage(globSchema) }

// ReadOnly 报告 Glob 不修改文件系统。
func (g *globTool) ReadOnly(json.RawMessage) bool { return true }

// ConcurrencySafe 报告 Glob 可以与其他安全工具并行执行。
func (g *globTool) ConcurrencySafe(json.RawMessage) bool { return true }

// globInput 是 Glob 的输入。
type globInput struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path,omitempty"`
	MaxResults int    `json:"max_results,omitempty"`
}

// globMatch 是一个待排序的匹配项。
type globMatch struct {
	path  string // 相对 root 的斜杠路径
	mtime int64  // UnixNano，用于倒序排序
}

// Run 执行 Glob。
func (g *globTool) Run(ctx context.Context, input json.RawMessage, progress tool.ProgressFunc) (tool.Result, error) {
	var in globInput
	if err := decodeSearchInput(input, &in); err != nil {
		return searchError("glob: %v", err), nil
	}
	in.Pattern = strings.TrimSpace(in.Pattern)
	if in.Pattern == "" {
		return searchError("glob: pattern is required"), nil
	}
	if in.MaxResults <= 0 {
		in.MaxResults = defaultMaxResults
	}

	searchPath, err := globResolveInRoot(g.root, in.Path)
	if err != nil {
		return searchError("glob: %v", err), nil
	}
	info, err := os.Stat(searchPath)
	if err != nil {
		return searchError("glob: %v", err), nil
	}

	matches := make([]globMatch, 0, 32)
	walkErr := filepath.WalkDir(searchPath, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// 单个条目不可读不应中断整次搜索。
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if d.IsDir() {
			if p != searchPath && globSkipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}

		rel, relErr := filepath.Rel(searchPath, p)
		if relErr != nil {
			return nil
		}
		name := filepath.ToSlash(rel)
		if !info.IsDir() {
			// path 直接指向一个文件时，按文件名匹配。
			name = d.Name()
		}
		if !globMatchPath(in.Pattern, name) {
			return nil
		}

		outRel, outErr := filepath.Rel(g.root, p)
		if outErr != nil {
			return nil
		}
		var mtime int64
		if fi, infoErr := d.Info(); infoErr == nil {
			mtime = fi.ModTime().UnixNano()
		}
		matches = append(matches, globMatch{path: filepath.ToSlash(outRel), mtime: mtime})
		return nil
	})
	if walkErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return tool.Result{}, fmt.Errorf("glob: %w", ctxErr)
		}
		return tool.Result{}, fmt.Errorf("glob: walk: %w", walkErr)
	}

	// 按修改时间倒序；时间相同时按路径升序，保证输出稳定。
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].mtime == matches[j].mtime {
			return matches[i].path < matches[j].path
		}
		return matches[i].mtime > matches[j].mtime
	})

	truncated := false
	if len(matches) > in.MaxResults {
		matches = matches[:in.MaxResults]
		truncated = true
	}

	if progress != nil {
		progress(tool.Progress{Text: fmt.Sprintf("%d files matched", len(matches))})
	}

	if len(matches) == 0 {
		return tool.Result{
			Content: "no files found",
			Meta:    map[string]string{"count": "0"},
		}, nil
	}

	var b strings.Builder
	for i, m := range matches {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(m.path)
	}
	return tool.Result{
		Content:   b.String(),
		Truncated: truncated,
		Meta: map[string]string{
			"count":     fmt.Sprintf("%d", len(matches)),
			"truncated": fmt.Sprintf("%t", truncated),
		},
	}, nil
}

// decodeSearchInput 解析工具输入。空输入视为零值，由各工具报告必填字段缺失。
func decodeSearchInput(raw json.RawMessage, dst any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	if err := json.Unmarshal(trimmed, dst); err != nil {
		return fmt.Errorf("invalid input: %w", err)
	}
	return nil
}

// searchError 构造一个可供模型阅读的错误结果。
func searchError(format string, args ...any) tool.Result {
	return tool.Result{Content: fmt.Sprintf(format, args...), IsError: true}
}

// globResolveRoot 把 root 归一化为绝对路径并解析符号链接。
// 优先复用 paths.go 的 resolveRoot；目录不存在时退回绝对路径，
// 由 Run 里的 Resolve/Stat 报出结构化错误。
func globResolveRoot(root string) string {
	if real, err := resolveRoot(root); err == nil {
		return real
	}
	if root == "" {
		if wd, err := os.Getwd(); err == nil {
			root = wd
		}
	}
	if root == "" {
		return "."
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return filepath.Clean(root)
}

// globResolveInRoot 把输入路径解析成 root 内的绝对路径。
// 相对路径相对 root；越出 root 的路径被拒绝。解析规则统一委托 paths.go 的 Resolve，
// 避免与 Read/Write/Edit 出现多份符号链接与越界判定。
func globResolveInRoot(root, p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		p = "."
	}
	return Resolve(root, p)
}

// globSkipDir 报告目录是否应被跳过。
func globSkipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor":
		return true
	}
	return false
}

// globMatchPath 报告 path 是否匹配 glob 模式，支持 **、*、? 与字符类。
// 模式与路径都以 / 分隔；模式为空或语法非法时返回 false。
// 这里不使用 filepath.Match：它把 ** 当作普通 *，且 * 不能跨目录。
func globMatchPath(pattern, path string) bool {
	// 契约：模式与路径都用 / 分隔。反斜杠在模式里是转义符，在路径里是 Windows 分隔符。
	pattern = strings.TrimPrefix(pattern, "./")
	path = strings.TrimPrefix(strings.ReplaceAll(path, "\\", "/"), "./")
	if pattern == "" {
		return false
	}
	pat := splitGlobPattern(pattern)
	seg := strings.Split(path, "/")
	memo := make(map[[2]int]bool)
	seen := make(map[[2]int]bool)
	return globMatchSegments(pat, seg, 0, 0, memo, seen)
}

// splitGlobPattern 拆分模式并折叠连续斜杠。
func splitGlobPattern(pattern string) []string {
	raw := strings.Split(pattern, "/")
	out := raw[:0]
	for _, s := range raw {
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}

// globMatchSegments 逐段匹配，** 可吞掉任意多段（含零段）。
func globMatchSegments(pat, seg []string, pi, si int, memo, seen map[[2]int]bool) bool {
	key := [2]int{pi, si}
	if seen[key] {
		return memo[key]
	}
	seen[key] = true
	result := false
	switch {
	case pi == len(pat):
		result = si == len(seg)
	case pat[pi] == "**":
		// 零段
		if globMatchSegments(pat, seg, pi+1, si, memo, seen) {
			result = true
			break
		}
		// 一段或多段
		if si < len(seg) && globMatchSegments(pat, seg, pi, si+1, memo, seen) {
			result = true
		}
	case si == len(seg):
		result = false
	default:
		if globMatchSegment(pat[pi], seg[si]) {
			result = globMatchSegments(pat, seg, pi+1, si+1, memo, seen)
		}
	}
	memo[key] = result
	return result
}

// globMatchSegment 匹配单个路径段。段内不含分隔符。
func globMatchSegment(pattern, name string) bool {
	p := []rune(pattern)
	s := []rune(name)
	return globMatchRunes(p, s)
}

// globMatchRunes 是段内匹配器，支持 *、?、字符类与反斜杠转义。
func globMatchRunes(p, s []rune) bool {
	for pi, si := 0, 0; ; {
		if pi == len(p) {
			return si == len(s)
		}
		switch p[pi] {
		case '*':
			for pi < len(p) && p[pi] == '*' {
				pi++
			}
			if pi == len(p) {
				return true
			}
			for k := si; k <= len(s); k++ {
				if globMatchRunes(p[pi:], s[k:]) {
					return true
				}
			}
			return false
		case '?':
			if si >= len(s) {
				return false
			}
			pi++
			si++
		case '[':
			if si >= len(s) {
				return false
			}
			ok, next := globMatchClass(p, pi, s[si])
			if next < 0 {
				// 未闭合的字符类按字面量 '[' 处理。
				if s[si] != '[' {
					return false
				}
				pi++
				si++
				continue
			}
			if !ok {
				return false
			}
			pi = next
			si++
		case '\\':
			if pi+1 < len(p) {
				if si >= len(s) || s[si] != p[pi+1] {
					return false
				}
				pi += 2
				si++
				continue
			}
			if si >= len(s) || s[si] != '\\' {
				return false
			}
			pi++
			si++
		default:
			if si >= len(s) || s[si] != p[pi] {
				return false
			}
			pi++
			si++
		}
	}
}

// globMatchClass 匹配 p[start] 处的字符类。返回 (是否匹配, 类结束后的下标)；
// 未闭合时返回 (false, -1)。
func globMatchClass(p []rune, start int, c rune) (bool, int) {
	i := start + 1
	negate := false
	if i < len(p) && (p[i] == '!' || p[i] == '^') {
		negate = true
		i++
	}
	matched := false
	first := true
	for i < len(p) {
		if p[i] == ']' && !first {
			return matched != negate, i + 1
		}
		lo := p[i]
		if lo == '\\' && i+1 < len(p) {
			i++
			lo = p[i]
		}
		i++
		hi := lo
		if i+1 < len(p) && p[i] == '-' && p[i+1] != ']' {
			i++
			hi = p[i]
			if hi == '\\' && i+1 < len(p) {
				i++
				hi = p[i]
			}
			i++
		}
		if c >= lo && c <= hi {
			matched = true
		}
		first = false
	}
	return false, -1
}
