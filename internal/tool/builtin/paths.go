// Package builtin 实现文件读写类内置工具。
//
// 所有文件类工具在构造时固定一个工作区根目录，运行时把模型给出的路径解析到该目录下，
// 并拒绝任何逃逸出根目录的路径（包括通过符号链接逃逸）。工具本身不做权限判定：
// 只读性、并发安全与受影响路径通过 tool.Tool / tool.PathAware 暴露，
// 由 agent 与 permission 层决定 allow / ask / deny。
package builtin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"anc-dsk/internal/tool"
)

// ErrPathRequired 表示输入缺少路径，或工作区根目录为空。
var ErrPathRequired = errors.New("builtin: path is required")

// ErrPathOutsideRoot 表示路径解析后落在工作区根目录之外。
var ErrPathOutsideRoot = errors.New("builtin: path escapes workspace root")

// fileBase 保存构造时解析好的工作区根目录。
// 根目录解析失败时保留 err，运行期把它转成结构化错误结果，而不是 panic。
type fileBase struct {
	root string
	err  error
}

// newFileBase 解析 root 的绝对路径与符号链接。
func newFileBase(root string) fileBase {
	real, err := resolveRoot(root)
	if err != nil {
		return fileBase{err: err}
	}
	return fileBase{root: real}
}

// resolveRoot 把 root 解析为绝对的真实路径，并要求它存在且是目录。
func resolveRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", ErrPathRequired
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("builtin: resolve root %q: %w", root, err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("builtin: resolve root %q: %w", root, err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("builtin: stat root %q: %w", root, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("builtin: root %q is not a directory", root)
	}
	return real, nil
}

// Resolve 把 p 解析成 root 内的绝对路径。
//
// 规则（docs/agent-architecture.md §9.1）：
//  1. root 先解析为真实路径；
//  2. 相对路径与 root 拼接后 Clean，绝对路径直接 Clean；
//  3. 找到目标路径最深的已存在祖先并解析符号链接，再把不存在的尾部拼回；
//  4. 用 filepath.Rel 检查结果没有逃出 root。
//
// 越界时返回包装 ErrPathOutsideRoot 的错误。
func Resolve(root, p string) (string, error) {
	real, err := resolveRoot(root)
	if err != nil {
		return "", err
	}
	return resolveIn(real, p)
}

// resolve 把 p 解析成工作区内的绝对路径；根目录解析失败时返回该错误。
func (b fileBase) resolve(p string) (string, error) {
	if b.err != nil {
		return "", b.err
	}
	return resolveIn(b.root, p)
}

// display 返回 p 相对工作区根目录的路径，用于结果文案。
func (b fileBase) display(p string) string {
	if b.err != nil {
		return p
	}
	rel, err := filepath.Rel(b.root, p)
	if err != nil || rel == "." {
		return p
	}
	return rel
}

// resolveIn 在已解析的根目录下解析 p，并拒绝越界。
func resolveIn(rootReal, p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", ErrPathRequired
	}
	target := p
	if !filepath.IsAbs(target) {
		target = filepath.Join(rootReal, target)
	}
	target = filepath.Clean(target)

	// 找到最深的已存在祖先：不存在的尾部不参与符号链接解析，
	// 但已存在部分必须解析，否则 root 内的软链接就能把读写带到 root 外。
	existing := target
	for {
		_, err := os.Lstat(existing)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("builtin: inspect %q: %w", target, err)
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", fmt.Errorf("%w: %q has no existing ancestor", ErrPathOutsideRoot, p)
		}
		existing = parent
	}

	resolvedAncestor, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", fmt.Errorf("builtin: resolve %q: %w", existing, err)
	}
	tail, err := filepath.Rel(existing, target)
	if err != nil {
		return "", fmt.Errorf("builtin: resolve %q: %w", target, err)
	}
	resolved := resolvedAncestor
	if tail != "." {
		resolved = filepath.Join(resolvedAncestor, tail)
	}
	if !withinRoot(rootReal, resolved) {
		return "", fmt.Errorf("%w: %q resolves to %q", ErrPathOutsideRoot, p, resolved)
	}
	return resolved, nil
}

// withinRoot 报告 p 是否等于 root 或位于其下。
//
// 判定不能用 strings.HasPrefix(rel, "..")：合法文件名 "..foo" 的 rel 是 "..foo"，
// 会被误杀（docs/agent-architecture.md §9.1）。
func withinRoot(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return !filepath.IsAbs(rel)
}

// decodeInput 把模型给出的 JSON 解到 dst，并在错误里带上字段问题。
func decodeInput(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		return errors.New("input is empty")
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("invalid input: %w", err)
	}
	return nil
}

// errorf 构造结构化错误结果。工具的可预期失败都走这里，不返回 error、不 panic。
func errorf(format string, args ...any) tool.Result {
	return tool.Result{Content: fmt.Sprintf(format, args...), IsError: true}
}
