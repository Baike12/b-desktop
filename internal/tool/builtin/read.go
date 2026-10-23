package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"anc-dsk/internal/tool"
)

const (
	// maxReadFileSize 是 Read 允许读取的最大文件字节数。
	maxReadFileSize = 256 * 1024
	// maxReadLines 是单次调用允许读取的最大行数。
	maxReadLines = 2000
	// maxReadTokens 是 Read 输出允许的最大估算 token 数。
	maxReadTokens = 25000
	// binaryProbeSize 是二进制探测的采样字节数（与 Grep 的跳过规则一致）。
	binaryProbeSize = 512
)

const readSchema = `{
  "type": "object",
  "properties": {
    "file_path": {
      "type": "string",
      "description": "Path to the file to read. Relative paths are resolved against the workspace root."
    },
    "offset": {
      "type": "integer",
      "minimum": 0,
      "description": "0-based line number to start reading from. Defaults to 0."
    },
    "limit": {
      "type": "integer",
      "minimum": 1,
      "maximum": 2000,
      "description": "Maximum number of lines to read. Defaults to 2000."
    },
    "show_line_numbers": {
      "type": "boolean",
      "description": "Prefix every line with its 1-based line number (6-wide, followed by a tab). Defaults to true."
    }
  },
  "required": ["file_path"],
  "additionalProperties": false
}`

type readInput struct {
	FilePath        string `json:"file_path"`
	Offset          int    `json:"offset,omitempty"`
	Limit           int    `json:"limit,omitempty"`
	ShowLineNumbers *bool  `json:"show_line_numbers,omitempty"`
}

// readTool 是 Read 的实现。
type readTool struct {
	fileBase
}

// NewRead 创建读取文件的 Read 工具。root 为工作区根目录，路径不得逃逸出该目录。
func NewRead(root string) tool.Tool {
	return &readTool{fileBase: newFileBase(root)}
}

// Name 返回工具名。
func (t *readTool) Name() string { return "Read" }

// Description 返回给模型看的用途说明。
func (t *readTool) Description() string {
	return "Reads a text file from the workspace. Relative paths are resolved against the workspace root; " +
		"paths that escape the root (including through symlinks) are rejected. " +
		"Files larger than 256KB, binary files (NUL bytes) and invalid UTF-8 are rejected. " +
		"Output is line-numbered by default; use offset and limit to page through a file (limit max 2000 lines)."
}

// InputSchema 返回 JSON Schema。
func (t *readTool) InputSchema() json.RawMessage { return json.RawMessage(readSchema) }

// ReadOnly 报告该次调用只读。
func (t *readTool) ReadOnly(json.RawMessage) bool { return true }

// ConcurrencySafe 报告该次调用可与其他安全调用并行。
func (t *readTool) ConcurrencySafe(json.RawMessage) bool { return true }

// AffectedPaths 返回本次调用会读取的绝对路径。
func (t *readTool) AffectedPaths(input json.RawMessage) ([]string, error) {
	var in readInput
	if err := decodeInput(input, &in); err != nil {
		return nil, err
	}
	path, err := t.resolve(in.FilePath)
	if err != nil {
		return nil, err
	}
	return []string{path}, nil
}

