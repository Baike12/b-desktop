package mcp

import (
	"errors"
	"path/filepath"
	"testing"
)

// TestLoadConfigMissingFile 覆盖文件不存在与空路径。
func TestLoadConfigMissingFile(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.MCPServers) != 0 {
		t.Fatalf("MCPServers = %v, want empty", cfg.MCPServers)
	}
	if _, err := LoadConfig(""); err != nil {
		t.Fatalf("LoadConfig(\"\"): %v", err)
	}
}

// TestLoadConfigParsesServers 覆盖 command / args / env 与服务器名去空白。
func TestLoadConfigParsesServers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	body := `{"mcpServers": {
		" demo ": {"command": "npx", "args": ["-y", "server"], "env": {"TOKEN": "abc"}},
		"plain": {"type": "stdio", "command": "plain-bin"}
	}}`
	if err := writeFile(path, []byte(body)); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	demo, ok := cfg.MCPServers["demo"]
	if !ok {
		t.Fatalf("servers = %v, want trimmed name demo", cfg.MCPServers)
	}
	if demo.Command != "npx" || len(demo.Args) != 2 || demo.Env["TOKEN"] != "abc" {
		t.Fatalf("demo = %+v", demo)
	}
	if plain := cfg.MCPServers["plain"]; plain.Command != "plain-bin" || plain.Type != "stdio" {
		t.Fatalf("plain = %+v", plain)
	}
	names := cfg.names()
	if len(names) != 2 || names[0] != "demo" || names[1] != "plain" {
		t.Fatalf("names = %v", names)
	}
	if keys := demo.envKeys(); len(keys) != 1 || keys[0] != "TOKEN" {
		t.Fatalf("envKeys = %v", keys)
	}
}

// TestLoadConfigErrors 覆盖非法 JSON、空 command 与不支持的传输。
func TestLoadConfigErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
		want error
	}{
		{"bad json", `{"mcpServers": {`, ErrConfigInvalid},
		{"empty command", `{"mcpServers": {"x": {"command": " "}}}`, ErrConfigInvalid},
		{"empty name", `{"mcpServers": {" ": {"command": "x"}}}`, ErrConfigInvalid},
		{"bad env key", `{"mcpServers": {"x": {"command": "x", "env": {"A=B": "c"}}}}`, ErrConfigInvalid},
		{"unsupported type", `{"mcpServers": {"x": {"type": "http", "command": "x"}}}`, ErrUnsupportedTransport},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mcp.json")
			if err := writeFile(path, []byte(tc.body)); err != nil {
				t.Fatalf("write config: %v", err)
			}
			if _, err := LoadConfig(path); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestLoadConfigReadError 覆盖非 ENOENT 的读取错误。
func TestLoadConfigReadError(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadConfig(dir); err == nil {
		t.Fatal("LoadConfig(dir) error = nil")
	}
}

// TestServerConfigValidate 覆盖 env 键校验边界。
func TestServerConfigValidate(t *testing.T) {
	if err := (ServerConfig{Command: "x", Env: map[string]string{"OK_1": "v"}}).validate("s"); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if err := (ServerConfig{}).validate("s"); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want ErrConfigInvalid", err)
	}
}
