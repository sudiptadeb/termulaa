package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestShellQuotePath(t *testing.T) {
	got := shellQuotePath("/tmp/it's spaced.txt")
	want := "'/tmp/it'\\''s spaced.txt' "
	if got != want {
		t.Fatalf("shellQuotePath() = %q, want %q", got, want)
	}
}

func TestSessionLastOutputStampedByReadPump(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSession(generateID(), findShell(), dir, 80, 24, filepath.Join(dir, "hist"), 0, defaultScrollbackBytes)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer s.Kill()

	// readPump stamps lastOutput as soon as the PTY produces any bytes —
	// the shell's startup prompt is enough. (WriteInput is deliberately not
	// used here: its unsynchronized LastActive write would race with
	// readPump's under -race, a pre-existing quirk outside this test.)
	deadline := time.Now().Add(5 * time.Second)
	for s.LastOutput().IsZero() {
		if time.Now().After(deadline) {
			t.Fatal("LastOutput still zero after PTY produced output")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if got := s.LastOutput(); time.Since(got) > time.Minute || got.After(time.Now()) {
		t.Fatalf("LastOutput = %v, want a recent timestamp", got)
	}
}
