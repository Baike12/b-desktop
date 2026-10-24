package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 按文件名搜索工作目录下的文件。
//
// 搜索是递归遍历而不是建索引：桌面端的工作目录通常不大，一次遍历的代价可以接受，
// 换来的是零维护成本与永远一致的结果。遍历有明确的预算上限，避免在超大目录里卡住。

const (
	// DefaultFileSearchLimit 是未指定 limit 时返回的结果数上限。
	DefaultFileSearchLimit = 200
	// MaxFileSearchLimit 是单次搜索返回结果数硬上限。
	MaxFileSearchLimit = 1000
	// maxFileSearchDirs 是单次搜索最多访问的目录数。
	maxFileSearchDirs = 20000
	// maxFileSearchEntries 是单次搜索最多检查的文件数。
	maxFileSearchEntries = 200000
)

// skippedSearchDirs 是搜索时始终跳过的目录：版本控制元数据与依赖/产物目录。
// 不包含 build 这类常见源码目录名，避免漏掉用户真正在找的文件。
var skippedSearchDirs = map[string]bool{
	".git":         true,
	".hg":          true,
	".svn":         true,
	".jj":          true,
	"node_modules": true,
	"vendor":       true,
	"target":       true,
	"dist":         true,
	"__pycache__":  true,
	".venv":        true,
	"venv":         true,
}

// SearchFiles 按名称子串查找工作目录下的文件，返回相对工作目录的斜杠路径。
//
// query 为空时返回空结果。匹配不区分大小写，目录本身不作为结果返回，
// 只返回文件。遍历会跳过隐藏目录与常见依赖目录。
func SearchFiles(ctx context.Context, root, query string, limit int) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("workspace: search files: %w", err)
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return []string{}, nil
	}
	if limit <= 0 {
		limit = DefaultFileSearchLimit
	}
	if limit > MaxFileSearchLimit {
		limit = MaxFileSearchLimit
	}
	rootReal, err := resolveRoot(root)
	if err != nil {
		return nil, err
	}

	results := make([]string, 0, 16)
	dirs := 0
	entries := 0
	var walk func(dir string, rel string) error
	walk = func(dir string, rel string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		dirs++
		if dirs > maxFileSearchDirs || entries > maxFileSearchEntries || len(results) >= limit {
			return nil
		}
		list, err := os.ReadDir(dir)
		if err != nil {
			// 单个目录读不动（权限、竞态删除）时跳过，不中断整次搜索。
			return nil
		}
		for _, item := range list {
			if err := ctx.Err(); err != nil {
				return err
			}
			entries++
			name := item.Name()
			if item.IsDir() {
				if skippedSearchDirs[name] || strings.HasPrefix(name, ".") {
					continue
				}
				child := filepath.Join(dir, name)
				childRel := joinRel(rel, name)
				if err := walk(child, childRel); err != nil {
					return err
				}
				if len(results) >= limit {
					return nil
				}
				continue
			}
			if !strings.Contains(strings.ToLower(name), needle) {
				continue
			}
			results = append(results, joinRel(rel, name))
			if len(results) >= limit {
				return nil
			}
		}
		return nil
	}
	if err := walk(rootReal, ""); err != nil {
		return nil, fmt.Errorf("workspace: search files: %w", err)
	}
	return results, nil
}