// Run 读取文件并返回带行号的文本。
func (t *readTool) Run(ctx context.Context, input json.RawMessage, progress tool.ProgressFunc) (tool.Result, error) {
	var in readInput
	if err := decodeInput(input, &in); err != nil {
		return errorf("Read: %v", err), nil
	}
	if in.Offset < 0 {
		return errorf("Read: offset must be >= 0, got %d", in.Offset), nil
	}
	limit := in.Limit
	if limit == 0 {
		limit = maxReadLines
	}
	if limit < 0 || limit > maxReadLines {
		return errorf("Read: limit must be between 1 and %d, got %d", maxReadLines, limit), nil
	}
	showNumbers := true
	if in.ShowLineNumbers != nil {
		showNumbers = *in.ShowLineNumbers
	}
	if err := ctx.Err(); err != nil {
		return errorf("Read: canceled: %v", err), nil
	}

	path, err := t.resolve(in.FilePath)
	if err != nil {
		return errorf("Read: %v", err), nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errorf("Read: file not found: %s", t.display(path)), nil
		}
		return errorf("Read: %s: %v", t.display(path), err), nil
	}
	if info.IsDir() {
		return errorf("Read: %s is a directory", t.display(path)), nil
	}
	if info.Size() > maxReadFileSize {
		return errorf("Read: %s is %d bytes, exceeding the %d byte limit", t.display(path), info.Size(), maxReadFileSize), nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return errorf("Read: %s: %v", t.display(path), err), nil
	}
	// Stat 与 ReadFile 之间文件可能变大，按实际读到的字节数再校验一次。
	if len(data) > maxReadFileSize {
		return errorf("Read: %s is %d bytes, exceeding the %d byte limit", t.display(path), len(data), maxReadFileSize), nil
	}
	if err := checkTextContent(data); err != nil {
		return errorf("Read: %s: %v", t.display(path), err), nil
	}
	if err := ctx.Err(); err != nil {
		return errorf("Read: canceled: %v", err), nil
	}
	if progress != nil {
		progress(tool.Progress{Bytes: int64(len(data)), Total: int64(len(data))})
	}

	lines := splitLines(string(data))
	if len(lines) == 0 {
		return tool.Result{
			Content: fmt.Sprintf("%s is empty (0 bytes)", t.display(path)),
			Meta:    map[string]string{"file_path": path, "total_lines": "0", "lines": "0"},
		}, nil
	}

	start := in.Offset
	if start >= len(lines) {
		return tool.Result{
			Content: fmt.Sprintf("%s has %d lines; offset %d is past the end", t.display(path), len(lines), in.Offset),
			Meta:    map[string]string{"file_path": path, "total_lines": strconv.Itoa(len(lines)), "lines": "0"},
		}, nil
	}
	end := start + limit
	if end > len(lines) {
		end = len(lines)
	}

	var b strings.Builder
	tokens, written := 0, 0
	truncated := false
	for i := start; i < end; i++ {
		frag := renderLine(i+1, lines[i], showNumbers)
		cost := estimateTokens(frag)
		if tokens+cost > maxReadTokens {
			truncated = true
			if written == 0 {
				b.WriteString(clipToTokens(frag, maxReadTokens))
				written++
			}
			break
		}
		b.WriteString(frag)
		tokens += cost
		written++
	}
	if truncated {
		b.WriteString(fmt.Sprintf("\n[truncated: output exceeded %d tokens]\n", maxReadTokens))
	}
	return tool.Result{
		Content:   b.String(),
		Truncated: truncated,
		Meta: map[string]string{
			"file_path":   path,
			"total_lines": strconv.Itoa(len(lines)),
			"lines":       strconv.Itoa(written),
		},
	}, nil
}

// checkTextContent 判定内容是否是可按文本处理的 UTF-8。
// 前 512 字节含 NUL，或整体不是合法 UTF-8，都按二进制拒绝。
func checkTextContent(data []byte) error {
	probe := data
	if len(probe) > binaryProbeSize {
		probe = probe[:binaryProbeSize]
	}
	if bytes.IndexByte(probe, 0) >= 0 {
		return errors.New("binary file: contains NUL byte")
	}
	if !utf8.Valid(data) {
		return errors.New("binary file: not valid UTF-8")
	}
	return nil
}

// splitLines 按换行切分内容，丢弃末尾换行造成的空项，并去掉 CRLF 的 \r。
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	return lines
}

// renderLine 渲染一行：行号宽 6 右对齐后跟一个制表符。
func renderLine(n int, line string, showNumbers bool) string {
	if !showNumbers {
		return line + "\n"
	}
	return fmt.Sprintf("%6d\t%s\n", n, line)
}

// estimateTokens 估算文本 token 数：ASCII 约 4 字符/token，非 ASCII 约 1 字符/token。
func estimateTokens(s string) int {
	ascii, nonASCII := 0, 0
	for _, r := range s {
		if r < utf8.RuneSelf {
			ascii++
		} else {
			nonASCII++
		}
	}
	return (ascii+3)/4 + nonASCII
}

// clipToTokens 把 s 截断到不超过 budget 个估算 token；截断点保证在 rune 边界。
func clipToTokens(s string, budget int) string {
	if budget <= 0 {
		return ""
	}
	ascii, nonASCII := 0, 0
	for i, r := range s {
		if r < utf8.RuneSelf {
			ascii++
		} else {
			nonASCII++
		}
		if (ascii+3)/4+nonASCII > budget {
			return s[:i]
		}
	}
	return s
}
