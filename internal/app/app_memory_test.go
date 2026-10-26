package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Baike12/b-desktop/internal/session"
)

// newMemoryTestApp 创建带记忆组件的测试应用。
func newMemoryTestApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cfg := extensionsTestConfig(t)
	app, err := NewWithDeps(cfg, func(context.Context, string, any) {}, session.NewMemStore(), nil)
	if err != nil {
		t.Fatalf("NewWithDeps: %v", err)
	}
	return app
}

func TestMemoryBindingsRoundTrip(t *testing.T) {
	app := newMemoryTestApp(t)
	workDir := app.cfg.WorkDir

	empty, err := app.ListMemories("user", "")
	if err != nil || len(empty) != 0 {
		t.Fatalf("initial list = %+v err=%v, want empty", empty, err)
	}

	saved, err := app.SaveMemory(MemorySaveInput{
		Scope:       "user",
		WorkDir:     workDir,
		Name:        "提交前确认",
		Description: "用户要求 git 提交前展示 diff",
		Type:        "feedback",
		Keywords:    []string{"git", "提交"},
		Importance:  8,
		Content:     "git 提交前必须先展示完整 diff 并获得确认。",
	})
	if err != nil {
		t.Fatalf("SaveMemory: %v", err)
	}
	if saved.Scope != "user" || saved.Status != "active" || saved.Importance != 8 {
		t.Fatalf("saved = %+v", saved)
	}

	// 内容相同再次保存必须报重复，不能落第二条。
	if _, err := app.SaveMemory(MemorySaveInput{Scope: "user", WorkDir: workDir, Name: "提交前确认", Content: "git 提交前必须先展示完整 diff 并获得确认。"}); !errors.Is(err, ErrMemoryDuplicate) {
		t.Fatalf("duplicate err = %v, want ErrMemoryDuplicate", err)
	}

	list, err := app.ListMemories("user", "")
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v err=%v", list, err)
	}
	detail, err := app.ReadMemory("user", "", saved.FileName)
	if err != nil {
		t.Fatalf("ReadMemory: %v", err)
	}
	if detail.Body != "git 提交前必须先展示完整 diff 并获得确认。" || detail.AccessCount < 0 {
		t.Fatalf("detail = %+v", detail)
	}

	updated, err := app.UpdateMemory(MemoryUpdateInput{
		Scope:       "user",
		WorkDir:     workDir,
		FileName:    saved.FileName,
		Description: "用户要求所有 git 提交前展示 diff",
	})
	if err != nil {
		t.Fatalf("UpdateMemory: %v", err)
	}
	if updated.Description != "用户要求所有 git 提交前展示 diff" || updated.Body != detail.Body {
		t.Fatalf("updated = %+v", updated)
	}

	// 项目作用域独立存储。
	if _, err := app.SaveMemory(MemorySaveInput{Scope: "project", WorkDir: workDir, Name: "构建命令", Content: "make build && make test。"}); err != nil {
		t.Fatalf("SaveMemory project: %v", err)
	}
	projects, err := app.ListMemories("project", workDir)
	if err != nil || len(projects) != 1 || projects[0].Name != "构建命令" {
		t.Fatalf("projects = %+v err=%v", projects, err)
	}
	users, err := app.ListMemories("user", "")
	if err != nil || len(users) != 1 {
		t.Fatalf("users = %+v err=%v", users, err)
	}

	if err := app.DeleteMemory("project", workDir, projects[0].FileName); err != nil {
		t.Fatalf("DeleteMemory: %v", err)
	}
	projects, err = app.ListMemories("project", workDir)
	if err != nil || len(projects) != 0 {
		t.Fatalf("projects after delete = %+v err=%v", projects, err)
	}
}

func TestMemoryAutoExtractConfigRoundTrip(t *testing.T) {
	app := newMemoryTestApp(t)

	// 测试注入的零值配置默认关闭；先显式打开再验证持久化语义。
	on := true
	if err := app.SetProviderConfig(ProviderConfigInput{
		Provider:          app.cfg.Provider,
		Model:             app.cfg.Model,
		MemoryAutoExtract: &on,
	}); err != nil {
		t.Fatalf("SetProviderConfig on: %v", err)
	}
	if !app.GetProviderConfig().MemoryAutoExtract {
		t.Fatal("MemoryAutoExtract should be on after update")
	}
	off := false
	if err := app.SetProviderConfig(ProviderConfigInput{
		Provider:          app.cfg.Provider,
		Model:             app.cfg.Model,
		MemoryAutoExtract: &off,
	}); err != nil {
		t.Fatalf("SetProviderConfig off: %v", err)
	}
	if app.GetProviderConfig().MemoryAutoExtract {
		t.Fatal("MemoryAutoExtract should be off after update")
	}
	// nil 表示保持不变。
	if err := app.SetProviderConfig(ProviderConfigInput{Provider: app.cfg.Provider, Model: app.cfg.Model}); err != nil {
		t.Fatalf("SetProviderConfig nil: %v", err)
	}
	if app.GetProviderConfig().MemoryAutoExtract {
		t.Fatal("nil input must keep current value")
	}
}

func TestLoadConfigDefaultsMemoryAutoExtract(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.json")
	// 配置文件缺失：记忆抽取默认开启。
	cfg, err := loadConfigFile(path)
	if err != nil {
		t.Fatalf("missing file: %v", err)
	}
	if !cfg.MemoryAutoExtract {
		t.Fatal("missing file should default MemoryAutoExtract to true")
	}
}
