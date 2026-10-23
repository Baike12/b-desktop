package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Baike12/b-desktop/internal/agent"
	"github.com/Baike12/b-desktop/internal/llm"
	"github.com/Baike12/b-desktop/internal/session"
)

// titleRoutingProvider 区分标题请求与正常对话请求，便于验证自动命名。
type titleRoutingProvider struct {
	mu         sync.Mutex
	titleCalls int
	mainCalls  int
	titleErr   error
	titleText  string
}

func (p *titleRoutingProvider) Name() string { return "title-routing" }

func (p *titleRoutingProvider) Stream(_ context.Context, req llm.Request) (llm.Stream, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if req.System == sessionTitleSystemPrompt {
		p.titleCalls++
		if p.titleErr != nil {
			return nil, p.titleErr
		}
		text := p.titleText
		if text == "" {
			text = "修复登录样式"
		}
		return &fakeStream{step: textStep("title-1", text)}, nil
	}
	p.mainCalls++
	return &fakeStream{step: textStep("main-1", "好的")}, nil
}

func (p *titleRoutingProvider) counts() (title, main int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.titleCalls, p.mainCalls
}

func TestSendMessageAutoTitlesSession(t *testing.T) {
	provider := &titleRoutingProvider{}
	app, collector := newTestApp(t, provider)
	info, err := app.CreateSession(CreateSessionInput{Title: "新会话"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if _, err := app.SendMessage(info.ID, "帮我修复登录页样式"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	dto := collector.waitForTitle(t, 3*time.Second)
	if dto.SessionID != info.ID || dto.Title != "修复登录样式" {
		t.Fatalf("title event = %+v", dto)
	}

	// 标题写入 transcript 后，列表与重放都必须返回新标题。
	infos, err := app.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(infos) != 1 || infos[0].Title != "修复登录样式" {
		t.Fatalf("session list = %+v", infos)
	}
	view, err := app.LoadSession(info.ID)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if view.Title != "修复登录样式" {
		t.Fatalf("session view title = %q", view.Title)
	}

	// 第二轮不再生成标题。
	collector.waitFor(t, agent.EventTurnEnd, 3*time.Second)
	if _, err := app.SendMessage(info.ID, "继续"); err != nil {
		t.Fatalf("SendMessage second: %v", err)
	}
	collector.waitFor(t, agent.EventTurnEnd, 3*time.Second)
	if title, main := provider.counts(); title != 1 || main != 2 {
		t.Fatalf("calls = title %d main %d, want title 1 main 2", title, main)
	}
}

func TestSessionTitleFallsBackWhenModelFails(t *testing.T) {
	provider := &titleRoutingProvider{titleErr: errors.New("title request failed")}
	app, collector := newTestApp(t, provider)
	info, err := app.CreateSession(CreateSessionInput{Title: "新会话"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if _, err := app.SendMessage(info.ID, "修复登录页样式\n补充说明"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	dto := collector.waitForTitle(t, 3*time.Second)
	if dto.Title != "修复登录页样式" {
		t.Fatalf("fallback title = %q", dto.Title)
	}
}

func TestListSessionsBackfillsPlaceholderTitle(t *testing.T) {
	app, _ := newTestApp(t, newFakeProvider())
	store, _, err := app.sessionStore()
	if err != nil {
		t.Fatalf("sessionStore: %v", err)
	}
	info, err := app.CreateSession(CreateSessionInput{Title: "新会话"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.Append(context.Background(), session.Record{
		Type:      session.RecordMessage,
		SessionID: info.ID,
		Message: &llm.Message{
			Role:   llm.RoleUser,
			Blocks: []llm.ContentBlock{{Type: llm.BlockText, Text: "# 分析仓库结构\n细节"}},
		},
	}); err != nil {
		t.Fatalf("Append message: %v", err)
	}

	infos, err := app.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(infos) != 1 || infos[0].Title != "分析仓库结构" {
		t.Fatalf("session list = %+v", infos)
	}

	// 再次列出时标题已落盘，不再重复追加 title 记录。
	if _, err := app.ListSessions(); err != nil {
		t.Fatalf("ListSessions second: %v", err)
	}
	recs, err := store.Load(context.Background(), info.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	titles := 0
	for _, rec := range recs {
		if rec.Type == session.RecordTitle {
			titles++
		}
	}
	if titles != 1 {
		t.Fatalf("title records = %d, want 1", titles)
	}
}

func TestNormalizeSessionTitle(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		fallback string
		want     string
	}{
		{"plain", "修复登录样式", "回退", "修复登录样式"},
		{"quotes", "“修复登录样式”", "回退", "修复登录样式"},
		{"prefix", "标题：重构会话存储", "回退", "重构会话存储"},
		{"multiline", "会话标题\n解释内容", "回退", "会话标题"},
		{"trailing punctuation", "实现自动标题。", "回退", "实现自动标题"},
		{"empty falls back", "  \n ", "回退标题", "回退标题"},
		{"long truncated", strings.Repeat("长", 40), "回退", strings.Repeat("长", sessionTitleMaxRunes) + "…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeSessionTitle(tc.raw, tc.fallback); got != tc.want {
				t.Fatalf("normalizeSessionTitle(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestFallbackSessionTitle(t *testing.T) {
	cases := []struct {
		prompt string
		want   string
	}{
		{"# 修复登录\n细节", "修复登录"},
		{"  帮我看看这个 bug  ", "帮我看看这个 bug"},
		{"", ""},
		{strings.Repeat("a", 40), strings.Repeat("a", sessionTitleMaxRunes) + "…"},
	}
	for _, tc := range cases {
		if got := fallbackSessionTitle(tc.prompt); got != tc.want {
			t.Fatalf("fallbackSessionTitle(%q) = %q, want %q", tc.prompt, got, tc.want)
		}
	}
}
