package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 文件写操作：新建、重命名、移动、删除与内容写入。
//
// 所有操作都以工作目录为根：词法越界、符号链接指向根外、目标已存在都会被拒绝，
// 错误一律带哨兵，前端可据此区分「路径非法」与「磁盘错误」。

const (
	// MaxWriteBytes 是单次文件写入的字节上限，防止前端误传超大内容。
	MaxWriteBytes = 8 << 20
	// newFileMode 是新建文件的权限位：rw-r--r--。
	newFileMode = 0o644
	// newDirMode 是新建目录的权限位：rwxr-xr-x。
	newDirMode = 0o755
)

// 文件写操作的错误哨兵。
var (
	// ErrExists 表示目标路径已存在。
	ErrExists = errors.New("workspace: path already exists")
	// ErrNotEmpty 表示目录非空，不能直接删除。
	ErrNotEmpty = errors.New("workspace: directory is not empty")
	// ErrSamePath 表示源路径与目标路径相同。
	ErrSamePath = errors.New("workspace: source and target are the same")
	// ErrWriteTooLarge 表示写入内容超过 MaxWriteBytes。
	ErrWriteTooLarge = errors.New("workspace: content too large")
)

// CreateEntry 在 rel 处新建文件或目录。isDir 为 true 时创建目录，否则创建空文件。
// 目标已存在时返回 ErrExists，不会覆盖既有内容。
func CreateEntry(ctx context.Context, root, rel string, isDir bool) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("workspace: create entry: %w", err)
	}
	rootReal, err := resolveRoot(root)
	if err != nil {
		return err
	}
	target, err := resolveNewPath(rootReal, rel)
	if err != nil {
		return err
	}
	if _, statErr := os.Lstat(target); statErr == nil {
		return fmt.Errorf("%w: %s", ErrExists, rel)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("workspace: stat %s: %w", rel, statErr)
	}
	if isDir {
		if err := os.MkdirAll(target, newDirMode); err != nil {
			return fmt.Errorf("workspace: create directory %s: %w", rel, err)
		}
		return nil
	}
	// resolveNewPath 已确认父目录存在且在根目录内，这里不需要再建父目录。
	f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, newFileMode)
	if err != nil {
		return fmt.Errorf("workspace: create file %s: %w", rel, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("workspace: close %s: %w", rel, err)
	}
	return nil
}

// RenameEntry 把 from 改名为 to。to 是相对工作目录的新路径，必须仍位于根目录内，
// 且不能指向已存在的路径。目录改名会连同内容一起移动。
func RenameEntry(ctx context.Context, root, from, to string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("workspace: rename entry: %w", err)
	}
	rootReal, err := resolveRoot(root)
	if err != nil {
		return err
	}
	// 改名作用于词法路径本身：符号链接被改的是链接，不是它指向的目标。
	// 但目标解析后必须仍在根目录内，否则等于把根外的东西拖进来。
	source, err := resolveWithin(rootReal, from)
	if err != nil {
		return err
	}
	if _, err := resolveExistingPath(rootReal, from); err != nil {
		return err
	}
	target, err := resolveNewPath(rootReal, to)
	if err != nil {
		return err
	}
	if source == target {
		return fmt.Errorf("%w: %s", ErrSamePath, from)
	}
	if _, statErr := os.Lstat(target); statErr == nil {
		return fmt.Errorf("%w: %s", ErrExists, to)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("workspace: stat %s: %w", to, statErr)
	}
	if err := os.Rename(source, target); err != nil {
		return fmt.Errorf("workspace: rename %s to %s: %w", from, to, err)
	}
	return nil
}

// MoveEntry 把 from 移动到 toDir 目录下，返回移动后的相对路径。
// toDir 为空表示移动到工作目录根。移动到自身或其子目录会被拒绝。
func MoveEntry(ctx context.Context, root, from, toDir string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("workspace: move entry: %w", err)
	}
	rootReal, err := resolveRoot(root)
	if err != nil {
		return "", err
	}
	// 与改名同理：移动的是词法路径本身，符号链接被移动的是链接。
	source, err := resolveWithin(rootReal, from)
	if err != nil {
		return "", err
	}
	if _, err := resolveExistingPath(rootReal, from); err != nil {
		return "", err
	}
	dir, err := resolveExistingPath(rootReal, toDir)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("workspace: stat %s: %w", toDir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: %s", ErrNotDirectory, toDir)
	}
	if source == dir {
		return "", fmt.Errorf("%w: %s", ErrSamePath, from)
	}
	// 目录不能移动到自己的子目录里，否则目标路径会落在源目录内部。
	if isWithin(dir, source) {
		return "", fmt.Errorf("%w: %s into %s", ErrInvalidPath, from, toDir)
	}
	name := filepath.Base(source)
	target := filepath.Join(dir, name)
	if _, statErr := os.Lstat(target); statErr == nil {
		return "", fmt.Errorf("%w: %s", ErrExists, target)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", fmt.Errorf("workspace: stat %s: %w", target, statErr)
	}
	if err := os.Rename(source, target); err != nil {
		return "", fmt.Errorf("workspace: move %s to %s: %w", from, toDir, err)
	}
	rel, err := filepath.Rel(rootReal, target)
	if err != nil {
		return "", fmt.Errorf("workspace: resolve moved path: %w", err)
	}
	return filepath.ToSlash(rel), nil
}

