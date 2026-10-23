package session

import (
	"errors"
	"testing"
	"time"

	"anc-dsk/internal/llm"
)

func metaRec(id string, at time.Time) Record {
	return Record{
		ID: "rec_meta", Type: RecordSessionMeta, SessionID: id, Time: at,
		Meta: map[string]string{"title": "标题", "workDir": "/repo", "model": "m1"},
	}
}

func msgRec(sessionID string, m llm.Message) Record {
	return Record{Type: RecordMessage, SessionID: sessionID, Time: m.Time, Message: &m}
}

func toolUseIDList(ids ...string) []llm.ContentBlock {
	out := make([]llm.ContentBlock, 0, len(ids))
	for _, id := range ids {
		out = append(out, llm.ContentBlock{Type: llm.BlockToolUse, ToolUseID: id, ToolName: "Read", Input: []byte(`{}`)})
	}
	return out
}

func resultBlocks(ids ...string) []llm.ContentBlock {
	out := make([]llm.ContentBlock, 0, len(ids))
	for _, id := range ids {
		out = append(out, llm.ContentBlock{Type: llm.BlockToolResult, ToolUseID: id, Text: "ok-" + id})
	}
	return out
}

func TestReplayNoBoundary(t *testing.T) {
	base := testTime
	recs := []Record{
		metaRec("s_01", base),
		msgRec("s_01", llm.Message{ID: "u1", Role: llm.RoleUser, Time: base.Add(time.Second), Blocks: textBlocks("问题")}),
		msgRec("s_01", llm.Message{ID: "a1", Role: llm.RoleAssistant, Time: base.Add(2 * time.Second), Blocks: textBlocks("先读文件")}),
		testUsageRecord("s_01", 10, 5, base.Add(2*time.Second)),
		testUsageRecord("s_01", 3, 2, base.Add(3*time.Second)),
		{Type: RecordMode, SessionID: "s_01", Time: base.Add(3 * time.Second), Meta: map[string]string{"mode": "plan"}},
		msgRec("s_01", llm.Message{ID: "a2", Role: llm.RoleAssistant, Time: base.Add(4 * time.Second), Blocks: toolUseIDList("t1")}),
		msgRec("s_01", llm.Message{ID: "u2", Role: llm.RoleUser, Time: base.Add(5 * time.Second), Blocks: resultBlocks("t1")}),
		{Type: RecordTurnEnd, SessionID: "s_01", Time: base.Add(6 * time.Second), TurnEnd: &TurnEnd{TurnID: "turn", StopReason: "completed"}},
	}
	got, err := Replay(recs)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if got.Meta.ID != "s_01" || got.Meta.Title != "标题" || got.Meta.WorkDir != "/repo" || got.Meta.Model != "m1" {
		t.Fatalf("meta = %+v", got.Meta)
	}
	if !got.Meta.CreatedAt.Equal(base) {
		t.Fatalf("createdAt = %v", got.Meta.CreatedAt)
	}
	if got.Records != len(recs) {
		t.Fatalf("records = %d, want %d", got.Records, len(recs))
	}
	if len(got.Messages) != 4 {
		t.Fatalf("messages = %d, want 4: %+v", len(got.Messages), got.Messages)
	}
	if got.Messages[0].Role != llm.RoleUser || got.Messages[0].Blocks[0].Text != "问题" {
		t.Fatalf("first message = %+v", got.Messages[0])
	}
	if got.Messages[3].Blocks[0].Text != "ok-t1" {
		t.Fatalf("tool result = %+v", got.Messages[3].Blocks)
	}
	if got.Usage.InputTokens != 13 || got.Usage.OutputTokens != 7 {
		t.Fatalf("usage = %+v", got.Usage)
	}
	if got.Mode != "plan" {
		t.Fatalf("mode = %q", got.Mode)
	}
	if !got.LastAssistantAt.Equal(base.Add(4 * time.Second)) {
		t.Fatalf("lastAssistantAt = %v", got.LastAssistantAt)
	}
	if got.Interrupted {
		t.Fatal("interrupted = true, want false")
	}
	if got.Boundary != nil {
		t.Fatalf("boundary = %+v, want nil", got.Boundary)
	}
}

