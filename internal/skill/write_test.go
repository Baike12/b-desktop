package skill

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newWriteLoader(t *testing.T) (*Loader, string) {
	t.Helper()
	userDir := filepath.Join(t.TempDir(), "skills")
	loader, err := NewLoaderAt([]string{userDir}, []string{SourceUser})
	if err != nil {
		t.Fatalf("NewLoaderAt: %v", err)
	}
	return loader, userDir
}

func TestCreateUpdateDeleteSkill(t *testing.T) {
	loader, userDir := newWriteLoader(t)

	created, err := loader.Create("代码审查", "审查改动", "# 步骤\n\n1. 读文件")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Name != "代码审查" || created.Description != "审查改动" || created.Source != SourceUser {
		t.Fatalf("created = %+v", created)
	}
	body, err := loader.Body(t.Context(), "代码审查")
	if err != nil {
		t.Fatalf("Body: %v", err)
	}
	if !strings.Contains(body, "1. 读文件") {
		t.Fatalf("body = %q", body)
	}

	if err := loader.Update("代码审查", "新的描述", "更新后的正文"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	updated, ok := loader.Lookup("代码审查")
	if !ok || updated.Description != "新的描述" {
		t.Fatalf("updated = %+v ok=%v", updated, ok)
	}

	if err := loader.Delete("代码审查"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := loader.Lookup("代码审查"); ok {
		t.Fatalf("skill still present after delete")
	}
	entries, err := os.ReadDir(userDir)
	if err != nil {
		t.Fatalf("read user dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("user dir not empty: %v", entries)
	}
}

func TestCreateDuplicateNameRejected(t *testing.T) {
	loader, _ := newWriteLoader(t)
	if _, err := loader.Create("dup", "d", "c"); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := loader.Create("dup", "d", "c")
	if !errors.Is(err, ErrSkillExists) {
		t.Fatalf("err = %v, want ErrSkillExists", err)
	}
}

func TestUpdateMissingSkill(t *testing.T) {
	loader, _ := newWriteLoader(t)
	if err := loader.Update("missing", "d", "c"); !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("err = %v, want ErrSkillNotFound", err)
	}
}

func TestCreateDirNameSanitizedAndUnique(t *testing.T) {
	loader, userDir := newWriteLoader(t)
	if _, err := loader.Create("a/b", "d", "c"); err != nil {
		t.Fatalf("create a/b: %v", err)
	}
	// 不同名称可能派生出相同目录名，必须自动加后缀而不是覆盖。
	if _, err := loader.Create("a b", "d2", "c2"); err != nil {
		t.Fatalf("create a b: %v", err)
	}
	entries, err := os.ReadDir(userDir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("dirs = %d, want 2", len(entries))
	}
	if _, err := os.Stat(filepath.Join(userDir, "a-b", SkillFileName)); err != nil {
		t.Fatalf("expected a-b/SKILL.md: %v", err)
	}
}

func TestDeleteRejectsOutsidePath(t *testing.T) {
	loader, userDir := newWriteLoader(t)
	outside := filepath.Join(t.TempDir(), "victim")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, SkillFileName), []byte("---\nname: victim\n---\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// 直接把越界技能塞进缓存，验证 Delete 会拒绝删除受管理目录之外的路径。
	loader.mu.Lock()
	loader.skills["victim"] = Skill{Name: "victim", Path: filepath.Join(outside, SkillFileName), Source: SourceUser}
	loader.mu.Unlock()
	if err := loader.Delete("victim"); !errors.Is(err, ErrSkillPath) {
		t.Fatalf("err = %v, want ErrSkillPath", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside dir was removed: %v", err)
	}
	_ = userDir
}
