package main

import "testing"

func TestShellQuotePath(t *testing.T) {
	got := shellQuotePath("/tmp/it's spaced.txt")
	want := "'/tmp/it'\\''s spaced.txt' "
	if got != want {
		t.Fatalf("shellQuotePath() = %q, want %q", got, want)
	}
}
