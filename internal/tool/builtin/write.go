package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"anc-dsk/internal/tool"
)

const writeSchema = `{
  "type": "object",
  "properties": {
    "file_path": {
      "type": "string",
      "description": "Path of the file to write. Relative paths are resolved against the workspace root; paths escaping the root are rejected."
    },
    "content": {
      "type": "string",
      "description": "Full file content. The file is replaced, not appended to."
    }
  },
  "required": ["file_path", "content"],
  "additionalProperties": false
}`

type writeInput struct {
	FilePath string  `json:"file_path"`
	Content  *string `json:"content"`
}

// writeTool 是 Write 的实现。
type writeTool struct {
	fileBase
}

// NewWrite 创建写入文件的 Write 工具。root 为工作区根目录，路径不得逃逸出该目录。
func NewWrite(root string) tool.Tool {
	return &writeTool{fileBase: newFileBase(root)}
}

// Name 返回工具名。
func (t *writeTool) Name() string { return "Write" }

// Description 返回给模型看的用途说明。
func (t *writeTool) Description() string {
	return "Writes a text file inside the workspace, creating parent directories as needed. " +
		"The write is atomic: content goes to a temporary file in the same directory and is then renamed, " +
		"so readers never observe a partially written file. The whole file is replaced; use Edit for targeted changes. " +
		"Paths that escape the workspace root (including through symlinks) are rejected."
}

// InputSchema 返回 JSON Schema。
func (t *writeTool) InputSchema() json.RawMessage { return json.RawMessage(writeSchema) }

// ReadOnly 报告该次调用会修改文件系统。
func (t *writeTool) ReadOnly(json.RawMessage) bool { return false }

// ConcurrencySafe 报告该次调用不能与其他调用并行。
func (t *writeTool) ConcurrencySafe(json.RawMessage) bool { return false }

// AffectedPaths 返回本次调用会写入的绝对路径。
func (t *writeTool) AffectedPaths(input json.RawMessage) ([]string, error) {
	var in writeInput
	if err := decodeInput(input, &in); err != nil {
		return nil, err
	}
	path, err := t.resolve(in.FilePath)
	if err != nil {
		return nil, err
	}
	return []string{path}, nil
}

// Run 原子地写入整个文件。
func (t *writeTool) Run(ctx context.Context, input json.RawMessage, progress tool.ProgressFunc) (tool.Result, error) {
	var in writeInput
	if err := decodeInput(input, &in); err != nil {
		return errorf("Write: %v", err), nil
	}
	if in.Content == nil {
		return errorf("Write: content is required"), nil
	}
	if err := ctx.Err(); err != nil {
		return errorf("Write: canceled: %v", err), nil
	}

	path, err := t.resolve(in.FilePath)
	if err != nil {
		return errorf("Write: %v", err), nil
	}
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return errorf("Write: %s is a directory", t.display(path)), nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errorf("Write: %s: %v", t.display(path), err), nil
	}

	content := []byte(*in.Content)
	if err := writeFileAtomic(path, content); err != nil {
		return errorf("Write: %s: %v", t.display(path), err), nil
	}
	if progress != nil {
		progress(tool.Progress{Bytes: int64(len(content)), Total: int64(len(content))})
	}
	return tool.Result{
		Content: fmt.Sprintf("wrote %d bytes to %s", len(content), t.display(path)),
		Meta: map[string]string{
			"file_path": path,
			"bytes":     strconv.Itoa(len(content)),
		},
	}, nil
}

// writeFileAtomic 先写同目录临时文件，再 rename 覆盖目标：
// 目标文件要么保持旧内容，要么变成完整新内容，不会出现半截文件。
// 目标已存在时沿用其权限位，新建文件使用 0644。
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	perm := fs.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat: %w", err)
	}

	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmp := f.Name()
	cleanup := func() {
		// 清理失败不应覆盖主错误。
		_ = f.Close()
		_ = os.Remove(tmp)
	}

	if _, err := f.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := f.Chmod(perm); err != nil {
		cleanup()
		return fmt.Errorf("set permissions: %w", err)
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace file: %w", err)
	}
	return nil
}
