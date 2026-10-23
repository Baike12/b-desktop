package project

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreAddListDedupeAndPersist(t *testing.T) {
	root := t.TempDir()
	alpha := filepath.Join(root, "alpha")
	beta := filepath.Join(root, "beta")
	for _, dir := range []string{alpha, beta} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	file := filepath.Join(root, "state", "projects.json")
	store := NewStore(file)
	ctx := context.Background()

	alphaReal, err := filepath.EvalSymlinks(alpha)
	if err != nil {
		t.Fatal(err)
	}
	betaReal, err := filepath.EvalSymlinks(beta)
	if err != nil {
		t.Fatal(err)
	}

	first, err := store.Add(ctx, alpha)
	if err != nil {
		t.Fatalf("Add(alpha): %v", err)
	}
	if first.Name != "alpha" || first.Path != alphaReal {
		t.Fatalf("unexpected project: %+v", first)
	}
	if _, err := store.Add(ctx, beta+string(filepath.Separator)); err != nil {
		t.Fatalf("Add(beta): %v", err)
	}
	again, err := store.Add(ctx, alpha)
	if err != nil {
		t.Fatalf("Add(alpha) twice: %v", err)
	}
	if !again.AddedAt.Equal(first.AddedAt) {
		t.Fatalf("重复添加不应改变条目: %+v vs %+v", again, first)
	}

	// 新加入的项目排在最前。
	items, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 2 || items[0].Path != betaReal || items[1].Path != alphaReal {
		t.Fatalf("unexpected order: %+v", items)
	}

	// 重新创建 Store 后仍能读回，验证落盘。
	reopened, err := NewStore(file).List(ctx)
	if err != nil {
		t.Fatalf("List after reopen: %v", err)
	}
	if len(reopened) != 2 || reopened[0].Path != betaReal {
		t.Fatalf("unexpected persisted list: %+v", reopened)
	}

	info, err := os.Stat(file)
	if err != nil {
		t.Fatalf("stat projects.json: %v", err)
	}
	if info.Mode().Perm() != fileMode {
		t.Fatalf("file mode = %o, want %o", info.Mode().Perm(), fileMode)
	}
}

func TestStoreRejectsInvalidPath(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "note.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewStore(filepath.Join(root, "projects.json"))
	ctx := context.Background()

	if _, err := store.Add(ctx, ""); err == nil {
		t.Fatal("空路径应返回错误")
	}
	if _, err := store.Add(ctx, file); err == nil {
		t.Fatal("文件路径应返回错误")
	}
	if _, err := store.Add(ctx, filepath.Join(root, "missing")); err == nil {
		t.Fatal("不存在的目录应返回错误")
	}
}

func TestStoreRemove(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "p")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store := NewStore(filepath.Join(root, "projects.json"))
	if _, err := store.Add(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(ctx, dir); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	items, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("Remove 后仍存在: %+v", items)
	}
	if err := store.Remove(ctx, dir); err != nil {
		t.Fatalf("重复 Remove 应为空操作: %v", err)
	}
}

func TestStoreCorruptFile(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "projects.json")
	if err := os.WriteFile(file, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(file).List(context.Background()); err == nil {
		t.Fatal("损坏文件应返回错误")
	}
}

func TestStoreContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := NewStore(filepath.Join(t.TempDir(), "projects.json"))
	if _, err := store.List(ctx); err == nil {
		t.Fatal("已取消的 ctx 应返回错误")
	}
}
