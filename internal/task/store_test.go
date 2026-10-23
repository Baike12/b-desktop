package task

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tasks.json")
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s, path
}

func TestStoreMissingFileIsEmpty(t *testing.T) {
	s, _ := newStore(t)
	if got := s.List(); len(got) != 0 {
		t.Fatalf("list = %+v, want empty", got)
	}
}

func TestStoreCorruptFileReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := NewStore(path); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestStoreSkipsInvalidEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.json")
	body := `{"tasks":[
      {"id":"good","cron":"*/5 * * * *","prompt":"p","enabled":true,"createdAtMs":1},
      {"id":"bad-cron","cron":"nope","prompt":"p","enabled":true,"createdAtMs":2},
      {"id":"","cron":"* * * * *","prompt":"p","enabled":true,"createdAtMs":3},
      {"id":"no-prompt","cron":"* * * * *","prompt":"  ","enabled":true,"createdAtMs":4}
    ]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	list := s.List()
	if len(list) != 1 || list[0].ID != "good" {
		t.Fatalf("list = %+v, want only good", list)
	}
}

func TestStoreCreatePersists(t *testing.T) {
	s, path := newStore(t)
	created, err := s.Create(NewTaskInput{Cron: "*/5 * * * *", Prompt: "ping", WorkDir: "/tmp"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == "" || !created.Enabled {
		t.Fatalf("created = %+v", created)
	}
	if created.CreatedAtMS == 0 {
		t.Fatal("CreatedAtMS not set")
	}
	reloaded, err := NewStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	list := reloaded.List()
	if len(list) != 1 || list[0].ID != created.ID || list[0].Prompt != "ping" || list[0].WorkDir != "/tmp" {
		t.Fatalf("reloaded = %+v", list)
	}
}

func TestStoreCreateValidates(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Create(NewTaskInput{Cron: "bad", Prompt: "p"}); !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("err = %v, want ErrInvalidTask", err)
	}
	if _, err := s.Create(NewTaskInput{Cron: "* * * * *", Prompt: "  "}); !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("err = %v, want ErrInvalidTask", err)
	}
	if got := s.List(); len(got) != 0 {
		t.Fatalf("list = %+v, want empty", got)
	}
}

func TestStoreDelete(t *testing.T) {
	s, path := newStore(t)
	created, err := s.Create(NewTaskInput{Cron: "* * * * *", Prompt: "p"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Delete(created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.Delete(created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete err = %v, want ErrNotFound", err)
	}
	reloaded, err := NewStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := reloaded.List(); len(got) != 0 {
		t.Fatalf("list = %+v, want empty", got)
	}
}

func TestStoreSetEnabled(t *testing.T) {
	s, path := newStore(t)
	created, err := s.Create(NewTaskInput{Cron: "* * * * *", Prompt: "p"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.SetEnabled(created.ID, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	reloaded, err := NewStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	list := reloaded.List()
	if len(list) != 1 || list[0].Enabled {
		t.Fatalf("enabled not persisted: %+v", list)
	}
	if err := s.SetEnabled("missing", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestStoreMarkFired(t *testing.T) {
	s, path := newStore(t)
	created, err := s.Create(NewTaskInput{Cron: "* * * * *", Prompt: "p"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	at := time.UnixMilli(1_700_000_000_000)
	if err := s.MarkFired(created.ID, at); err != nil {
		t.Fatalf("MarkFired: %v", err)
	}
	reloaded, err := NewStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	list := reloaded.List()
	if len(list) != 1 || list[0].LastRunAtMS != at.UnixMilli() {
		t.Fatalf("LastRunAtMS = %+v", list)
	}
	if err := s.MarkFired("missing", at); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestStoreFileMode(t *testing.T) {
	_, path := newStore(t)
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := s.Create(NewTaskInput{Cron: "* * * * *", Prompt: "p"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode = %o, want 600", mode)
	}
}

func TestStoreCreateEnabledFalse(t *testing.T) {
	s, _ := newStore(t)
	disabled := false
	created, err := s.Create(NewTaskInput{Cron: "* * * * *", Prompt: "p", Enabled: &disabled})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Enabled {
		t.Fatal("task should be disabled")
	}
}

func TestStorePathAndGet(t *testing.T) {
	s, path := newStore(t)
	if s.Path() != path {
		t.Fatalf("Path = %q, want %q", s.Path(), path)
	}
	created, err := s.Create(NewTaskInput{Cron: "* * * * *", Prompt: "p"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != created.ID || got.Prompt != "p" {
		t.Fatalf("Get = %+v", got)
	}
	if _, err := s.Get("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing err = %v, want ErrNotFound", err)
	}
}

func TestStoreFileJSONShape(t *testing.T) {
	s, path := newStore(t)
	if _, err := s.Create(NewTaskInput{Cron: "0 9 * * *", Prompt: "p"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var file struct {
		Tasks []map[string]any `json:"tasks"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(file.Tasks) != 1 {
		t.Fatalf("tasks = %+v", file.Tasks)
	}
	if file.Tasks[0]["cron"] != "0 9 * * *" {
		t.Fatalf("task = %+v", file.Tasks[0])
	}
}