// DeleteEntry 删除 rel。文件直接删除，目录连同内容一起删除。
// 符号链接只删除链接本身，不跟随到根目录之外的目标。
func DeleteEntry(ctx context.Context, root, rel string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("workspace: delete entry: %w", err)
	}
	rootReal, err := resolveRoot(root)
	if err != nil {
		return err
	}
	// 删除走词法路径：符号链接必须删链接本身，不能展开成根目录外的目标。
	target, err := resolveWithin(rootReal, rel)
	if err != nil {
		return err
	}
	// 但父目录必须解析后仍在根内：只做词法检查的话，
	// "link/secret.txt"（link 指向根外）会真的删掉根目录外的文件。
	parent, err := filepath.EvalSymlinks(filepath.Dir(target))
	if err != nil {
		return fmt.Errorf("workspace: resolve parent of %s: %w", rel, err)
	}
	if !withinRoot(rootReal, parent) {
		return fmt.Errorf("%w: %s", ErrOutsideRoot, rel)
	}
	info, err := os.Lstat(target)
	if err != nil {
		return fmt.Errorf("workspace: stat %s: %w", rel, err)
	}
	if info.IsDir() {
		if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("workspace: remove directory %s: %w", rel, err)
		}
		return nil
	}
	if err := os.Remove(target); err != nil {
		return fmt.Errorf("workspace: remove %s: %w", rel, err)
	}
	return nil
}

// WriteFile 覆盖写入 rel 的内容。rel 必须指向工作目录内已存在的普通文件，
// 内容超过 MaxWriteBytes 时返回 ErrWriteTooLarge。
func WriteFile(ctx context.Context, root, rel, content string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("workspace: write file: %w", err)
	}
	if len(content) > MaxWriteBytes {
		return fmt.Errorf("%w: %d bytes", ErrWriteTooLarge, len(content))
	}
	rootReal, err := resolveRoot(root)
	if err != nil {
		return err
	}
	target, err := resolveExistingPath(rootReal, rel)
	if err != nil {
		return err
	}
	info, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("workspace: stat %s: %w", rel, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s", ErrNotFile, rel)
	}
	// 先写临时文件再改名，避免中途失败留下半个文件。
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".bdesktop-*")
	if err != nil {
		return fmt.Errorf("workspace: create temp file in %s: %w", rel, err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	if _, err := tmp.WriteString(content); err != nil {
		cleanup()
		return fmt.Errorf("workspace: write %s: %w", rel, err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("workspace: sync %s: %w", rel, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("workspace: close %s: %w", rel, err)
	}
	// 保留原文件的权限位，避免保存后把可执行脚本变成 0600。
	if err := os.Chmod(tmpPath, info.Mode().Perm()); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("workspace: chmod %s: %w", rel, err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("workspace: replace %s: %w", rel, err)
	}
	return nil
}

// resolveExistingPath 解析工作目录下已存在条目的真实路径。
// 符号链接会被展开，展开后的目标必须仍在根目录内。
func resolveExistingPath(rootReal, rel string) (string, error) {
	full, err := resolveWithin(rootReal, rel)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", fmt.Errorf("workspace: resolve %s: %w", rel, err)
	}
	if !withinRoot(rootReal, real) {
		return "", fmt.Errorf("%w: %s", ErrOutsideRoot, rel)
	}
	return real, nil
}

// resolveNewPath 解析工作目录下可能尚不存在的路径：词法越界会被拒绝，
// 父目录必须已存在且位于根目录内（避免借符号链接把文件写到根外）。
func resolveNewPath(rootReal, rel string) (string, error) {
	trimmed := strings.TrimSpace(rel)
	if trimmed == "" || trimmed == "." {
		return "", fmt.Errorf("%w: empty path", ErrInvalidPath)
	}
	full, err := resolveWithin(rootReal, rel)
	if err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(full))
	if err != nil {
		return "", fmt.Errorf("workspace: resolve parent of %s: %w", rel, err)
	}
	if !withinRoot(rootReal, parent) {
		return "", fmt.Errorf("%w: %s", ErrOutsideRoot, rel)
	}
	return full, nil
}
