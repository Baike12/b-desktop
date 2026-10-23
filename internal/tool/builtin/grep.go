package builtin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Baike12/b-desktop/internal/tool"
)

// grepSchema 是 Grep 的输入 schema。
const grepSchema = `{
  "type": "object",
  "properties": {
    "pattern": {
      "type": "string",
      "description": "RE2 regular expression matched against each line."
    },
    "path": {
      "type": "string",
      "description": "File or directory to search. Defaults to the workspace root."
    },
    "include": {
      "type": "string",
      "description": "Glob filter for file paths. A pattern without / matches the file base name at any depth; otherwise it matches the path relative to path. Supports **, *, ?, and character classes."
    },
    "max_results": {
      "type": "integer",
      "minimum": 1,
      "description": "Maximum number of matching lines to return. Defaults to 200."
    },
    "context_lines": {
      "type": "integer",
      "minimum": 0,
      "maximum": 5,
      "description": "Number of surrounding lines to include for each match. Defaults to 0."
    }
  },
  "required": ["pattern"],
  "additionalProperties": false
}`

// grepTool 用 RE2 正则逐文件搜索。它不持有可变状态，可并发调用。
type grepTool struct {
	root string // 已解析符号链接的绝对工作目录
}

// NewGrep 创建 Grep 工具。root 是会话工作目录，所有搜索路径都被限制在它之下。
// 返回的实例只读且并发安全。
func NewGrep(root string) tool.Tool {
	return &grepTool{root: globResolveRoot(root)}
}

// Name 返回工具名。
func (g *grepTool) Name() string { return "Grep" }

// Description 返回工具说明。
func (g *grepTool) Description() string {
	return "Search file contents with an RE2 regular expression. Outputs one hit per line as path:line:text, " +
		"with paths relative to the workspace root. Skips binary files (NUL byte in the first 512 bytes) and the " +
		".git, node_modules and vendor directories. Use include to restrict files by glob."
}

// InputSchema 返回输入 JSON Schema。
func (g *grepTool) InputSchema() json.RawMessage { return json.RawMessage(grepSchema) }

// ReadOnly 报告 Grep 不修改文件系统。
func (g *grepTool) ReadOnly(json.RawMessage) bool { return true }

// ConcurrencySafe 报告 Grep 可以与其他安全工具并行执行。
func (g *grepTool) ConcurrencySafe(json.RawMessage) bool { return true }

// grepInput 是 Grep 的输入。
type grepInput struct {
	Pattern      string `json:"pattern"`
	Path         string `json:"path,omitempty"`
	Include      string `json:"include,omitempty"`
	MaxResults   int    `json:"max_results,omitempty"`
	ContextLines int    `json:"context_lines,omitempty"`
}

