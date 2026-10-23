package subagent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestReloadAgentTypes 验证 frontmatter 解析、正文提取与非法文件跳过。
func TestReloadAgentTypes(t *testing.T) {
	dir := t.TempDir()
	writeAgentFile(t, dir, "review.md", "---\nname: review\ndescription: 代码评审\\n用于复核改动\nmodel: inherit\n---\n你是评审员。\n\n只报告确定的问题。\n")
	writeAgentFile(t, dir, "quoted.md", "---\nname: \"quoted\"\ndescription: '带引号的描述'\n---\n引号正文\n")
	writeAgentFile(t, dir, "block.md", "---\nname: block\ndescription: |\n  第一行\n  第二行\n---\n块标量正文\n")
	writeAgentFile(t, dir, "no-desc.md", "---\nname: no-desc\n---\n正文\n")
	writeAgentFile(t, dir, "no-frontmatter.md", "# 只是说明文档\n")
	writeAgentFile(t, dir, "readme.txt", "---\nname: ignored\ndescription: 非 md 文件\n---\n")
	writeAgentFile(t, dir, "zz-review-copy.md", "---\nname: review\ndescription: 重复定义\n---\n重复正文\n")

	mgr := newTestManager(t, dir, dir, newFakeProvider(), "")
	types := mgr.Types()
	if len(types) != 3 {
		t.Fatalf("types = %d (%+v), want 3", len(types), types)
	}

	byName := make(map[string]AgentType, len(types))
	for _, at := range types {
		byName[at.Name] = at
	}
	review, ok := byName["review"]
	if !ok {
		t.Fatalf("review type missing: %+v", types)
	}
	if review.Description != "代码评审\n用于复核改动" {
		t.Fatalf("description = %q", review.Description)
	}
	if review.SystemPrompt != "你是评审员。\n\n只报告确定的问题。" {
		t.Fatalf("system prompt = %q", review.SystemPrompt)
	}
	if review.Path != filepath.Join(dir, "review.md") {
		t.Fatalf("path = %q", review.Path)
	}
	if quoted, ok := byName["quoted"]; !ok || quoted.Description != "带引号的描述" {
		t.Fatalf("quoted type = %+v, ok=%v", quoted, ok)
	}
	if block, ok := byName["block"]; !ok || block.Description != "第一行\n第二行" {
		t.Fatalf("block type = %+v, ok=%v", block, ok)
	}

	// Reload 必须反映目录变化：删除全部文件后类型列表清空。
	for _, name := range []string{"review.md", "quoted.md", "block.md", "zz-review-copy.md", "no-desc.md", "no-frontmatter.md", "readme.txt"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatalf("Remove %s: %v", name, err)
		}
	}
	if err := mgr.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := len(mgr.Types()); got != 0 {
		t.Fatalf("types after reload = %d, want 0", got)
	}
}

// TestReloadMissingDir 验证目录不存在时视为空列表。
func TestReloadMissingDir(t *testing.T) {
	dir := t.TempDir()
	mgr := newTestManager(t, dir, filepath.Join(dir, "missing"), newFakeProvider(), "")
	if got := len(mgr.Types()); got != 0 {
		t.Fatalf("types = %d, want 0", got)
	}
}

// TestNewManagerRequiresRuntime 验证缺少 Runtime 时返回 ErrInvalidConfig。
func TestNewManagerRequiresRuntime(t *testing.T) {
	_, err := NewManager(Config{AgentsDir: t.TempDir()})
	if err == nil {
		t.Fatal("NewManager without Runtime succeeded")
	}
}

// TestToolsReturnsAgentTool 验证 Tools 返回 Agent 工具且名称正确。
func TestToolsReturnsAgentTool(t *testing.T) {
	dir := t.TempDir()
	mgr := newTestManager(t, dir, filepath.Join(dir, "agents"), newFakeProvider(), "")
	tools := mgr.Tools()
	if len(tools) != 1 || tools[0].Name() != ToolName {
		t.Fatalf("tools = %v", tools)
	}
}

// TestRuntimeReceivesSessionID 验证 Manager 把父会话 ID 传给 Runtime，
// 供调用方继承该会话的权限模式与工作目录。
func TestRuntimeReceivesSessionID(t *testing.T) {
	dir := t.TempDir()
	var got string
	mgr, err := NewManager(Config{
		WorkDir:   dir,
		AgentsDir: filepath.Join(dir, "agents"),
		Runtime: func(sessionID string) (Runtime, error) {
			got = sessionID
			return Runtime{Provider: newFakeProvider(textStep("m1", "ok")), WorkDir: dir}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer func() { _ = mgr.Close() }()

	if _, err := mgr.Run(context.Background(), agentRequest("parent-session"), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != "parent-session" {
		t.Fatalf("Runtime session ID = %q, want parent-session", got)
	}
}
