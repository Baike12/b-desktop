package telemetry_test

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

	"github.com/Baike12/b-desktop/internal/agent"
	"github.com/Baike12/b-desktop/internal/llm"
	"github.com/Baike12/b-desktop/internal/telemetry"
)

func newStore(t *testing.T) (*telemetry.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "usage.jsonl")
	store, err := telemetry.New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

func metric(at time.Time, model string, stop agent.StopReason, usage llm.Usage, durationMS int64) agent.RequestMetric {
	return agent.RequestMetric{
		SessionID:  "session-1",
		Model:      model,
		Usage:      usage,
		DurationMS: durationMS,
		StopReason: stop,
		At:         at,
	}
}

func TestRecordAggregatesByDayAndModel(t *testing.T) {
	store, path := newStore(t)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("usage log perm = %o, want 600", perm)
	}

	day1 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.Local)
	day1Later := time.Date(2026, 3, 1, 11, 0, 0, 0, time.Local)
	day2 := time.Date(2026, 3, 2, 9, 0, 0, 0, time.Local)

	store.Record(metric(day1, "sonnet", agent.StopCompleted, llm.Usage{
		InputTokens: 100, OutputTokens: 20, CacheReadInputTokens: 30, CacheCreationInputTokens: 5,
	}, 1200))
	failed := metric(day1Later, "sonnet", agent.StopError, llm.Usage{
		InputTokens: 200, OutputTokens: 40,
	}, 800)
	failed.Err = "boom"
	store.Record(failed)
	store.Record(metric(day2, "haiku", agent.StopCompleted, llm.Usage{
		InputTokens: 50, OutputTokens: 10,
	}, 400))

	summary, err := store.Summary(context.Background(), day1, time.Date(2026, 3, 2, 23, 0, 0, 0, time.Local))
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.From != "2026-03-01" || summary.To != "2026-03-02" {
		t.Fatalf("range = %q..%q", summary.From, summary.To)
	}
	if summary.RequestCount != 3 || summary.ErrorCount != 1 {
		t.Fatalf("counts = %d/%d, want 3/1", summary.RequestCount, summary.ErrorCount)
	}
	if summary.InputTokens != 350 || summary.OutputTokens != 70 {
		t.Fatalf("tokens = %d/%d, want 350/70", summary.InputTokens, summary.OutputTokens)
	}
	if summary.CacheReadInputTokens != 30 || summary.CacheCreationInputTokens != 5 {
		t.Fatalf("cache = %d/%d, want 30/5", summary.CacheReadInputTokens, summary.CacheCreationInputTokens)
	}
	if summary.TotalTokens != 455 || summary.DurationMS != 2400 {
		t.Fatalf("total/duration = %d/%d, want 455/2400", summary.TotalTokens, summary.DurationMS)
	}
	if len(summary.Models) != 2 {
		t.Fatalf("models = %d, want 2", len(summary.Models))
	}
	if summary.Models[0].Model != "sonnet" || summary.Models[0].TotalTokens != 395 {
		t.Fatalf("first model = %+v, want sonnet 395", summary.Models[0])
	}
	if summary.Models[1].Model != "haiku" || summary.Models[1].TotalTokens != 60 {
		t.Fatalf("second model = %+v, want haiku 60", summary.Models[1])
	}
	if len(summary.Days) != 2 || summary.Days[0].Date != "2026-03-01" || summary.Days[1].Date != "2026-03-02" {
		t.Fatalf("days = %+v", summary.Days)
	}
	if summary.Days[0].RequestCount != 2 || summary.Days[0].TotalTokens != 395 {
		t.Fatalf("day1 = %+v", summary.Days[0])
	}
	if len(summary.Days[0].Models) != 1 || summary.Days[0].Models[0].Model != "sonnet" {
		t.Fatalf("day1 models = %+v", summary.Days[0].Models)
	}

	all, err := store.Summary(context.Background(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Summary unbounded: %v", err)
	}
	if all.RequestCount != 3 || all.From != "" || all.To != "" {
		t.Fatalf("unbounded summary = %+v", all)
	}

	recent, err := store.Recent(context.Background(), 2)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(recent) != 2 {
		t.Fatalf("recent = %d rows, want 2", len(recent))
	}
	if recent[0].Model != "haiku" || !recent[0].At.Equal(day2) {
		t.Fatalf("recent[0] = %+v, want haiku at day2", recent[0])
	}
	if recent[1].Model != "sonnet" || !recent[1].At.Equal(day1Later) || recent[1].StopReason != "error" {
		t.Fatalf("recent[1] = %+v", recent[1])
	}
	if recent[1].Err != "boom" || recent[1].TotalTokens != 240 || recent[1].DurationMS != 800 {
		t.Fatalf("recent[1] details = %+v", recent[1])
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if lines := strings.Count(string(data), "\n"); lines != 3 {
		t.Fatalf("lines = %d, want 3", lines)
	}
}

func TestSummaryFiltersByDate(t *testing.T) {
	store, _ := newStore(t)
	day1 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.Local)
	day2 := time.Date(2026, 3, 2, 10, 0, 0, 0, time.Local)
	store.Record(metric(day1, "sonnet", agent.StopCompleted, llm.Usage{InputTokens: 10}, 100))
	store.Record(metric(day2, "haiku", agent.StopCompleted, llm.Usage{InputTokens: 20}, 200))

	summary, err := store.Summary(context.Background(), day2, day2)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.RequestCount != 1 || summary.TotalTokens != 20 || len(summary.Days) != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.Days[0].Date != "2026-03-02" {
		t.Fatalf("day = %q, want 2026-03-02", summary.Days[0].Date)
	}
}

