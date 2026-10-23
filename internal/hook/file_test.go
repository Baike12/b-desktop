package hook

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReadEntriesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".b-desktop", "hooks.json")
	in := map[Event][]FileEntry{
		EventPreToolUse:       {{Matcher: "Bash", Command: "./check.sh"}},
		EventUserPromptSubmit: {{Matcher: "", Command: "./ctx.sh"}},
	}
	if err := WriteEntries(path, in); err != nil {
		t.Fatalf("WriteEntries: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("permissions = %o, want 600", mode)
	}
	out, err := ReadEntries(path)
	if err != nil {
		t.Fatalf("ReadEntries: %v", err)
	}
	if len(out[EventPreToolUse]) != 1 || out[EventPreToolUse][0].Command != "./check.sh" {
		t.Fatalf("pre tool use = %+v", out[EventPreToolUse])
	}
	if len(out[EventUserPromptSubmit]) != 1 || out[EventUserPromptSubmit][0].Matcher != "" {
		t.Fatalf("user prompt submit = %+v", out[EventUserPromptSubmit])
	}
}

func TestWriteEntriesSkipsEmptyCommands(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	in := map[Event][]FileEntry{
		EventPostToolUse: {{Matcher: "Write", Command: "  "}, {Matcher: "Write", Command: " ./w.sh "}},
	}
	if err := WriteEntries(path, in); err != nil {
		t.Fatalf("WriteEntries: %v", err)
	}
	out, err := ReadEntries(path)
	if err != nil {
		t.Fatalf("ReadEntries: %v", err)
	}
	items := out[EventPostToolUse]
	if len(items) != 1 || items[0].Command != "./w.sh" {
		t.Fatalf("items = %+v", items)
	}
}

func TestValidEvent(t *testing.T) {
	for _, name := range []string{"PreToolUse", "PostToolUse", "UserPromptSubmit", "SessionStart"} {
		if !ValidEvent(name) {
			t.Fatalf("%s should be valid", name)
		}
	}
	for _, name := range []string{"Stop", "", "preToolUse"} {
		if ValidEvent(name) {
			t.Fatalf("%s should be invalid", name)
		}
	}
}

func TestManagerPaths(t *testing.T) {
	dir := t.TempDir()
	manager, err := New(Config{WorkDir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	user, project := manager.Paths()
	if user == "" || project == "" {
		t.Fatalf("paths = %q, %q", user, project)
	}
	if project != filepath.Join(dir, ".b-desktop", "hooks.json") {
		t.Fatalf("project path = %q", project)
	}
}
