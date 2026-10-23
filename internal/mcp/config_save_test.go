package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	cfg := Config{MCPServers: map[string]ServerConfig{
		"fs": {Command: "npx", Args: []string{"-y", "server-fs"}, Env: map[string]string{"KEY": "v"}},
	}}
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("permissions = %o, want 600", mode)
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	server, ok := loaded.MCPServers["fs"]
	if !ok {
		t.Fatalf("server fs missing: %+v", loaded.MCPServers)
	}
	if server.Command != "npx" || len(server.Args) != 2 || server.Env["KEY"] != "v" {
		t.Fatalf("round trip mismatch: %+v", server)
	}
}

func TestSaveConfigRejectsInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	cases := []Config{
		{MCPServers: map[string]ServerConfig{"": {Command: "x"}}},
		{MCPServers: map[string]ServerConfig{"a": {Command: " "}}},
		{MCPServers: map[string]ServerConfig{"a": {Type: "sse", Command: "x"}}},
		{MCPServers: map[string]ServerConfig{"a": {Command: "x", Env: map[string]string{"A=B": "v"}}}},
	}
	for i, cfg := range cases {
		if err := SaveConfig(path, cfg); err == nil {
			t.Fatalf("case %d: expected error", i)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid save must not create file, stat err = %v", err)
	}
}

func TestSaveConfigOverwritesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	first := Config{MCPServers: map[string]ServerConfig{"a": {Command: "a"}}}
	if err := SaveConfig(path, first); err != nil {
		t.Fatalf("first save: %v", err)
	}
	second := Config{MCPServers: map[string]ServerConfig{"b": {Command: "b"}}}
	if err := SaveConfig(path, second); err != nil {
		t.Fatalf("second save: %v", err)
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.MCPServers) != 1 {
		t.Fatalf("servers = %+v, want only b", loaded.MCPServers)
	}
	if _, ok := loaded.MCPServers["a"]; ok {
		t.Fatalf("old server a leaked into config")
	}
}
