package app

import (
	"errors"
	"testing"
)

func TestExtensionStoreCachesSuccess(t *testing.T) {
	a := &App{}
	calls := 0
	first, err := a.extensionStore("skills", func() (any, error) {
		calls++
		return "loader", nil
	})
	if err != nil {
		t.Fatalf("extensionStore: %v", err)
	}
	second, err := a.extensionStore("skills", func() (any, error) {
		calls++
		return "other", nil
	})
	if err != nil {
		t.Fatalf("extensionStore: %v", err)
	}
	if first != "loader" || second != "loader" {
		t.Fatalf("values = %v, %v, want loader", first, second)
	}
	if calls != 1 {
		t.Fatalf("init calls = %d, want 1", calls)
	}
}

func TestExtensionStoreDoesNotCacheFailure(t *testing.T) {
	a := &App{}
	calls := 0
	if _, err := a.extensionStore("mcp", func() (any, error) {
		calls++
		return nil, errors.New("connect failed")
	}); err == nil {
		t.Fatal("expected error")
	}
	value, err := a.extensionStore("mcp", func() (any, error) {
		calls++
		return "manager", nil
	})
	if err != nil {
		t.Fatalf("extensionStore retry: %v", err)
	}
	if value != "manager" {
		t.Fatalf("value = %v, want manager", value)
	}
	if calls != 2 {
		t.Fatalf("init calls = %d, want 2", calls)
	}
}
