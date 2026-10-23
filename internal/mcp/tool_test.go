package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestBuildToolName 覆盖工具名规范化与分隔符。
func TestBuildToolName(t *testing.T) {
	cases := []struct{ server, remote, want string }{
		{"alpha", "search", "mcp__alpha__search"},
		{"my.server v1", "do_it-now", "mcp__my_server_v1__do_it-now"},
		{"claude.ai demo", "tool.name", "mcp__claude_ai_demo__tool_name"},
	}
	for _, tc := range cases {
		if got := buildToolName(tc.server, tc.remote); got != tc.want {
			t.Fatalf("buildToolName(%q, %q) = %q, want %q", tc.server, tc.remote, got, tc.want)
		}
	}
}

// TestTruncateDescription 覆盖按字符截断与短描述原样保留。
func TestTruncateDescription(t *testing.T) {
	short := strings.Repeat("短", 10)
	if got := truncateDescription(short); got != short {
		t.Fatalf("short description changed: %q", got)
	}
	long := strings.Repeat("描", maxDescriptionRunes+5)
	got := truncateDescription(long)
	if !strings.HasSuffix(got, descriptionTruncatedSuffix) {
		t.Fatalf("long description lacks suffix")
	}
	trimmed := strings.TrimSuffix(got, descriptionTruncatedSuffix)
	if len([]rune(trimmed)) != maxDescriptionRunes {
		t.Fatalf("truncated rune count = %d, want %d", len([]rune(trimmed)), maxDescriptionRunes)
	}
}

// TestNewClientToolDefaults 覆盖缺失 schema 与只读标注解析。
func TestNewClientToolDefaults(t *testing.T) {
	manager := NewManager("", nil)
	wrapped := newClientTool(manager, "srv", ToolInfo{Name: "t", Description: "d"})
	assertJSONEqual(t, wrapped.InputSchema(), json.RawMessage(`{"type":"object"}`))
	if wrapped.ReadOnly(nil) || wrapped.ConcurrencySafe(nil) {
		t.Fatal("tool without annotations should be writable")
	}

	no := false
	yes := true
	annotated := newClientTool(manager, "srv", ToolInfo{
		Name:        "t2",
		Annotations: &ToolAnnotations{ReadOnlyHint: &yes, DestructiveHint: &no},
	})
	if !annotated.ReadOnly(nil) || !annotated.ConcurrencySafe(nil) {
		t.Fatal("readOnlyHint=true should mark tool read-only")
	}
}

// TestRenderCallResult 覆盖文本块拼接、非文本块保留与 structuredContent。
func TestRenderCallResult(t *testing.T) {
	res := CallToolResult{Content: []json.RawMessage{
		json.RawMessage(`{"type":"text","text":"line1"}`),
		json.RawMessage(`{"type":"text","text":"line2"}`),
	}}
	if got, want := renderCallResult(res), "line1\nline2"; got != want {
		t.Fatalf("renderCallResult = %q, want %q", got, want)
	}

	mixed := CallToolResult{Content: []json.RawMessage{
		json.RawMessage(`{"type":"image","data":"AAA","mimeType":"image/png"}`),
	}, StructuredContent: json.RawMessage(`{"n":2}`)}
	got := renderCallResult(mixed)
	if !strings.Contains(got, `"type":"image"`) || !strings.Contains(got, "structuredContent: {\"n\":2}") {
		t.Fatalf("renderCallResult(mixed) = %q", got)
	}

	if got := renderCallResult(CallToolResult{}); got != "" {
		t.Fatalf("renderCallResult(empty) = %q", got)
	}
	if raw := mixed.Content[0]; !strings.Contains(string(raw), "AAA") {
		t.Fatalf("image block mutated: %s", raw)
	}
}

// TestMergeEnv 覆盖环境变量合并与覆盖。
func TestMergeEnv(t *testing.T) {
	got := mergeEnv([]string{"A=1", "B=2", "INVALID"}, map[string]string{"B": "3", "C": "4"})
	want := []string{"A=1", "B=3", "C=4"}
	if len(got) != len(want) {
		t.Fatalf("mergeEnv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("mergeEnv = %v, want %v", got, want)
		}
	}
}

// TestTailBuffer 覆盖只保留末尾内容的缓冲。
func TestTailBuffer(t *testing.T) {
	buf := &tailBuffer{limit: 4}
	if _, err := buf.Write([]byte("abcdef")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := buf.String(); got != "cdef" {
		t.Fatalf("tail = %q, want cdef", got)
	}
}
