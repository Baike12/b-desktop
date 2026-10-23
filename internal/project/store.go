// Package project 记录用户打开过的项目文件夹。
//
// 项目列表是纯应用状态：只保存文件夹路径、显示名与加入时间。
// 会话本身按 workDir 归属到项目，不在这里冗余存储。
package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const fileMode os.FileMode = 0o600

// Project 是一个已打开的项目文件夹。
type Project struct {
	Path    string    `json:"path"`
	Name    string    `json:"name"`
	AddedAt time.Time `json:"addedAt"`
}

// Store 是 projects.json 的读写实现，并发安全。
type Store struct {
	mu   sync.Mutex
	path string
}

// NewStore 创建项目存储；path 是 JSON 文件路径，文件不存在视为空列表。
func NewStore(path string) *Store {
	return &Store{path: path}
}

// List 返回已打开的项目，最近打开的在前。
func (s *Store) List(ctx context.Context) ([]Project, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

// Add 登记一个项目文件夹并返回条目。
// 路径会解析为绝对真实路径；重复添加返回已有条目，不改变顺序。
func (s *Store) Add(ctx context.Context, dir string) (Project, error) {
	if err := ctx.Err(); err != nil {
		return Project{}, err
	}
	path, err := resolveDir(dir)
	if err != nil {
		return Project{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.loadLocked()
	if err != nil {
		return Project{}, err
	}
	for _, item := range items {
		if item.Path == path {
			return item, nil
		}
	}

	entry := Project{Path: path, Name: filepath.Base(path), AddedAt: time.Now()}
	items = append([]Project{entry}, items...)
	if err := s.writeLocked(items); err != nil {
		return Project{}, err
	}
	return entry, nil
}

// Remove 移除一个项目条目；不存在时是空操作。
func (s *Store) Remove(ctx context.Context, dir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := resolveDir(dir)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.loadLocked()
	if err != nil {
		return err
	}
	kept := make([]Project, 0, len(items))
	for _, item := range items {
		if item.Path != path {
			kept = append(kept, item)
		}
	}
	if len(kept) == len(items) {
		return nil
	}
	return s.writeLocked(kept)
}

// resolveDir 校验并规范化项目目录。
// 使用 EvalSymlinks 与 CreateSession 的工作目录解析保持一致，避免同一目录出现两条记录。
func resolveDir(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", errors.New("project: empty path")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("project: resolve %q: %w", dir, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("project: stat %q: %w", dir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("project: %q is not a directory", dir)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real, nil
	}
	return abs, nil
}

// loadLocked 读取列表；文件不存在返回空切片。调用方必须持有 s.mu。
func (s *Store) loadLocked() ([]Project, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Project{}, nil
		}
		return nil, fmt.Errorf("project: read %s: %w", s.path, err)
	}
	var items []Project
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("project: parse %s: %w", s.path, err)
	}
	out := make([]Project, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.Path) == "" {
			continue
		}
		if item.Name == "" {
			item.Name = filepath.Base(item.Path)
		}
		out = append(out, item)
	}
	return out, nil
}

// writeLocked 原子写入列表：同目录临时文件 + rename。调用方必须持有 s.mu。
func (s *Store) writeLocked(items []Project) error {
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return fmt.Errorf("project: marshal: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("project: create dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".projects-*.json")
	if err != nil {
		return fmt.Errorf("project: create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	if err := tmp.Chmod(fileMode); err != nil {
		cleanup()
		return fmt.Errorf("project: chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("project: write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("project: sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("project: close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("project: replace %s: %w", s.path, err)
	}
	return nil
}
