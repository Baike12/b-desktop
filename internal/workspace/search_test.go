package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Baike12/b-desktop/internal/llm"
	"github.com/Baike12/b-desktop/internal/session"
)

// newSearchStore 在 dataDir 下创建会话存储并写入会话，返回后关闭存储。
func newSearchStore(t *testing.T, dataDir string, meta session.Meta, records []session.Record) {
	t.Helper()
	store, err := session.NewFileStore(dataDir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()
	if err := store.CreateSession(context.Background(), meta); err != nil {
		t.Fatalf("CreateSession(%s): %v", meta.ID, err)
	}
	for i, rec := range records {
		rec.SessionID = meta.ID
		if err := store.Append(context.Background(), rec); err != nil {
			t.Fatalf("Append(%s, #%d): %v", meta.ID, i, err)
		}
	}
}

// textMessage 构造一条纯文本消息记录。
func textMessage(role llm.Role, at time.Time, text string) session.Record {
	return session.Record{
		Type: session.RecordMessage,
		Time: at,
		Message: &llm.Message{
			Role:   role,
			Time:   at,
			Blocks: []llm.ContentBlock{{Type: llm.BlockText, Text: text}},
		},
	}
}

// toolUseMessage 构造一条带工具调用的 assistant 消息记录。
func toolUseMessage(at time.Time, name string) session.Record {
	return session.Record{
		Type: session.RecordMessage,
		Time: at,
		Message: &llm.Message{
			Role: llm.RoleAssistant,
			Time: at,
			Blocks: []llm.ContentBlock{
				{Type: llm.BlockText, Text: "running"},
				{Type: llm.BlockToolUse, ToolUseID: "tu_1", ToolName: name, Input: []byte(`{}`)},
			},
		},
	}
}

func TestSearchSessionsMatchesTextAndToolName(t *testing.T) {
	dataDir := t.TempDir()
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	newSearchStore(t, dataDir,
		session.Meta{ID: "s1", Title: "登录修复", WorkDir: "/repo/a", CreatedAt: base},
		[]session.Record{
			textMessage(llm.RoleUser, base.Add(time.Minute), "Please fix the Login bug"),
			toolUseMessage(base.Add(2*time.Minute), "Bash"),
			textMessage(llm.RoleAssistant, base.Add(3*time.Minute), "Login fixed now"),
		})
	newSearchStore(t, dataDir,
		session.Meta{ID: "s2", Title: "无关会话", WorkDir: "/repo/b", CreatedAt: base},
		[]session.Record{
			textMessage(llm.RoleUser, base.Add(time.Minute), "write documentation"),
		})

	ctx := context.Background()

	textResults, err := SearchSessions(ctx, dataDir, "login", 0)
	if err != nil {
		t.Fatalf("SearchSessions(login): %v", err)
	}
	if len(textResults) != 1 {
		t.Fatalf("login results = %d, want 1: %+v", len(textResults), textResults)
	}
	got := textResults[0]
	if got.SessionID != "s1" || got.Title != "登录修复" || got.WorkDir != "/repo/a" {
		t.Fatalf("unexpected session metadata: %+v", got)
	}
	if got.MatchCount != 2 {
		t.Fatalf("MatchCount = %d, want 2", got.MatchCount)
	}
	if len(got.Matches) != 2 {
		t.Fatalf("len(Matches) = %d, want 2", len(got.Matches))
	}
	if got.Matches[0].Role != "user" || !strings.Contains(got.Matches[0].Snippet, "Login") {
		t.Fatalf("first hit = %+v, want user hit containing Login", got.Matches[0])
	}
	if got.Matches[1].Role != "assistant" || got.Matches[1].Line != 4 {
		t.Fatalf("second hit = %+v, want assistant hit on line 4", got.Matches[1])
	}

	toolResults, err := SearchSessions(ctx, dataDir, "bash", 0)
	if err != nil {
		t.Fatalf("SearchSessions(bash): %v", err)
	}
	if len(toolResults) != 1 || len(toolResults[0].Matches) != 1 {
		t.Fatalf("bash results = %+v, want one tool hit", toolResults)
	}
	toolHit := toolResults[0].Matches[0]
	if toolHit.Role != "tool" || toolHit.ToolName != "Bash" {
		t.Fatalf("tool hit = %+v, want tool/Bash", toolHit)
	}

	empty, err := SearchSessions(ctx, dataDir, "no-such-query", 0)
	if err != nil {
		t.Fatalf("SearchSessions(miss): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("miss results = %+v, want empty", empty)
	}
}

func TestSearchSessionsSortsAndLimits(t *testing.T) {
	dataDir := t.TempDir()
	base := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	newSearchStore(t, dataDir,
		session.Meta{ID: "older", Title: "older", WorkDir: "/repo", CreatedAt: base},
		[]session.Record{textMessage(llm.RoleUser, base.Add(time.Minute), "shared keyword here")})
	newSearchStore(t, dataDir,
		session.Meta{ID: "newer", Title: "newer", WorkDir: "/repo", CreatedAt: base},
		[]session.Record{textMessage(llm.RoleUser, base.Add(time.Hour), "shared keyword again")})

	all, err := SearchSessions(context.Background(), dataDir, "shared", 0)
	if err != nil {
		t.Fatalf("SearchSessions: %v", err)
	}
	if len(all) != 2 || all[0].SessionID != "newer" || all[1].SessionID != "older" {
		t.Fatalf("order = %+v, want newer then older", all)
	}

	limited, err := SearchSessions(context.Background(), dataDir, "shared", 1)
	if err != nil {
		t.Fatalf("SearchSessions(limit=1): %v", err)
	}
	if len(limited) != 1 || limited[0].SessionID != "newer" {
		t.Fatalf("limit=1 results = %+v, want only newer", limited)
	}
}

func TestSearchSessionsSkipsCorruptLinesAndBadHeader(t *testing.T) {
	dataDir := t.TempDir()
	root := filepath.Join(dataDir, "sessions")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	at := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	header, err := session.Encode(session.Record{
		ID: "r1", Type: session.RecordSessionMeta, SessionID: "corrupt",
		Time: at, Meta: map[string]string{"title": "corrupt", "workDir": "/repo"},
	})
	if err != nil {
		t.Fatalf("Encode header: %v", err)
	}
	msg, err := session.Encode(session.Record{
		ID: "r2", Type: session.RecordMessage, SessionID: "corrupt", Time: at,
		Message: &llm.Message{Role: llm.RoleUser, Blocks: []llm.ContentBlock{{Type: llm.BlockText, Text: "find the needle"}}},
	})
	if err != nil {
		t.Fatalf("Encode message: %v", err)
	}
	content := string(header) + "\n{broken json\n" + string(msg) + "\n"
	if err := os.WriteFile(filepath.Join(root, "corrupt.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "badheader.jsonl"), []byte("{}\n"+string(msg)+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile bad header: %v", err)
	}

	results, err := SearchSessions(context.Background(), dataDir, "needle", 0)
	if err != nil {
		t.Fatalf("SearchSessions: %v", err)
	}
	if len(results) != 1 || results[0].SessionID != "corrupt" {
		t.Fatalf("results = %+v, want only corrupt session", results)
	}
	if len(results[0].Matches) != 1 || results[0].Matches[0].Line != 3 {
		t.Fatalf("matches = %+v, want one hit on line 3", results[0].Matches)
	}
}

func TestSearchSessionsErrorsAndMissingDir(t *testing.T) {
	if _, err := SearchSessions(context.Background(), t.TempDir(), "   ", 0); !errors.Is(err, ErrEmptyQuery) {
		t.Fatalf("empty query error = %v, want ErrEmptyQuery", err)
	}
	if _, err := SearchSessions(context.Background(), "", "x", 0); !errors.Is(err, ErrEmptyDataDir) {
		t.Fatalf("empty data dir error = %v, want ErrEmptyDataDir", err)
	}

	missing := filepath.Join(t.TempDir(), "does-not-exist")
	results, err := SearchSessions(context.Background(), missing, "x", 0)
	if err != nil {
		t.Fatalf("missing dir error = %v, want nil", err)
	}
	if len(results) != 0 {
		t.Fatalf("missing dir results = %+v, want empty", results)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := SearchSessions(ctx, t.TempDir(), "x", 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ctx error = %v, want context.Canceled", err)
	}
}

func TestBuildSnippetWindowsLongText(t *testing.T) {
	long := strings.Repeat("a", 500) + " needle " + strings.Repeat("b", 500)
	snippet := buildSnippet(long, "needle")
	if !strings.HasPrefix(snippet, "…") || !strings.HasSuffix(snippet, "…") {
		t.Fatalf("snippet = %q, want both ellipses", snippet)
	}
	if !strings.Contains(snippet, "needle") {
		t.Fatalf("snippet = %q, want needle", snippet)
	}
	if len([]rune(snippet)) > snippetWindow*2+len("needle")+2 {
		t.Fatalf("snippet too long: %d runes", len([]rune(snippet)))
	}

	collapsed := buildSnippet("line one\n\tline two   needle", "needle")
	if strings.ContainsAny(collapsed, "\n\t") {
		t.Fatalf("snippet = %q, want whitespace collapsed", collapsed)
	}
}
