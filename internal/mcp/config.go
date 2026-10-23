// Package mcp 实现 MCP（Model Context Protocol）客户端。
//
// 传输层只实现 stdio：子进程的 stdin/stdout 上跑换行分隔的 JSON-RPC 2.0
// （MCP 规范要求每条消息一行，消息内不得出现裸换行）。Manager 按 ~/.b-desktop/mcp.json
// 惰性启动服务器，把远端工具包装成 internal/tool.Tool，名字形如
// mcp__<server>__<tool>，输入 schema 原样透传。
package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ConfigFileName 是用户级 MCP 配置文件名，位于 ~/.b-desktop 下。
const ConfigFileName = "mcp.json"

// 配置错误哨兵，调用方用 errors.Is 判断。
var (
	// ErrConfigInvalid 表示 mcp.json 不是合法的服务器配置。
	ErrConfigInvalid = errors.New("mcp: invalid config")
	// ErrUnsupportedTransport 表示配置了 stdio 之外的传输类型。
	ErrUnsupportedTransport = errors.New("mcp: unsupported transport")
)

// ServerConfig 是一个 stdio MCP 服务器的配置。
type ServerConfig struct {
	// Type 是传输类型；空与 "stdio" 等价，其它值返回 ErrUnsupportedTransport。
	Type string `json:"type,omitempty"`
	// Command 是可执行文件路径或 PATH 中的命令名，不能为空。
	Command string `json:"command"`
	// Args 是传给命令的参数，不经过 shell 解释。
	Args []string `json:"args,omitempty"`
	// Env 是追加到子进程环境变量上的键值对，同名覆盖父进程。
	Env map[string]string `json:"env,omitempty"`
}

// Config 是 mcp.json 的顶层结构。
type Config struct {
	// MCPServers 以服务器名为键。
	MCPServers map[string]ServerConfig `json:"mcpServers"`
}

// LoadConfig 读取并校验 MCP 配置文件。
// path 为空或文件不存在时返回空配置，不视为错误；解析失败或字段非法返回
// ErrConfigInvalid / ErrUnsupportedTransport。服务器名首尾空白会被去除。
func LoadConfig(path string) (Config, error) {
	empty := Config{MCPServers: map[string]ServerConfig{}}
	if strings.TrimSpace(path) == "" {
		return empty, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return empty, nil
		}
		return Config{}, fmt.Errorf("mcp: read config %s: %w", path, err)
	}
	var raw struct {
		MCPServers map[string]ServerConfig `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Config{}, fmt.Errorf("%w: parse %s: %w", ErrConfigInvalid, path, err)
	}
	servers, err := normalizeServers(raw.MCPServers)
	if err != nil {
		return Config{}, err
	}
	return Config{MCPServers: servers}, nil
}

// normalizeServers 校验服务器配置并归一化服务器名（去除首尾空白）。
func normalizeServers(raw map[string]ServerConfig) (map[string]ServerConfig, error) {
	out := make(map[string]ServerConfig, len(raw))
	for name, server := range raw {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			return nil, fmt.Errorf("%w: empty server name", ErrConfigInvalid)
		}
		if _, dup := out[trimmed]; dup {
			return nil, fmt.Errorf("%w: duplicate server %q", ErrConfigInvalid, trimmed)
		}
		if err := server.validate(trimmed); err != nil {
			return nil, err
		}
		out[trimmed] = server
	}
	return out, nil
}

// SaveConfig 校验并原子写入 MCP 配置。
//
// 写入流程与其它配置文件一致：先写同目录临时文件（0600），fsync 后 rename
// 覆盖目标，避免中途崩溃留下半份 JSON。父目录不存在时按 0700 创建。
func SaveConfig(path string, cfg Config) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%w: empty config path", ErrConfigInvalid)
	}
	servers, err := normalizeServers(cfg.MCPServers)
	if err != nil {
		return err
	}
	payload := struct {
		MCPServers map[string]ServerConfig `json:"mcpServers"`
	}{MCPServers: servers}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("mcp: encode config: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mcp: create config dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".mcp-*.tmp")
	if err != nil {
		return fmt.Errorf("mcp: create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("mcp: chmod temp config: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("mcp: write temp config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("mcp: sync temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("mcp: close temp config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("mcp: replace config: %w", err)
	}
	return nil
}

// validate 校验单个服务器配置；name 用于错误信息。
func (s ServerConfig) validate(name string) error {
	if s.Type != "" && s.Type != "stdio" {
		return fmt.Errorf("%w: server %q uses type %q", ErrUnsupportedTransport, name, s.Type)
	}
	if strings.TrimSpace(s.Command) == "" {
		return fmt.Errorf("%w: server %q has empty command", ErrConfigInvalid, name)
	}
	for key := range s.Env {
		if strings.TrimSpace(key) == "" || strings.ContainsRune(key, '=') {
			return fmt.Errorf("%w: server %q has invalid env key %q", ErrConfigInvalid, name, key)
		}
	}
	return nil
}

// names 返回配置中的服务器名，按字典序排列，保证工具顺序可复现。
func (c Config) names() []string {
	out := make([]string, 0, len(c.MCPServers))
	for name := range c.MCPServers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// envKeys 返回环境变量名，按字典序排列；值不下发前端，避免泄露密钥。
func (s ServerConfig) envKeys() []string {
	if len(s.Env) == 0 {
		return nil
	}
	out := make([]string, 0, len(s.Env))
	for key := range s.Env {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
