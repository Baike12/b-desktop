package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"anc-dsk/internal/llm"
)

var testTime = time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)

func testFileStore(t *testing.T) *FileStore {
	t.Helper()
	s, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func testMeta(id, title string, at time.Time) Meta {
	return Meta{ID: id, Title: title, WorkDir: "/repo", Model: "claude-sonnet-4-5", CreatedAt: at}
}

func testMessageRecord(sessionID, msgID string, role llm.Role, blocks []llm.ContentBlock, at time.Time) Record {
	return Record{
		Type:      RecordMessage,
		SessionID: sessionID,
		Time:      at,
		Message:   &llm.Message{ID: msgID, Role: role, Blocks: blocks, Time: at},
	}
}

func textBlocks(text string) []llm.ContentBlock {
	return []llm.ContentBlock{{Type: llm.BlockText, Text: text}}
}

func testUsageRecord(sessionID string, in, out int, at time.Time) Record {
	return Record{
		Type:      RecordUsage,
		SessionID: sessionID,
		Time:      at,
		Usage:     &llm.Usage{InputTokens: in, OutputTokens: out},
	}
}

func testPath(s *FileStore, id string) string { return filepath.Join(s.dir, id+".jsonl") }

func mustCreate(t *testing.T, s Store, meta Meta) {
	t.Helper()
	if err := s.CreateSession(context.Background(), meta); err != nil {
		t.Fatalf("CreateSession(%s): %v", meta.ID, err)
	}
}

func mustAppend(t *testing.T, s Store, rec Record) {
	t.Helper()
	if err := s.Append(context.Background(), rec); err != nil {
		t.Fatalf("Append(%s): %v", rec.Type, err)
	}
}

func TestValidateID(t *testing.T) {
	valid := []string{"s_01", "A1", "a-b_C9", strings.Repeat("a", 64)}
	for _, id := range valid {
		if err := ValidateID(id); err != nil {
			t.Errorf("ValidateID(%q) = %v, want nil", id, err)
		}
	}
	invalid := []string{"", "../x", "a/b", "-a", "_a", "a b", "a.b", `a\b`, strings.Repeat("a", 65)}
	for _, id := range invalid {
		err := ValidateID(id)
		if err == nil {
			t.Errorf("ValidateID(%q) = nil, want error", id)
			continue
		}
		if !errors.Is(err, ErrInvalidID) {
			t.Errorf("ValidateID(%q) = %v, want ErrInvalidID", id, err)
		}
	}
}

func TestEncodeDecode(t *testing.T) {
	rec := testMessageRecord("s_01", "msg_1", llm.RoleUser, textBlocks("hi"), testTime)
	line, err := Encode(rec)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if strings.Contains(string(line), "\n") {
		t.Fatalf("Encode returned embedded newline: %q", line)
	}
	got, err := Decode(line)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.Message == nil || got.Message.Blocks[0].Text != "hi" || !got.Time.Equal(testTime) {
		t.Fatalf("round trip mismatch: %+v", got)
	}

	if _, err := Encode(Record{Type: "bogus"}); err == nil {
		t.Fatal("Encode(unknown type) = nil, want error")
	}
	cases := map[string]string{
		"empty":           "",
		"unknown type":    `{"type":"bogus"}`,
		"missing payload": `{"type":"message","sessionId":"s_01"}`,
		"bad compact":     `{"type":"compact","sessionId":"s_01"}`,
		"bad json":        `{"type":`,
	}
	for name, line := range cases {
		if _, err := Decode([]byte(line)); err == nil {
			t.Errorf("Decode(%s) = nil, want error", name)
		}
	}
}

func TestCreateSessionWritesMetaFirstLine(t *testing.T) {
	s := testFileStore(t)
	mustCreate(t, s, testMeta("s_01", "重构登录", testTime))

	raw, err := os.ReadFile(testPath(s, "s_01"))
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if !strings.HasSuffix(string(raw), "\n") {
		t.Fatalf("transcript does not end with newline: %q", raw)
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	rec, err := Decode([]byte(lines[0]))
	if err != nil {
		t.Fatalf("Decode meta line: %v", err)
	}
	if rec.Type != RecordSessionMeta || rec.SessionID != "s_01" {
		t.Fatalf("meta record = %+v", rec)
	}
	if rec.ID == "" {
		t.Fatal("meta record has empty id")
	}
	if !rec.Time.Equal(testTime) {
		t.Fatalf("meta time = %v, want %v", rec.Time, testTime)
	}
	if rec.Meta["title"] != "重构登录" || rec.Meta["workDir"] != "/repo" || rec.Meta["model"] != "claude-sonnet-4-5" {
		t.Fatalf("meta fields = %+v", rec.Meta)
	}
	if err := s.CreateSession(context.Background(), testMeta("s_01", "dup", testTime)); !errors.Is(err, ErrSessionExists) {
		t.Fatalf("duplicate CreateSession = %v, want ErrSessionExists", err)
	}

	st, err := os.Stat(testPath(s, "s_01"))
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != sessionFileMode {
		t.Fatalf("file mode = %o, want %o", got, sessionFileMode)
	}
	dst, err := os.Stat(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := dst.Mode().Perm(); got != sessionDirMode {
		t.Fatalf("dir mode = %o, want %o", got, sessionDirMode)
	}
}

func TestAppendAndLoadRoundTrip(t *testing.T) {
	s := testFileStore(t)
	mustCreate(t, s, testMeta("s_01", "标题", testTime))
	recs := []Record{
		testMessageRecord("s_01", "msg_1", llm.RoleUser, textBlocks("看一下代码"), testTime.Add(time.Second)),
		testMessageRecord("s_01", "msg_2", llm.RoleAssistant, textBlocks("好"), testTime.Add(2*time.Second)),
		testUsageRecord("s_01", 12, 34, testTime.Add(2*time.Second)),
		{Type: RecordMode, SessionID: "s_01", Time: testTime.Add(3 * time.Second), Meta: map[string]string{"mode": "plan"}},
		{Type: RecordTurnEnd, SessionID: "s_01", Time: testTime.Add(4 * time.Second), TurnEnd: &TurnEnd{TurnID: "turn_1", StopReason: "completed", Turns: 1}},
	}
	for _, rec := range recs {
		mustAppend(t, s, rec)
	}

	loaded, err := s.Load(context.Background(), "s_01")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != len(recs)+1 {
		t.Fatalf("loaded %d records, want %d", len(loaded), len(recs)+1)
	}
	for i, rec := range recs {
		got := loaded[i+1]
		if got.Type != rec.Type {
			t.Fatalf("record %d type = %q, want %q", i+1, got.Type, rec.Type)
		}
		if got.ID == "" {
			t.Fatalf("record %d has empty id", i+1)
		}
	}
	if loaded[1].Message.Blocks[0].Text != "看一下代码" {
		t.Fatalf("message text = %q", loaded[1].Message.Blocks[0].Text)
	}
	if loaded[3].Usage.InputTokens != 12 || loaded[3].Usage.OutputTokens != 34 {
		t.Fatalf("usage = %+v", loaded[3].Usage)
	}
	if loaded[4].Meta["mode"] != "plan" {
		t.Fatalf("mode = %+v", loaded[4].Meta)
	}
	if loaded[5].TurnEnd.TurnID != "turn_1" {
		t.Fatalf("turn_end = %+v", loaded[5].TurnEnd)
	}

	// 显式 ID 必须保留，缺失时补全。
	explicit := Record{ID: "rec_fixed", Type: RecordMessage, SessionID: "s_01",
		Time: testTime, Message: &llm.Message{ID: "m", Role: llm.RoleUser, Blocks: textBlocks("x")}}
	mustAppend(t, s, explicit)
	loaded, err = s.Load(context.Background(), "s_01")
	if err != nil {
		t.Fatal(err)
	}
	if last := loaded[len(loaded)-1]; last.ID != "rec_fixed" {
		t.Fatalf("explicit id = %q", last.ID)
	}
}

func TestAppendRejectsInvalidRecords(t *testing.T) {
	s := testFileStore(t)
	mustCreate(t, s, testMeta("s_01", "t", testTime))
	cases := []struct {
		name string
		rec  Record
	}{
		{"no session", Record{Type: RecordMessage, Message: &llm.Message{}}},
		{"bad session", Record{Type: RecordMessage, SessionID: "../x", Message: &llm.Message{}}},
		{"unknown type", Record{Type: "bogus", SessionID: "s_01"}},
		{"nil message", Record{Type: RecordMessage, SessionID: "s_01"}},
		{"nil usage", Record{Type: RecordUsage, SessionID: "s_01"}},
		{"nil compact", Record{Type: RecordCompact, SessionID: "s_01"}},
		{"nil turn end", Record{Type: RecordTurnEnd, SessionID: "s_01"}},
		{"second meta", Record{Type: RecordSessionMeta, SessionID: "s_01"}},
	}
	for _, tc := range cases {
		if err := s.Append(context.Background(), tc.rec); err == nil {
			t.Errorf("%s: Append = nil, want error", tc.name)
		}
	}
	loaded, err := s.Load(context.Background(), "s_01")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 {
		t.Fatalf("invalid appends wrote %d records, want only meta", len(loaded))
	}
}

func TestAppendSyncPolicy(t *testing.T) {
	s := testFileStore(t)
	var mu sync.Mutex
	syncs := 0
	s.syncFile = func(*os.File) error {
		mu.Lock()
		syncs++
		mu.Unlock()
		return nil
	}
	mustCreate(t, s, testMeta("s_01", "t", testTime))
	syncsAt := func() int {
		mu.Lock()
		defer mu.Unlock()
		return syncs
	}
	if got := syncsAt(); got != 1 {
		t.Fatalf("syncs after CreateSession = %d, want 1", got)
	}
	mustAppend(t, s, Record{Type: RecordMode, SessionID: "s_01", Meta: map[string]string{"mode": "plan"}})
	if got := syncsAt(); got != 1 {
		t.Fatalf("mode append synced: %d, want 1", got)
	}
	for _, rec := range []Record{
		testMessageRecord("s_01", "m1", llm.RoleUser, textBlocks("a"), testTime),
		testUsageRecord("s_01", 1, 1, testTime),
		{Type: RecordCompact, SessionID: "s_01", Time: testTime, Compact: &Compact{Trigger: "auto", Summary: "s", Model: "m"}},
		{Type: RecordTurnEnd, SessionID: "s_01", Time: testTime, TurnEnd: &TurnEnd{TurnID: "t"}},
	} {
		mustAppend(t, s, rec)
	}
	if got := syncsAt(); got != 5 {
		t.Fatalf("syncs after key records = %d, want 5", got)
	}
}

func TestConcurrentAppend(t *testing.T) {
	s := testFileStore(t)
	const (
		sessions = 3
		writers  = 8
		perWrite = 25
	)
	var wg sync.WaitGroup
	errCh := make(chan error, sessions*writers)
	for si := 0; si < sessions; si++ {
		id := fmt.Sprintf("s_%02d", si)
		mustCreate(t, s, testMeta(id, "concurrent", testTime))
		for w := 0; w < writers; w++ {
			w := w
			wg.Add(1)
			go func() {
				defer wg.Done()
				for n := 0; n < perWrite; n++ {
					text := fmt.Sprintf("%s-%d-%d", id, w, n)
					rec := testMessageRecord(id, fmt.Sprintf("msg_%s_%d_%d", id, w, n), llm.RoleUser, textBlocks(text), testTime)
					rec.ID = ""
					if err := s.Append(context.Background(), rec); err != nil {
						errCh <- err
						return
					}
				}
			}()
		}
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent Append: %v", err)
	}

	want := writers * perWrite
	for si := 0; si < sessions; si++ {
		id := fmt.Sprintf("s_%02d", si)
		loaded, err := s.Load(context.Background(), id)
		if err != nil {
			t.Fatalf("Load(%s): %v", id, err)
		}
		if len(loaded) != want+1 {
			t.Fatalf("Load(%s) = %d records, want %d", id, len(loaded), want+1)
		}
		seen := make(map[string]struct{}, want)
		for _, rec := range loaded[1:] {
			if rec.Message == nil || len(rec.Message.Blocks) != 1 {
				t.Fatalf("Load(%s) returned malformed record: %+v", id, rec)
			}
			seen[rec.Message.Blocks[0].Text] = struct{}{}
		}
		if len(seen) != want {
			t.Fatalf("Load(%s) unique messages = %d, want %d", id, len(seen), want)
		}
	}
}

func TestLoadSkipsCorruptLines(t *testing.T) {
	s := testFileStore(t)
	mustCreate(t, s, testMeta("s_01", "t", testTime))
	mustAppend(t, s, testMessageRecord("s_01", "m1", llm.RoleUser, textBlocks("one"), testTime.Add(time.Second)))
	mustAppend(t, s, testMessageRecord("s_01", "m2", llm.RoleAssistant, textBlocks("two"), testTime.Add(2*time.Second)))

	damaged := "{\"broken\":\n" +
		"{\"type\":\"bogus\"}\n" +
		"\n" +
		"{\"type\":\"message\",\"sessionId\":\"s_01\"}\n"
	f, err := os.OpenFile(testPath(s, "s_01"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(damaged); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	loaded, err := s.Load(context.Background(), "s_01")
	if err == nil {
		t.Fatal("Load = nil error, want CorruptError")
	}
	var ce *CorruptError
	if !errors.As(err, &ce) {
		t.Fatalf("Load error = %v, want *CorruptError", err)
	}
	if !errors.Is(err, ErrCorruptLines) {
		t.Fatalf("errors.Is(ErrCorruptLines) = false for %v", err)
	}
	if len(loaded) != 3 {
		t.Fatalf("loaded %d valid records, want 3", len(loaded))
	}
	if ce.Skipped() != 4 {
		t.Fatalf("skipped %d lines, want 4", ce.Skipped())
	}
	wantLines := []int{4, 5, 6, 7}
	for i, l := range ce.Lines {
		if l.Number != wantLines[i] {
			t.Fatalf("corrupt line %d number = %d, want %d", i, l.Number, wantLines[i])
		}
	}
	if !strings.Contains(err.Error(), "damaged line") {
		t.Fatalf("error text not diagnosable: %v", err)
	}

	// 跳过的损坏行不影响重放。
	if _, rerr := Replay(loaded); rerr != nil {
		t.Fatalf("Replay after skipping corrupt lines: %v", rerr)
	}
}

func TestLoadTruncatedTail(t *testing.T) {
	s := testFileStore(t)
	mustCreate(t, s, testMeta("s_01", "t", testTime))
	mustAppend(t, s, testMessageRecord("s_01", "m1", llm.RoleUser, textBlocks("one"), testTime))

	f, err := os.OpenFile(testPath(s, "s_01"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"message","sessionId":"s_01"`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	loaded, err := s.Load(context.Background(), "s_01")
	if !errors.Is(err, ErrCorruptLines) {
		t.Fatalf("Load error = %v, want ErrCorruptLines", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("loaded %d records, want meta + 1 message", len(loaded))
	}
	var ce *CorruptError
	if !errors.As(err, &ce) || ce.Skipped() != 1 {
		t.Fatalf("corrupt error = %+v", err)
	}
	if !errors.Is(ce.Lines[0].Err, errTruncatedLine) {
		t.Fatalf("line error = %v, want errTruncatedLine", ce.Lines[0].Err)
	}
}

func TestLoadFirstLineInvalid(t *testing.T) {
	s := testFileStore(t)
	cases := map[string]string{
		"empty file": "",
		"garbage":    "{\"broken\"\n",
		"empty line": "\n{\"type\":\"message\"}\n",
		"wrong type": `{"type":"message","sessionId":"s_01","message":{"id":"m","role":"user"}}` + "\n",
	}
	for name, body := range cases {
		id := strings.ReplaceAll(name, " ", "_")
		if err := os.WriteFile(testPath(s, id), []byte(body), sessionFileMode); err != nil {
			t.Fatal(err)
		}
		recs, err := s.Load(context.Background(), id)
		if !errors.Is(err, ErrInvalidTranscript) {
			t.Errorf("%s: Load error = %v, want ErrInvalidTranscript", name, err)
		}
		if recs != nil {
			t.Errorf("%s: Load returned records despite fatal error", name)
		}
	}
}

func TestLoadTailWindow(t *testing.T) {
	s := testFileStore(t)
	s.maxBytes = 400
	mustCreate(t, s, testMeta("s_01", "t", testTime))
	for i := 0; i < 20; i++ {
		mustAppend(t, s, testMessageRecord("s_01", fmt.Sprintf("m%d", i), llm.RoleUser,
			textBlocks(strings.Repeat("x", 60)), testTime.Add(time.Duration(i)*time.Second)))
	}
	loaded, err := s.Load(context.Background(), "s_01")
	if err != nil {
		t.Fatalf("Load tail: %v", err)
	}
	if len(loaded) < 2 || len(loaded) >= 21 {
		t.Fatalf("tail window loaded %d records, want head + some tail", len(loaded))
	}
	if loaded[0].Type != RecordSessionMeta {
		t.Fatalf("first record = %q, want session_meta", loaded[0].Type)
	}
	if _, rerr := Replay(loaded); rerr != nil {
		t.Fatalf("Replay tail: %v", rerr)
	}

	// 窗口内找不到行边界时视为无法恢复。
	s.maxBytes = 1
	st, err := os.Stat(testPath(s, "s_01"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(testPath(s, "s_01"), st.Size()-1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(context.Background(), "s_01"); !errors.Is(err, ErrInvalidTranscript) {
		t.Fatalf("Load without boundary = %v, want ErrInvalidTranscript", err)
	}
}

func TestListMetadata(t *testing.T) {
	s := testFileStore(t)
	mustCreate(t, s, testMeta("s_new", "新会话", testTime.Add(10*time.Hour)))
	mustCreate(t, s, testMeta("s_old", "旧会话", testTime))
	mustAppend(t, s, testMessageRecord("s_old", "m1", llm.RoleUser, textBlocks("a"), testTime.Add(time.Minute)))
	mustAppend(t, s, testMessageRecord("s_old", "m2", llm.RoleAssistant, textBlocks("b"), testTime.Add(2*time.Minute)))

	// 非会话文件与被判定非法的文件名必须被忽略。
	if err := os.WriteFile(filepath.Join(s.dir, "notes.txt"), []byte("x"), sessionFileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, "bad name.jsonl"), []byte("{}\n"), sessionFileMode); err != nil {
		t.Fatal(err)
	}

	infos, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("List returned %d sessions, want 2: %+v", len(infos), infos)
	}
	if infos[0].ID != "s_new" || infos[1].ID != "s_old" {
		t.Fatalf("order = %s, %s; want s_new, s_old", infos[0].ID, infos[1].ID)
	}
	if infos[0].Title != "新会话" || infos[0].WorkDir != "/repo" {
		t.Fatalf("meta info = %+v", infos[0])
	}
	if infos[0].Messages != 0 {
		t.Fatalf("s_new messages = %d, want 0", infos[0].Messages)
	}
	if infos[1].Messages != 2 {
		t.Fatalf("s_old messages = %d, want 2", infos[1].Messages)
	}
	if !infos[1].UpdatedAt.Equal(testTime.Add(2 * time.Minute)) {
		t.Fatalf("s_old updated = %v", infos[1].UpdatedAt)
	}
}

func TestLoadErrors(t *testing.T) {
	s := testFileStore(t)
	if _, err := s.Load(context.Background(), "missing"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Load(missing) = %v, want ErrSessionNotFound", err)
	}
	if _, err := s.Load(context.Background(), "../etc/passwd"); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("Load(traversal) = %v, want ErrInvalidID", err)
	}
	if err := s.Append(context.Background(), testMessageRecord("missing", "m", llm.RoleUser, textBlocks("x"), testTime)); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Append(missing) = %v, want ErrSessionNotFound", err)
	}
}

func TestDeleteAndRecreate(t *testing.T) {
	s := testFileStore(t)
	mustCreate(t, s, testMeta("s_01", "t", testTime))
	mustAppend(t, s, testMessageRecord("s_01", "m1", llm.RoleUser, textBlocks("a"), testTime))

	if err := s.Delete(context.Background(), "s_01"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Load(context.Background(), "s_01"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Load after Delete = %v, want ErrSessionNotFound", err)
	}
	if err := s.Delete(context.Background(), "s_01"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("second Delete = %v, want ErrSessionNotFound", err)
	}

	// 删除后同名会话可以重建。
	mustCreate(t, s, testMeta("s_01", "again", testTime))
	mustAppend(t, s, testMessageRecord("s_01", "m2", llm.RoleUser, textBlocks("b"), testTime))
	loaded, err := s.Load(context.Background(), "s_01")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 {
		t.Fatalf("recreated session has %d records, want 2", len(loaded))
	}
}

func TestCloseIdempotent(t *testing.T) {
	s := testFileStore(t)
	mustCreate(t, s, testMeta("s_01", "t", testTime))
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := s.Append(context.Background(), testMessageRecord("s_01", "m", llm.RoleUser, textBlocks("x"), testTime)); !errors.Is(err, errStoreClosed) {
		t.Fatalf("Append after Close = %v, want errStoreClosed", err)
	}
	if _, err := s.Load(context.Background(), "s_01"); !errors.Is(err, errStoreClosed) {
		t.Fatalf("Load after Close = %v, want errStoreClosed", err)
	}
	if _, err := s.List(context.Background()); !errors.Is(err, errStoreClosed) {
		t.Fatalf("List after Close = %v, want errStoreClosed", err)
	}
	if err := s.Flush(context.Background()); !errors.Is(err, errStoreClosed) {
		t.Fatalf("Flush after Close = %v, want errStoreClosed", err)
	}
}

func TestMemStore(t *testing.T) {
	s := NewMemStore()
	defer s.Close()
	mustCreate(t, s, testMeta("s_01", "内存会话", testTime))
	if err := s.CreateSession(context.Background(), testMeta("s_01", "dup", testTime)); !errors.Is(err, ErrSessionExists) {
		t.Fatalf("duplicate = %v, want ErrSessionExists", err)
	}
	if err := s.Append(context.Background(), testMessageRecord("s_02", "m", llm.RoleUser, textBlocks("x"), testTime)); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("append missing = %v, want ErrSessionNotFound", err)
	}
	mustAppend(t, s, testMessageRecord("s_01", "m1", llm.RoleUser, textBlocks("a"), testTime.Add(time.Second)))
	mustAppend(t, s, testUsageRecord("s_01", 5, 6, testTime.Add(2*time.Second)))

	loaded, err := s.Load(context.Background(), "s_01")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 3 {
		t.Fatalf("loaded %d records, want 3", len(loaded))
	}
	loaded[1].Message.Blocks[0].Text = "mutated"
	again, err := s.Load(context.Background(), "s_01")
	if err != nil {
		t.Fatal(err)
	}
	if again[1].Message.Blocks[0].Text != "a" {
		t.Fatal("Load did not return an isolated copy")
	}

	infos, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Title != "内存会话" {
		t.Fatalf("List = %+v", infos)
	}
	if infos[0].Messages != 1 {
		t.Fatalf("messages = %d, want 1", infos[0].Messages)
	}
	if !infos[0].UpdatedAt.Equal(testTime.Add(2 * time.Second)) {
		t.Fatalf("updated = %v", infos[0].UpdatedAt)
	}
	if err := s.Delete(context.Background(), "s_01"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(context.Background(), "s_01"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Load after delete = %v, want ErrSessionNotFound", err)
	}
}
