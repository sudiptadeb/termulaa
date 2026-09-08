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

func TestCleanReplayStart(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wrapped bool
		want    string
	}{
		{"unwrapped untouched", "[?25l\x1b[Hgarbage", false, "[?25l\x1b[Hgarbage"},
		{"cut mid CSI resumes at next ESC", "?25l\x1b[Hhello", true, "\x1b[Hhello"},
		{"leading utf8 continuation dropped", "\x97\xbd\x1b[0mtext", true, "\x1b[0mtext"},
		{"no ESC cuts at newline", "tial line\nnext\n", true, "next\n"},
		{"no ESC no newline unchanged", "plain", true, "plain"},
		{"empty", "", true, ""},
		{"ST inside OSC is a harmless start", "]0;title\x1b\\\x1b[31mred", true, "\x1b\\\x1b[31mred"},
	}
	for _, tc := range cases {
		got := cleanReplayStart([]byte(tc.in), tc.wrapped)
		if string(got) != tc.want {
			t.Errorf("%s: cleanReplayStart(%q, %v) = %q, want %q", tc.name, tc.in, tc.wrapped, got, tc.want)
		}
	}
}

func TestRingBufferReadReplayTrimsWrappedHead(t *testing.T) {
	r := NewRingBuffer(16)
	r.Write([]byte("0123456789"))          // 10 bytes, not full
	r.Write([]byte("ab\x1b[31mXY\x1b[0m")) // wraps: total 22, keeps the last 16
	all := r.ReadAll()
	if len(all) != 16 {
		t.Fatalf("ReadAll len = %d, want 16", len(all))
	}
	got := r.ReadReplay()
	if string(got) != "\x1b[31mXY\x1b[0m" {
		t.Fatalf("ReadReplay = %q, want replay starting at the first ESC", got)
	}
}

func TestTrackAltScreen(t *testing.T) {
	var tail []byte
	alt := false

	tail, alt = trackAltScreen(tail, []byte("prompt$ \x1b[?1049h\x1b[H\x1b[2J"), alt)
	if !alt {
		t.Fatal("expected alt screen after DECSET 1049")
	}
	// Leaving via 1049l split across two chunks.
	tail, alt = trackAltScreen(tail, []byte("frame\x1b[?10"), alt)
	if !alt {
		t.Fatal("a cut sequence must not change state yet")
	}
	tail, alt = trackAltScreen(tail, []byte("49l$ "), alt)
	if alt {
		t.Fatal("expected main screen after the split DECRST 1049 completed")
	}
	// Multi-parameter DECSET with 47 in the middle.
	tail, alt = trackAltScreen(tail, []byte("\x1b[?1000;47;1006h"), alt)
	if !alt {
		t.Fatal("expected alt screen from a multi-parameter DECSET containing 47")
	}
	// Unrelated modes do not touch it; 1047l does.
	tail, alt = trackAltScreen(tail, []byte("\x1b[?2004h\x1b[?25l"), alt)
	if !alt {
		t.Fatal("unrelated DECSET flipped alt screen state")
	}
	_, alt = trackAltScreen(tail, []byte("\x1b[?1047l"), alt)
	if alt {
		t.Fatal("expected main screen after DECRST 1047")
	}
	if len(tail) > altTailKeep {
		t.Fatalf("tail carry %d bytes exceeds altTailKeep %d", len(tail), altTailKeep)
	}
}

func TestClientQueueCoalescesAndOverflows(t *testing.T) {
	c := newWSClient(nil, "tab", "tok")
	c.queueBinary([]byte("abc"))
	c.queueBinary([]byte("def"))
	c.queueText([]byte(`{"type":"x"}`))
	c.queueBinary([]byte("ghi"))
	frames, overflow := c.takePending()
	if overflow {
		t.Fatal("unexpected overflow")
	}
	if len(frames) != 3 || string(frames[0].data) != "abcdef" || !frames[1].text || string(frames[2].data) != "ghi" {
		t.Fatalf("frames = %+v, want [abcdef, text, ghi]", frames)
	}
	if f, _ := c.takePending(); len(f) != 0 {
		t.Fatal("takePending must drain the queue")
	}

	big := make([]byte, maxCoalescedFrame)
	for i := 0; i*len(big) <= maxClientBacklog; i++ {
		c.queueBinary(big)
	}
	frames, overflow = c.takePending()
	if !overflow {
		t.Fatal("expected overflow once the backlog cap is exceeded")
	}
	for _, f := range frames {
		if len(f.data) > maxCoalescedFrame {
			t.Fatalf("coalesced frame %d bytes exceeds maxCoalescedFrame", len(f.data))
		}
	}
	c.queueBinary([]byte("late"))
	if f, _ := c.takePending(); len(f) != 0 {
		t.Fatal("an overflowed client must not accept more output")
	}
}
