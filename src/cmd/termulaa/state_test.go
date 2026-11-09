package main

import (
	"testing"
	"time"
)

func stateTestConfig() *Config {
	return &Config{Port: 1, CleanupAfterDays: 30, DefaultCols: 80, DefaultRows: 24}
}

// A tab that has gone a long time without a rename or a layout change, but
// whose shell is in use, must survive a restart: the handed-over PTY has to
// find its session, or the terminal is lost.
func TestLoadStateKeepsTabWhoseShellIsActive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	before := NewSessionManager(stateTestConfig())
	tab, err := before.CreateTab("daily driver")
	if err != nil {
		t.Fatal(err)
	}
	sessionID := tab.Layout.SessionID
	before.mu.Lock()
	before.tabs[tab.ID].LastActive = time.Now().AddDate(0, 0, -40)
	before.mu.Unlock()
	defer before.Stop()

	files := before.HandoffFiles() // saves state, as the in-place restart does

	after := NewSessionManager(stateTestConfig())
	after.LoadState()
	if after.GetTab(tab.ID) == nil {
		t.Fatalf("tab %s was dropped on load although its shell was active moments ago", tab.ID)
	}
	if after.GetSession(sessionID) == nil {
		t.Fatalf("session %s was dropped with its tab", sessionID)
	}
	if adopted := after.Adopt(files); adopted != 1 {
		t.Fatalf("adopted %d of 1 handed-over terminals", adopted)
	}
}

// The window still applies: a tab nobody touched for longer than it, whose
// sessions are just as stale, is discarded along with them.
func TestLoadStateDropsTabIdlePastTheWindow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	before := NewSessionManager(stateTestConfig())
	tab, err := before.CreateTab("forgotten")
	if err != nil {
		t.Fatal(err)
	}
	sessionID := tab.Layout.SessionID
	old := time.Now().AddDate(0, 0, -40)
	before.mu.Lock()
	before.tabs[tab.ID].LastActive = old
	before.sessions[sessionID].mu.Lock()
	before.sessions[sessionID].LastActive = old
	before.sessions[sessionID].mu.Unlock()
	before.mu.Unlock()
	before.Stop() // kills the shell; its exit does not restamp LastActive
	before.mu.Lock()
	before.sessions[sessionID].LastActive = old
	before.mu.Unlock()
	before.SaveState()

	after := NewSessionManager(stateTestConfig())
	after.LoadState()
	if after.GetTab(tab.ID) != nil || after.GetSession(sessionID) != nil {
		t.Fatalf("stale tab %s or its session %s survived the cleanup window", tab.ID, sessionID)
	}
}
