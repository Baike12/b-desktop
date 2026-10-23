package app

import (
	"path/filepath"
	"testing"

	"github.com/Baike12/b-desktop/internal/project"
)

func TestProjectPathForWorkDir(t *testing.T) {
	projectPath := filepath.Join(t.TempDir(), "b-desktop")
	projects := []project.Project{{Path: projectPath}}
	worktreesRoot := filepath.Join(filepath.Dir(projectPath), "worktrees", filepath.Base(projectPath))

	cases := []struct {
		name    string
		workDir string
		want    string
	}{
		{name: "exact project", workDir: projectPath, want: projectPath},
		{name: "project subdirectory", workDir: filepath.Join(projectPath, "internal"), want: ""},
		{name: "managed worktree", workDir: filepath.Join(worktreesRoot, "feature-x"), want: projectPath},
		{name: "nested worktree branch", workDir: filepath.Join(worktreesRoot, "team", "feature-y"), want: projectPath},
		{name: "other project worktree", workDir: filepath.Join(filepath.Dir(worktreesRoot), "other", "feature-x"), want: ""},
		{name: "worktrees root itself", workDir: worktreesRoot, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := projectPathForWorkDir(tc.workDir, projects); got != tc.want {
				t.Fatalf("projectPathForWorkDir(%q) = %q, want %q", tc.workDir, got, tc.want)
			}
		})
	}
}
