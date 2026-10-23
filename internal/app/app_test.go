package app

import "testing"

func TestGreet(t *testing.T) {
	got := New().Greet("ANC")
	want := "Hello ANC, ANC DSK is running."
	if got != want {
		t.Fatalf("Greet() = %q, want %q", got, want)
	}
}
