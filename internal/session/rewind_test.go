package session

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Baike12/b-desktop/internal/llm"
)

// rewindFixture 构造一个含两条对话轮次的会话，返回首条用户消息 ID。
func rewindFixture(t *testing.T, store Store, id string) string {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateSession(ctx, Meta{ID: id, WorkDir: "/tmp/work"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	first := llm.Message{ID: "u1", Role: llm.RoleUser, Blocks: []llm.ContentBlock{{Type: llm.BlockText, Text: "第一条"}}}
	assistant := llm.Message{ID: "a1", Role: llm.RoleAssistant, Blocks: []llm.ContentBlock{{Type: llm.BlockText, Text: "回复"}}}
	second := llm.Message{ID: "u2", Role: llm.RoleUser, Blocks: []llm.ContentBlock{{Type: llm.BlockText, Text: "第二条"}}}
	for _, msg := range []llm.Message{first, assistant, second} {
		if err := store.Append(ctx, Record{Type: RecordMessage, SessionID: id, Message: &msg}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	return first.ID
}

// rewindUserTexts 读取记录并返回全部用户消息文本。
func rewindUserTexts(t *testing.T, store Store, id string) []string {
	t.Helper()
	recs, err := store.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var out []string
	for _, rec := range recs {
		if rec.Type == RecordMessage && rec.Message != nil && rec.Message.Role == llm.RoleUser {
			out = append(out, rec.Message.Blocks[0].Text)
		}
	}
	return out
}

// TestMemStoreRewindBefore 截断后只剩 meta 与第一条用户消息之前的记录。
func TestMemStoreRewindBefore(t *testing.T) {
	store := NewMemStore()
	id := "s_rewind_mem"
	first := rewindFixture(t, store, id)

	if err := store.RewindBefore(context.Background(), id, first); err != nil {
		t.Fatalf("RewindBefore: %v", err)
	}
	texts := rewindUserTexts(t, store, id)
	if len(texts) != 0 {
		t.Fatalf("user texts after rewind = %v, want none", texts)
	}
	recs, err := store.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(recs) != 1 || recs[0].Type != RecordSessionMeta {
		t.Fatalf("records after rewind = %+v, want meta only", recs)
	}
	// 截断后继续追加必须正常。
	msg := llm.Message{ID: "u3", Role: llm.RoleUser, Blocks: []llm.ContentBlock{{Type: llm.BlockText, Text: "重来"}}}
	if err := store.Append(context.Background(), Record{Type: RecordMessage, SessionID: id, Message: &msg}); err != nil {
		t.Fatalf("Append after rewind: %v", err)
	}
}

// TestFileStoreRewindBefore 文件实现同样满足截断语义，且 meta 首行保留。
func TestFileStoreRewindBefore(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	defer store.Close()
	id := "s_rewind_file"
	rewindFixture(t, store, id)

	// 截断到第二条用户消息之前：剩 meta + u1 + a1。
	recs, err := store.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := store.RewindBefore(context.Background(), id, "u2"); err != nil {
		t.Fatalf("RewindBefore: %v", err)
	}
	texts := rewindUserTexts(t, store, id)
	if len(texts) != 1 || texts[0] != "第一条" {
		t.Fatalf("user texts after rewind = %v, want [第一条]", texts)
	}
	again, err := store.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("Load after rewind: %v", err)
	}
	if len(again) != len(recs)-1 {
		t.Fatalf("record count = %d, want %d", len(again), len(recs)-1)
	}
	if again[0].Type != RecordSessionMeta {
		t.Fatalf("first record = %s, want session_meta", again[0].Type)
	}
	// 截断后 Append 走重新打开的句柄。
	msg := llm.Message{ID: "u3", Role: llm.RoleUser, Blocks: []llm.ContentBlock{{Type: llm.BlockText, Text: "重来"}}}
	if err := store.Append(context.Background(), Record{Type: RecordMessage, SessionID: id, Message: &msg}); err != nil {
		t.Fatalf("Append after rewind: %v", err)
	}
	if texts := rewindUserTexts(t, store, id); len(texts) != 2 {
		t.Fatalf("user texts after re-append = %v", texts)
	}
}

// TestRewindBeforeUnknownMessage 未知锚点返回 ErrMessageNotFound，记录保持不变。
func TestRewindBeforeUnknownMessage(t *testing.T) {
	store := NewMemStore()
	id := "s_rewind_missing"
	rewindFixture(t, store, id)

	err := store.RewindBefore(context.Background(), id, "nope")
	if !errors.Is(err, ErrMessageNotFound) {
		t.Fatalf("err = %v, want ErrMessageNotFound", err)
	}
	if texts := rewindUserTexts(t, store, id); len(texts) != 2 {
		t.Fatalf("user texts unchanged expected, got %v", texts)
	}
	// 不存在的会话返回 ErrSessionNotFound。
	if err := store.RewindBefore(context.Background(), "s_absent", "u1"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("err = %v, want ErrSessionNotFound", err)
	}
}

// TestFileStoreRewindTempCleanup 回溯的临时文件最终被清理。
func TestFileStoreRewindTempCleanup(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	defer store.Close()
	id := "s_rewind_tmp"
	first := rewindFixture(t, store, id)
	if err := store.RewindBefore(context.Background(), id, first); err != nil {
		t.Fatalf("RewindBefore: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "sessions", ".rewind-*.tmp"))
	if len(matches) != 0 {
		t.Fatalf("leftover temp files: %v", matches)
	}
}
