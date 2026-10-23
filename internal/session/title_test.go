package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSetTitleUpdatesListAndReplay(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	cases := []struct {
		name string
		open func(t *testing.T) Store
	}{
		{
			name: "file",
			open: func(t *testing.T) Store {
				s, err := NewFileStore(t.TempDir())
				if err != nil {
					t.Fatalf("NewFileStore: %v", err)
				}
				t.Cleanup(func() { _ = s.Close() })
				return s
			},
		},
		{name: "memory", open: func(*testing.T) Store { return NewMemStore() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := tc.open(t)
			mustCreate(t, store, testMeta("s_title", "新会话", base))

			if err := SetTitle(context.Background(), store, "s_title", "  自动标题  "); err != nil {
				t.Fatalf("SetTitle first: %v", err)
			}
			if err := SetTitle(context.Background(), store, "s_title", "第二个标题"); err != nil {
				t.Fatalf("SetTitle second: %v", err)
			}

			infos, err := store.List(context.Background())
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(infos) != 1 || infos[0].Title != "第二个标题" {
				t.Fatalf("infos = %+v, want title 第二个标题", infos)
			}

			recs, err := store.Load(context.Background(), "s_title")
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			loaded, err := Replay(recs)
			if err != nil {
				t.Fatalf("Replay: %v", err)
			}
			if loaded.Meta.Title != "第二个标题" {
				t.Fatalf("replay title = %q", loaded.Meta.Title)
			}
			last := recs[len(recs)-1]
			if last.Type != RecordTitle || last.Meta["title"] != "第二个标题" {
				t.Fatalf("last record = %+v", last)
			}
		})
	}
}

func TestSetTitleValidation(t *testing.T) {
	store := NewMemStore()
	if err := SetTitle(context.Background(), store, "s_missing", "标题"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("missing session error = %v", err)
	}

	mustCreate(t, store, testMeta("s_title", "新会话", time.Now()))
	if err := SetTitle(context.Background(), store, "s_title", "   "); err == nil {
		t.Fatal("empty title accepted")
	}
	if err := SetTitle(context.Background(), store, "../bad", "标题"); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("invalid id error = %v", err)
	}

	long := strings.Repeat("长", MaxTitleRunes+10)
	if err := SetTitle(context.Background(), store, "s_title", long); err != nil {
		t.Fatalf("SetTitle long: %v", err)
	}
	recs, err := store.Load(context.Background(), "s_title")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := recs[len(recs)-1].Meta["title"]
	if n := len([]rune(got)); n != MaxTitleRunes {
		t.Fatalf("stored title runes = %d, want %d", n, MaxTitleRunes)
	}
}
