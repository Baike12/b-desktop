package app

import (
	"strings"
	"testing"
)

// TestListAgentsOnZeroValueApp 回归：agentRegistry 曾直接写未初始化的
// extData（nil map），导致真实环境里 ListAgents 调用 panic（前端只见
// Promise 永不返回、选择器显示「没有可选的 agent」）。这里用绕过 newApp
// 的零值实例模拟最坏的构造路径，要求 ListAgents 正常返回 benben 条目。
func TestListAgentsOnZeroValueApp(t *testing.T) {
	app := &App{}
	agents, err := app.ListAgents()
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) == 0 {
		t.Fatalf("agents is empty, want at least benben")
	}
	if agents[0].ID != "benben" || !agents[0].Available {
		t.Fatalf("first agent = %+v, want available benben", agents[0])
	}
	// 再次调用走缓存路径，结果稳定。
	again, err := app.ListAgents()
	if err != nil || again[0].ID != "benben" {
		t.Fatalf("second call = %+v, %v", again, err)
	}
	if !strings.Contains(agents[0].Name, "Benben") {
		t.Fatalf("benben name = %q", agents[0].Name)
	}
}
