package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/Baike12/b-desktop/internal/tool"
)

const editSchema = `{
  "type": "object",
  "properties": {
    "file_path": {
      "type": "string",
      "description": "Path of the file to edit. Relative paths are resolved against the workspace root; paths escaping the root are rejected."
    },
    "old_string": {
      "type": "string",
      "description": "Exact text to replace. Must not be empty."
    },
    "new_string": {
      "type": "string",
      "description": "Replacement text. May be empty to delete old_string."
    },
    "replace_all": {
      "type": "boolean",
      "description": "Replace every occurrence of old_string. When false (default), old_string must match exactly once."
    }
  },
  "required": ["file_path", "old_string", "new_string"],
  "additionalProperties": false
}`

type editInput struct {
	FilePath   string  `json:"file_path"`
	OldString  *string `json:"old_string"`
	NewString  *string `json:"new_string"`
	ReplaceAll bool    `json:"replace_all,omitempty"`
}

// editTool 是 Edit 的实现。
type editTool struct {
	fileBase
}

// NewEdit 创建精确替换文件的 Edit 工具。root 为工作区根目录，路径不得逃逸出该目录。
func NewEdit(root string) tool.Tool {
	return &editTool{fileBase: newFileBase(root)}
}

// Name 返回工具名。
func (t *editTool) Name() string { return "Edit" }

// Description 返回给模型看的用途说明。
func (t *editTool) Description() string {
	return "Replaces an exact string in an existing text file inside the workspace. " +
		"old_string must match exactly once unless replace_all is true; otherwise the call fails without modifying the file. " +
		"new_string may be empty to delete text. The file is rewritten atomically. " +
		"Paths that escape the workspace root (including through symlinks) are rejected."
}

// InputSchema 返回 JSON Schema。
func (t *editTool) InputSchema() json.RawMessage { return json.RawMessage(editSchema) }

// ReadOnly 报告该次调用会修改文件系统。
func (t *editTool) ReadOnly(json.RawMessage) bool { return false }

// ConcurrencySafe 报告该次调用不能与其他调用并行。
func (t *editTool) ConcurrencySafe(json.RawMessage) bool { return false }

// AffectedPaths 返回本次调用会修改的绝对路径。
func (t *editTool) AffectedPaths(input json.RawMessage) ([]string, error) {
	var in editInput
	if err := decodeInput(input, &in); err != nil {
		return nil, err
	}
	path, err := t.resolve(in.FilePath)
	if err != nil {
		return nil, err
	}
	return []string{path}, nil
}

// Run 执行精确字符串替换。
func (t *editTool) Run(ctx context.Context, input json.RawMessage, progress tool.ProgressFunc) (tool.Result, error) {
	var in editInput
	if err := decodeInput(input, &in); err != nil {
		return errorf("Edit: %v", err), nil
	}
	if in.OldString == nil || *in.OldString == "" {
		return errorf("Edit: old_string is required and must not be empty"), nil
	}
	if in.NewString == nil {
		return errorf("Edit: new_string is required"), nil
	}
	if err := ctx.Err(); err != nil {
		return errorf("Edit: canceled: %v", err), nil
	}

	path, err := t.resolve(in.FilePath)
	if err != nil {
		return errorf("Edit: %v", err), nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errorf("Edit: file not found: %s", t.display(path)), nil
		}
		return errorf("Edit: %s: %v", t.display(path), err), nil
	}
	if info.IsDir() {
		return errorf("Edit: %s is a directory", t.display(path)), nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return errorf("Edit: %s: %v", t.display(path), err), nil
	}
	if err := checkTextContent(data); err != nil {
		return errorf("Edit: %s: %v", t.display(path), err), nil
	}

	oldStr := *in.OldString
	newStr := *in.NewString
	source := string(data)
	count := strings.Count(source, oldStr)
	if count == 0 {
		return errorf("Edit: old_string not found in %s", t.display(path)), nil
	}
	if count > 1 && !in.ReplaceAll {
		return errorf("Edit: old_string matches %d times in %s; add surrounding context to make it unique or set replace_all=true", count, t.display(path)), nil
	}

	var updated string
	if in.ReplaceAll {
		updated = strings.ReplaceAll(source, oldStr, newStr)
	} else {
		idx := strings.Index(source, oldStr)
		updated = source[:idx] + newStr + source[idx+len(oldStr):]
	}
	if updated == source {
		return tool.Result{
			Content: fmt.Sprintf("no changes: old_string equals new_string in %s", t.display(path)),
			Meta:    map[string]string{"file_path": path, "replaced": "0"},
		}, nil
	}
	if err := writeFileAtomic(path, []byte(updated)); err != nil {
		return errorf("Edit: %s: %v", t.display(path), err), nil
	}
	if progress != nil {
		progress(tool.Progress{Bytes: int64(len(updated)), Total: int64(len(updated))})
	}
	return tool.Result{
		Content: fmt.Sprintf("replaced %d occurrence(s) in %s", count, t.display(path)),
		Meta:    map[string]string{"file_path": path, "replaced": strconv.Itoa(count)},
	}, nil
}
