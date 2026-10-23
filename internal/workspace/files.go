package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// 文件浏览的固定上限与排除项。
const (
	// MaxDirEntries 是单次列目录返回的条目上限；超出时置 FileList.Truncated。
	MaxDirEntries = 1000
	// MaxPreviewBytes 是单文件预览读取上限，超出部分截断。
	MaxPreviewBytes = 512 << 10
	// binarySniffBytes 是判断二进制文件时检查 NUL 字节的前缀长度。
	binarySniffBytes = 8 << 10
)

// vcsDirNames 是始终从文件树隐藏的版本控制元数据目录。
var vcsDirNames = map[string]bool{
	".git": true,
	".svn": true,
	".hg":  true,
	".jj":  true,
	".sl":  true,
	".bzr": true,
}

// 文件浏览错误哨兵。
var (
	// ErrEmptyRoot 表示未提供工作目录。
	ErrEmptyRoot = errors.New("workspace: empty work directory")
	// ErrInvalidPath 表示相对路径为空、为绝对路径或含路径穿越。
	ErrInvalidPath = errors.New("workspace: invalid path")
	// ErrOutsideRoot 表示解析符号链接后目标落在工作目录之外。
	ErrOutsideRoot = errors.New("workspace: path outside work directory")
	// ErrNotDirectory 表示目标不是目录。
	ErrNotDirectory = errors.New("workspace: not a directory")
	// ErrNotFile 表示目标不是普通文件。
	ErrNotFile = errors.New("workspace: not a regular file")
)

// FileEntry 是文件树的一个节点，Path 是相对工作目录的斜杠路径。
type FileEntry struct {
	Name      string
	Path      string
	IsDir     bool
	IsSymlink bool
	Size      int64
}

// FileList 是一次目录读取的结果。
type FileList struct {
	Entries   []FileEntry
	Truncated bool
}

// FileContent 是一次只读文件预览的结果。
type FileContent struct {
	Path      string
	Size      int64
	Content   string
	Binary    bool
	Truncated bool
}

// ListDir 列出 root/rel 的直接子项：目录在前、名称不区分大小写排序，
// 隐藏版本控制元数据目录。rel 为空表示工作目录根。
func ListDir(ctx context.Context, root, rel string) (FileList, error) {
	if err := ctx.Err(); err != nil {
		return FileList{}, fmt.Errorf("workspace: list dir: %w", err)
	}
	rootReal, err := resolveRoot(root)
	if err != nil {
		return FileList{}, err
	}
	dir, err := resolveWithin(rootReal, rel)
	if err != nil {
		return FileList{}, err
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return FileList{}, fmt.Errorf("workspace: resolve %s: %w", dir, err)
	}
	if !withinRoot(rootReal, real) {
		return FileList{}, fmt.Errorf("%w: %s", ErrOutsideRoot, rel)
	}
	info, err := os.Stat(real)
	if err != nil {
		return FileList{}, fmt.Errorf("workspace: stat %s: %w", rel, err)
	}
	if !info.IsDir() {
		return FileList{}, fmt.Errorf("%w: %s", ErrNotDirectory, rel)
	}

	entries, err := os.ReadDir(real)
	if err != nil {
		return FileList{}, fmt.Errorf("workspace: read dir %s: %w", rel, err)
	}
	out := make([]FileEntry, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return FileList{}, fmt.Errorf("workspace: list dir: %w", err)
		}
		name := entry.Name()
		if vcsDirNames[name] || name == ".DS_Store" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			// 读取单个条目失败（权限、竞态删除）时跳过，不影响整个目录。
			continue
		}
		isSymlink := info.Mode()&os.ModeSymlink != 0
		out = append(out, FileEntry{
			Name:      name,
			Path:      joinRel(rel, name),
			IsDir:     entry.IsDir(),
			IsSymlink: isSymlink,
			Size:      info.Size(),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		li, lj := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if li != lj {
			return li < lj
		}
		return out[i].Name < out[j].Name
	})

	truncated := false
	if len(out) > MaxDirEntries {
		out = out[:MaxDirEntries]
		truncated = true
	}
	return FileList{Entries: out, Truncated: truncated}, nil
}