func TestReplayCompactBoundary(t *testing.T) {
	base := testTime
	recs := []Record{
		metaRec("s_01", base),
		msgRec("s_01", llm.Message{ID: "u1", Role: llm.RoleUser, Time: base.Add(time.Second), Blocks: textBlocks("旧问题")}),
		msgRec("s_01", llm.Message{ID: "a1", Role: llm.RoleAssistant, Time: base.Add(2 * time.Second), Blocks: textBlocks("旧回答")}),
		testUsageRecord("s_01", 100, 50, base.Add(2*time.Second)),
		{
			Type: RecordCompact, SessionID: "s_01", Time: base.Add(3 * time.Second),
			Compact: &Compact{Trigger: "auto", PreTokens: 1000, PostTokens: 120, Summary: "摘要内容", Model: "m1"},
		},
		msgRec("s_01", llm.Message{ID: "u2", Role: llm.RoleUser, Time: base.Add(4 * time.Second), Blocks: textBlocks("新问题")}),
		msgRec("s_01", llm.Message{ID: "a2", Role: llm.RoleAssistant, Time: base.Add(5 * time.Second), Blocks: textBlocks("新回答")}),
		testUsageRecord("s_01", 7, 3, base.Add(5*time.Second)),
	}
	got, err := Replay(recs)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if got.Boundary == nil || got.Boundary.Summary != "摘要内容" || got.Boundary.PreTokens != 1000 {
		t.Fatalf("boundary = %+v", got.Boundary)
	}
	if len(got.Messages) != 3 {
		t.Fatalf("messages = %d, want summary + 2", len(got.Messages))
	}
	if got.Messages[0].Role != llm.RoleUser || got.Messages[0].Blocks[0].Text != "摘要内容" {
		t.Fatalf("summary message = %+v", got.Messages[0])
	}
	if got.Messages[1].Blocks[0].Text != "新问题" || got.Messages[2].Blocks[0].Text != "新回答" {
		t.Fatalf("post-boundary messages = %+v", got.Messages)
	}
	for _, m := range got.Messages {
		for _, b := range m.Blocks {
			if b.Text == "旧问题" || b.Text == "旧回答" {
				t.Fatalf("message before boundary was replayed: %+v", m)
			}
		}
	}
	if got.Usage.InputTokens != 107 || got.Usage.OutputTokens != 53 {
		t.Fatalf("usage = %+v, want whole-file sum", got.Usage)
	}
}

func TestReplayLastBoundaryWins(t *testing.T) {
	base := testTime
	recs := []Record{
		metaRec("s_01", base),
		msgRec("s_01", llm.Message{ID: "u1", Role: llm.RoleUser, Time: base.Add(time.Second), Blocks: textBlocks("first")}),
		{Type: RecordCompact, SessionID: "s_01", Time: base.Add(2 * time.Second), Compact: &Compact{Summary: "S1"}},
		msgRec("s_01", llm.Message{ID: "u2", Role: llm.RoleUser, Time: base.Add(3 * time.Second), Blocks: textBlocks("mid")}),
		{Type: RecordCompact, SessionID: "s_01", Time: base.Add(4 * time.Second), Compact: &Compact{Summary: "S2"}},
		msgRec("s_01", llm.Message{ID: "u3", Role: llm.RoleUser, Time: base.Add(5 * time.Second), Blocks: textBlocks("tail")}),
	}
	got, err := Replay(recs)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if got.Boundary.Summary != "S2" {
		t.Fatalf("boundary = %q, want S2", got.Boundary.Summary)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(got.Messages))
	}
	if got.Messages[0].Blocks[0].Text != "S2" || got.Messages[1].Blocks[0].Text != "tail" {
		t.Fatalf("messages = %+v", got.Messages)
	}
}

func TestReplayEmptySummary(t *testing.T) {
	base := testTime
	recs := []Record{
		metaRec("s_01", base),
		{Type: RecordCompact, SessionID: "s_01", Time: base.Add(time.Second), Compact: &Compact{Summary: ""}},
		msgRec("s_01", llm.Message{ID: "u1", Role: llm.RoleUser, Time: base.Add(2 * time.Second), Blocks: textBlocks("after")}),
	}
	got, err := Replay(recs)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(got.Messages) != 1 || got.Messages[0].Blocks[0].Text != "after" {
		t.Fatalf("messages = %+v", got.Messages)
	}
}