// Run 执行 Grep。
func (g *grepTool) Run(ctx context.Context, input json.RawMessage, progress tool.ProgressFunc) (tool.Result, error) {
	var in grepInput
	if err := decodeSearchInput(input, &in); err != nil {
		return searchError("grep: %v", err), nil
	}
	if strings.TrimSpace(in.Pattern) == "" {
		return searchError("grep: pattern is required"), nil
	}
	re, err := regexp.Compile(in.Pattern)
	if err != nil {
		return searchError("grep: invalid regular expression: %v", err), nil
	}
	if in.MaxResults <= 0 {
		in.MaxResults = defaultMaxResults
	}
	if in.ContextLines < 0 {
		in.ContextLines = 0
	}
	if in.ContextLines > 5 {
		in.ContextLines = 5
	}

	searchPath, err := globResolveInRoot(g.root, in.Path)
	if err != nil {
		return searchError("grep: %v", err), nil
	}
	if _, statErr := os.Stat(searchPath); statErr != nil {
		return searchError("grep: %v", statErr), nil
	}

	lines := make([]string, 0, 64)
	matchCount := 0
	filesScanned := 0
	truncated := false

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
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil || !info.Mode().IsRegular() {
			return nil
		}

		rel, relErr := filepath.Rel(searchPath, p)
		if relErr != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if relSlash == "." {
			relSlash = d.Name()
		}
		if in.Include != "" {
			target := relSlash
			if !strings.Contains(in.Include, "/") {
				target = d.Name()
			}
			if !globMatchPath(in.Include, target) {
				return nil
			}
		}

		outRel, outErr := filepath.Rel(g.root, p)
		if outErr != nil {
			outRel = p
		}
		filesScanned++
		if progress != nil && filesScanned%100 == 0 {
			progress(tool.Progress{Text: fmt.Sprintf("%d files scanned", filesScanned)})
		}

		limit := in.MaxResults - matchCount
		if limit <= 0 {
			truncated = true
			return fs.SkipAll
		}
		fileTruncated, fileErr := grepFile(ctx, p, filepath.ToSlash(outRel), re, limit, in.ContextLines, &lines, &matchCount)
		if fileErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			// 单个文件读取失败不应中断整次搜索。
			return nil
		}
		if fileTruncated {
			truncated = true
			return fs.SkipAll
		}
		return nil
	})
	if walkErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return tool.Result{}, fmt.Errorf("grep: %w", ctxErr)
		}
		return tool.Result{}, fmt.Errorf("grep: walk: %w", walkErr)
	}

	if progress != nil {
		progress(tool.Progress{Text: fmt.Sprintf("%d matches in %d files", matchCount, filesScanned)})
	}

	if len(lines) == 0 {
		return tool.Result{
			Content: "no matches found",
			Meta: map[string]string{
				"matches": "0",
				"files":   fmt.Sprintf("%d", filesScanned),
			},
		}, nil
	}
	return tool.Result{
		Content:   strings.Join(lines, "\n"),
		Truncated: truncated,
		Meta: map[string]string{
			"matches":   fmt.Sprintf("%d", matchCount),
			"files":     fmt.Sprintf("%d", filesScanned),
			"truncated": fmt.Sprintf("%t", truncated),
		},
	}, nil
}

// grepFile 在单个文件中搜索。匹配行输出 path:line:text，上下文行输出 path-line-text，
// 不相邻的上下文块之间用 -- 分隔。limit 是本文件最多还能追加的匹配行数。
func grepFile(ctx context.Context, path, display string, re *regexp.Regexp, limit, contextLines int, out *[]string, matchCount *int) (truncated bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	head := make([]byte, 512)
	n, readErr := io.ReadFull(f, head)
	if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
		return false, readErr
	}
	if bytes.IndexByte(head[:n], 0) >= 0 {
		return false, nil // 二进制文件
	}
	if _, seekErr := f.Seek(0, io.SeekStart); seekErr != nil {
		return false, seekErr
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	var ring []string
	lineNo := 0
	matchesHere := 0
	lastPrinted := 0
	trailUntil := 0 // 当前匹配之后仍可输出为上下文的最后一行
	for scanner.Scan() {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, ctxErr
		}
		lineNo++
		line := strings.TrimSuffix(scanner.Text(), "\r")

		if re.MatchString(line) {
			start := lineNo - contextLines
			if start < 1 {
				start = 1
			}
			if contextLines > 0 && lastPrinted > 0 && start > lastPrinted+1 {
				*out = append(*out, "--")
			}
			base := lineNo - len(ring)
			for i, prev := range ring {
				prevNo := base + i
				if prevNo < start || prevNo <= lastPrinted {
					continue
				}
				*out = append(*out, fmt.Sprintf("%s-%d-%s", display, prevNo, prev))
				lastPrinted = prevNo
			}
			*out = append(*out, fmt.Sprintf("%s:%d:%s", display, lineNo, line))
			lastPrinted = lineNo
			trailUntil = lineNo + contextLines
			*matchCount++
			matchesHere++
			if matchesHere >= limit {
				return true, nil
			}
		} else if lastPrinted > 0 && lineNo > lastPrinted && lineNo <= trailUntil {
			*out = append(*out, fmt.Sprintf("%s-%d-%s", display, lineNo, line))
			lastPrinted = lineNo
		}

		if contextLines > 0 {
			ring = append(ring, line)
			if len(ring) > contextLines {
				ring = ring[1:]
			}
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		return false, scanErr
	}
	return false, nil
}