// ReadFile 读取 root/rel 的只读预览。文件超过 MaxPreviewBytes 时截断；
// 含 NUL 字节的文件按二进制处理，不返回内容。符号链接必须仍指向工作目录内。
func ReadFile(ctx context.Context, root, rel string) (FileContent, error) {
	if err := ctx.Err(); err != nil {
		return FileContent{}, fmt.Errorf("workspace: read file: %w", err)
	}
	if strings.TrimSpace(rel) == "" || rel == "." {
		return FileContent{}, fmt.Errorf("%w: empty path", ErrInvalidPath)
	}
	rootReal, err := resolveRoot(root)
	if err != nil {
		return FileContent{}, err
	}
	full, err := resolveWithin(rootReal, rel)
	if err != nil {
		return FileContent{}, err
	}
	real, err := filepath.EvalSymlinks(full)
	if err != nil {
		return FileContent{}, fmt.Errorf("workspace: resolve %s: %w", rel, err)
	}
	if !withinRoot(rootReal, real) {
		return FileContent{}, fmt.Errorf("%w: %s", ErrOutsideRoot, rel)
	}
	info, err := os.Stat(real)
	if err != nil {
		return FileContent{}, fmt.Errorf("workspace: stat %s: %w", rel, err)
	}
	if info.IsDir() {
		return FileContent{}, fmt.Errorf("%w: %s", ErrNotFile, rel)
	}
	if !info.Mode().IsRegular() {
		return FileContent{}, fmt.Errorf("%w: %s", ErrNotFile, rel)
	}

	f, err := os.Open(real)
	if err != nil {
		return FileContent{}, fmt.Errorf("workspace: open %s: %w", rel, err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, MaxPreviewBytes+1))
	if err != nil {
		return FileContent{}, fmt.Errorf("workspace: read %s: %w", rel, err)
	}
	truncated := len(data) > MaxPreviewBytes
	if truncated {
		data = data[:MaxPreviewBytes]
	}
	// 截断可能切断 UTF-8 字符；只丢弃不完整的尾部字节。
	data = trimPartialRune(data)

	out := FileContent{
		Path:      path.Clean(filepath.ToSlash(rel)),
		Size:      info.Size(),
		Truncated: truncated,
	}
	sniff := data
	if len(sniff) > binarySniffBytes {
		sniff = sniff[:binarySniffBytes]
	}
	if bytes.IndexByte(sniff, 0) >= 0 {
		out.Binary = true
		return out, nil
	}
	out.Content = strings.ToValidUTF8(string(data), "\uFFFD")
	return out, nil
}

// resolveRoot 返回工作目录的真实绝对路径；目录不存在或不是目录时返回错误。
func resolveRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", ErrEmptyRoot
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("workspace: resolve work directory: %w", err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("workspace: work directory does not exist: %s", abs)
		}
		return "", fmt.Errorf("workspace: resolve work directory: %w", err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("workspace: stat work directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: %s", ErrNotDirectory, abs)
	}
	return real, nil
}

// resolveWithin 把相对路径拼到工作目录下，并做词法越界检查。
// 符号链接的最终目标由调用方再用 EvalSymlinks 校验。
func resolveWithin(rootReal, rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" || rel == "." {
		return rootReal, nil
	}
	if len(rel) > 4096 || strings.ContainsRune(rel, 0) {
		return "", fmt.Errorf("%w: %q", ErrInvalidPath, rel)
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: %s", ErrInvalidPath, rel)
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s", ErrInvalidPath, rel)
	}
	full := filepath.Join(rootReal, clean)
	if !withinRoot(rootReal, full) {
		return "", fmt.Errorf("%w: %s", ErrOutsideRoot, rel)
	}
	return full, nil
}

// withinRoot 报告 target 是否等于 root 或位于 root 之下（两者都必须是绝对路径）。
func withinRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// joinRel 拼接相对路径，根目录用空字符串表示。
func joinRel(rel, name string) string {
	if rel == "" || rel == "." {
		return name
	}
	return path.Join(rel, name)
}

// trimPartialRune 丢弃被截断的 UTF-8 末尾不完整字符。
func trimPartialRune(data []byte) []byte {
	for i := 0; i < utf8.UTFMax-1 && len(data) > 0; i++ {
		r, size := utf8.DecodeLastRune(data)
		if r != utf8.RuneError || size > 1 {
			return data
		}
		data = data[:len(data)-1]
	}
	return data
}