func TestReplayMissingToolResults(t *testing.T) {
	base := testTime
	recs := []Record{
		metaRec("s_01", base),
		msgRec("s_01", llm.Message{
			ID: "a1", Role: llm.RoleAssistant, Time: base.Add(time.Second),
			Blocks: toolUseIDList("t1", "t2"),
		}),
	}
	got, err := Replay(recs)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("messages = %d, want assistant + synthetic results", len(got.Messages))
	}
	if !got.Interrupted {
		t.Fatal("interrupted = false, want true")
	}
	results := got.Messages[1]
	if results.Role != llm.RoleUser || len(results.Blocks) != 2 {
		t.Fatalf("synthetic results = %+v", results)
	}
	for i, id := range []string{"t1", "t2"} {
		b := results.Blocks[i]
		if b.Type != llm.BlockToolResult || b.ToolUseID != id || !b.IsError || b.Text != interruptedResultText {
			t.Fatalf("result %d = %+v", i, b)
		}
	}
	if !results.Time.Equal(base.Add(time.Second)) {
		t.Fatalf("synthetic time = %v", results.Time)
	}
}

func TestReplayReordersToolResults(t *testing.T) {
	base := testTime
	recs := []Record{
		metaRec("s_01", base),
		msgRec("s_01", llm.Message{
			ID: "a1", Role: llm.RoleAssistant, Time: base.Add(time.Second),
			Blocks: toolUseIDList("t1", "t2"),
		}),
		msgRec("s_01", llm.Message{
			ID: "u1", Role: llm.RoleUser, Time: base.Add(2 * time.Second),
			Blocks: append(resultBlocks("t2", "t1"), llm.ContentBlock{Type: llm.BlockText, Text: "补充说明"}),
		}),
	}
	got, err := Replay(recs)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(got.Messages))
	}
	blocks := got.Messages[1].Blocks
	if len(blocks) != 3 {
		t.Fatalf("blocks = %+v", blocks)
	}
	if blocks[0].ToolUseID != "t1" || blocks[1].ToolUseID != "t2" || blocks[2].Text != "补充说明" {
		t.Fatalf("reordered blocks = %+v", blocks)
	}
	if got.Interrupted {
		t.Fatal("interrupted = true, want false")
	}
}

func TestReplayPartialToolResults(t *testing.T) {
	base := testTime
	recs := []Record{
		metaRec("s_01", base),
		msgRec("s_01", llm.Message{
			ID: "a1", Role: llm.RoleAssistant, Time: base.Add(time.Second),
			Blocks: toolUseIDList("t1", "t2"),
		}),
		msgRec("s_01", llm.Message{
			ID: "u1", Role: llm.RoleUser, Time: base.Add(2 * time.Second),
			Blocks: resultBlocks("t1"),
		}),
	}
	got, err := Replay(recs)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	blocks := got.Messages[1].Blocks
	if len(blocks) != 2 {
		t.Fatalf("blocks = %+v", blocks)
	}
	if blocks[0].ToolUseID != "t1" || blocks[0].IsError {
		t.Fatalf("first result = %+v", blocks[0])
	}
	if blocks[1].ToolUseID != "t2" || !blocks[1].IsError || blocks[1].Text != interruptedResultText {
		t.Fatalf("second result = %+v", blocks[1])
	}
	if !got.Interrupted {
		t.Fatal("interrupted = false, want true")
	}
}

func TestReplayCompleteToolResults(t *testing.T) {
	base := testTime
	recs := []Record{
		metaRec("s_01", base),
		msgRec("s_01", llm.Message{
			ID: "a1", Role: llm.RoleAssistant, Time: base.Add(time.Second),
			Blocks: toolUseIDList("t1"),
		}),
		msgRec("s_01", llm.Message{
			ID: "u1", Role: llm.RoleUser, Time: base.Add(2 * time.Second),
			Blocks: append(resultBlocks("t1"), llm.ContentBlock{Type: llm.BlockText, Text: "note"}),
		}),
	}
	got, err := Replay(recs)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if got.Interrupted {
		t.Fatal("interrupted = true, want false")
	}
	blocks := got.Messages[1].Blocks
	if len(blocks) != 2 || blocks[0].ToolUseID != "t1" || blocks[1].Text != "note" {
		t.Fatalf("blocks = %+v", blocks)
	}
}