func TestInvalidArguments(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()

	if _, err := store.Recent(ctx, 0); !errors.Is(err, telemetry.ErrInvalidLimit) {
		t.Fatalf("Recent(0) error = %v, want ErrInvalidLimit", err)
	}
	day1 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.Local)
	day2 := time.Date(2026, 3, 2, 0, 0, 0, 0, time.Local)
	if _, err := store.Summary(ctx, day2, day1); !errors.Is(err, telemetry.ErrInvalidRange) {
		t.Fatalf("Summary(reversed) error = %v, want ErrInvalidRange", err)
	}
	if _, err := telemetry.New(""); !errors.Is(err, telemetry.ErrEmptyPath) {
		t.Fatalf("New(\"\") error = %v, want ErrEmptyPath", err)
	}
}

func TestCloseIsIdempotentAndStopsWrites(t *testing.T) {
	store, path := newStore(t)
	at := time.Date(2026, 3, 1, 10, 0, 0, 0, time.Local)
	store.Record(metric(at, "sonnet", agent.StopCompleted, llm.Usage{InputTokens: 1}, 10))
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	store.Record(metric(at, "sonnet", agent.StopCompleted, llm.Usage{InputTokens: 1}, 10))

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if lines := strings.Count(string(data), "\n"); lines != 1 {
		t.Fatalf("lines = %d, want 1", lines)
	}
	summary, err := store.Summary(context.Background(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Summary after Close: %v", err)
	}
	if summary.RequestCount != 1 {
		t.Fatalf("requests = %d, want 1", summary.RequestCount)
	}
}

func TestMalformedLinesAreSkipped(t *testing.T) {
	store, path := newStore(t)
	at := time.Date(2026, 3, 1, 10, 0, 0, 0, time.Local)
	store.Record(metric(at, "sonnet", agent.StopCompleted, llm.Usage{InputTokens: 7}, 10))
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if _, err := file.WriteString("not-json\n\n"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	summary, err := store.Summary(context.Background(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.RequestCount != 1 || summary.TotalTokens != 7 {
		t.Fatalf("summary = %+v, want 1 request / 7 tokens", summary)
	}
	recent, err := store.Recent(context.Background(), 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(recent) != 1 {
		t.Fatalf("recent = %d, want 1", len(recent))
	}
}

func TestRecordNormalizesMissingFields(t *testing.T) {
	store, _ := newStore(t)
	at := time.Date(2026, 3, 1, 10, 0, 0, 0, time.Local)
	store.Record(agent.RequestMetric{At: at, DurationMS: -5})
	recent, err := store.Recent(context.Background(), 1)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(recent) != 1 {
		t.Fatalf("recent = %d, want 1", len(recent))
	}
	if recent[0].Model != "unknown" {
		t.Fatalf("model = %q, want unknown", recent[0].Model)
	}
	if !recent[0].At.Equal(at) {
		t.Fatalf("at = %v, want %v", recent[0].At, at)
	}
	if recent[0].DurationMS != 0 {
		t.Fatalf("duration = %d, want 0", recent[0].DurationMS)
	}
}

func TestConcurrentRecord(t *testing.T) {
	store, path := newStore(t)
	at := time.Date(2026, 3, 1, 10, 0, 0, 0, time.Local)
	const writes = 64
	var wg sync.WaitGroup
	wg.Add(writes)
	for i := 0; i < writes; i++ {
		go func(i int) {
			defer wg.Done()
			store.Record(metric(at, fmt.Sprintf("model-%d", i%4), agent.StopCompleted, llm.Usage{InputTokens: 1}, 1))
		}(i)
	}
	wg.Wait()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if lines := strings.Count(string(data), "\n"); lines != writes {
		t.Fatalf("lines = %d, want %d", lines, writes)
	}
	summary, err := store.Summary(context.Background(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.RequestCount != writes || len(summary.Models) != 4 {
		t.Fatalf("summary = requests %d models %d", summary.RequestCount, len(summary.Models))
	}
}