func TestReplayInterruptedOnlyForLastAssistant(t *testing.T) {
	base := testTime
	recs := []Record{
		metaRec("s_01", base),
		msgRec("s_01", llm.Message{
			ID: "a1", Role: llm.RoleAssistant, Time: base.Add(time.Second),
			Blocks: toolUseIDList("t1"),
		}),
		msgRec("s_01", llm.Message{
			ID: "a2", Role: llm.RoleAssistant, Time: base.Add(2 * time.Second),
			Blocks: textBlocks("之后还有一条"),
		}),
	}
	got, err := Replay(recs)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if got.Interrupted {
		t.Fatal("interrupted = true, want false: last assistant has no tool_use")
	}
	if len(got.Messages) != 3 {
		t.Fatalf("messages = %d, want assistant + synthetic results + assistant", len(got.Messages))
	}
	if got.Messages[1].Role != llm.RoleUser || got.Messages[1].Blocks[0].ToolUseID != "t1" {
		t.Fatalf("synthetic results = %+v", got.Messages[1])
	}
}

func TestReplayDropsEmptyAssistant(t *testing.T) {
	base := testTime
	recs := []Record{
		metaRec("s_01", base),
		msgRec("s_01", llm.Message{ID: "u1", Role: llm.RoleUser, Time: base.Add(time.Second), Blocks: textBlocks("q")}),
		msgRec("s_01", llm.Message{ID: "a1", Role: llm.RoleAssistant, Time: base.Add(2 * time.Second)}),
		msgRec("s_01", llm.Message{ID: "a2", Role: llm.RoleAssistant, Time: base.Add(3 * time.Second), Blocks: textBlocks("hi")}),
	}
	got, err := Replay(recs)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("messages = %d, want 2: %+v", len(got.Messages), got.Messages)
	}
	if !got.LastAssistantAt.Equal(base.Add(3 * time.Second)) {
		t.Fatalf("lastAssistantAt = %v", got.LastAssistantAt)
	}
}

func TestReplayTimeFallback(t *testing.T) {
	base := testTime
	rec := msgRec("s_01", llm.Message{ID: "a1", Role: llm.RoleAssistant, Blocks: textBlocks("x")})
	rec.Time = base.Add(9 * time.Second)
	recs := []Record{metaRec("s_01", base), rec}
	got, err := Replay(recs)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if !got.LastAssistantAt.Equal(base.Add(9 * time.Second)) {
		t.Fatalf("lastAssistantAt = %v, want record time fallback", got.LastAssistantAt)
	}
	if !got.Messages[0].Time.Equal(base.Add(9 * time.Second)) {
		t.Fatalf("message time = %v", got.Messages[0].Time)
	}
}

func TestReplaySkipsNonMessageRecords(t *testing.T) {
	base := testTime
	recs := []Record{
		metaRec("s_01", base),
		{Type: RecordMessage, SessionID: "s_01", Time: base}, // 没有 Message 载荷，直接构造时应被跳过
		{Type: RecordMode, SessionID: "s_01", Time: base, Meta: map[string]string{"mode": "acceptEdits"}},
		msgRec("s_01", llm.Message{ID: "u1", Role: llm.RoleUser, Time: base.Add(time.Second), Blocks: textBlocks("q")}),
	}
	got, err := Replay(recs)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("messages = %+v", got.Messages)
	}
	if got.Mode != "acceptEdits" {
		t.Fatalf("mode = %q", got.Mode)
	}
}

func TestReplayInvalidFirstRecord(t *testing.T) {
	cases := map[string][]Record{
		"empty":    nil,
		"not meta": {{Type: RecordMessage, SessionID: "s_01", Message: &llm.Message{}}},
		"compact":  {{Type: RecordCompact, SessionID: "s_01", Compact: &Compact{}}},
	}
	for name, recs := range cases {
		if _, err := Replay(recs); !errors.Is(err, ErrInvalidTranscript) {
			t.Errorf("%s: Replay error = %v, want ErrInvalidTranscript", name, err)
		}
	}
}
