package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Config holds the terminal-agent configuration.
// TODO: move to main.go once the full config story is wired up.
type Config struct {
	Port int `json:"port"`
}

const (
	savePeriod       = 30 * time.Second
	cwdPollInterval  = 3 * time.Second
	cleanupAfterDays = 7
)

// stateFile is the JSON structure persisted to ~/.terminal-agent/state.json.
type stateFile struct {
	Sessions map[string]*SessionMeta `json:"sessions"`
	Tabs     map[string]*Tab         `json:"tabs"`
}

// tabClient represents a WebSocket connection to a tab's control channel.
type tabClient struct {
	conn  *websocket.Conn
	tabID string
	mode  string // "owner" or "watch"
}

// SessionManager is the central registry for all sessions and tabs.
type SessionManager struct {
	sessions   map[string]*Session
	tabs       map[string]*Tab
	tabClients map[string][]*tabClient // tabID → connected tab WS clients
	mu         sync.RWMutex
	cfg        *Config // defined in main.go
	dataDir    string
	shell      string
	ctx        context.Context
	cancel     context.CancelFunc
}

// NewSessionManager creates a manager, initializes directories, and detects the shell.
func NewSessionManager(cfg *Config) *SessionManager {
	home, _ := os.UserHomeDir()
	dataDir := filepath.Join(home, ".terminal-agent")

	// Ensure data directories exist.
	os.MkdirAll(filepath.Join(dataDir, "scrollback"), 0700)
	os.MkdirAll(filepath.Join(dataDir, "history"), 0700)

	ctx, cancel := context.WithCancel(context.Background())

	return &SessionManager{
		sessions:   make(map[string]*Session),
		tabs:       make(map[string]*Tab),
		tabClients: make(map[string][]*tabClient),
		cfg:        cfg,
		dataDir:    dataDir,
		shell:      findShell(),
		ctx:        ctx,
		cancel:     cancel,
	}
}

// ---------------------------------------------------------------------------
// State persistence
// ---------------------------------------------------------------------------

// stateFilePath returns the path to state.json.
func (m *SessionManager) stateFilePath() string {
	return filepath.Join(m.dataDir, "state.json")
}

// scrollbackPath returns the path for a session's scrollback file.
func (m *SessionManager) scrollbackPath(id string) string {
	return filepath.Join(m.dataDir, "scrollback", id+".raw")
}

// historyPath returns the HISTFILE path for a session.
func (m *SessionManager) historyPath(id string) string {
	return filepath.Join(m.dataDir, "history", id+".hist")
}

// LoadState reads state.json and populates the sessions/tabs maps.
// Restored sessions are marked alive=false since their PTYs are gone.
// Entries older than cleanupAfterDays are discarded.
func (m *SessionManager) LoadState() {
	data, err := os.ReadFile(m.stateFilePath())
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("manager: failed to read state file: %v", err)
		}
		return
	}

	var state stateFile
	if err := json.Unmarshal(data, &state); err != nil {
		log.Printf("manager: failed to parse state file: %v", err)
		return
	}

	cutoff := time.Now().AddDate(0, 0, -cleanupAfterDays)
	loaded := 0

	m.mu.Lock()
	defer m.mu.Unlock()

	for id, meta := range state.Sessions {
		if meta.LastActive.Before(cutoff) {
			// Clean up old scrollback file.
			os.Remove(m.scrollbackPath(id))
			os.Remove(m.historyPath(id))
			continue
		}

		s := &Session{
			ID:         meta.ID,
			CWD:        meta.CWD,
			Shell:      meta.Shell,
			Cols:       meta.Cols,
			Rows:       meta.Rows,
			ExitCode:   meta.ExitCode,
			Alive:      false, // PTY is gone after restart
			CreatedAt:  meta.CreatedAt,
			LastActive: meta.LastActive,
			scrollback: NewRingBuffer(scrollbackSize),
			clients:    make([]*wsClient, 0),
			done:       make(chan struct{}),
		}
		// The done channel should be closed for dead sessions.
		close(s.done)

		// Load scrollback if available.
		sbPath := m.scrollbackPath(id)
		if err := s.scrollback.LoadFrom(sbPath); err != nil && !os.IsNotExist(err) {
			log.Printf("manager: failed to load scrollback for %s: %v", id, err)
		}

		m.sessions[id] = s
		loaded++
	}

	for id, tab := range state.Tabs {
		if tab.LastActive.Before(cutoff) {
			continue
		}
		m.tabs[id] = tab
	}

	if loaded > 0 {
		log.Printf("manager: restored %d sessions, %d tabs from state", loaded, len(m.tabs))
	}
}

// SaveState persists all session metadata and tab data to state.json.
// For alive sessions, scrollback is also saved to disk.
func (m *SessionManager) SaveState() {
	m.mu.RLock()

	state := stateFile{
		Sessions: make(map[string]*SessionMeta, len(m.sessions)),
		Tabs:     make(map[string]*Tab, len(m.tabs)),
	}

	for id, s := range m.sessions {
		meta := s.Meta()
		state.Sessions[id] = &meta

		// Save scrollback for alive sessions.
		if s.Alive {
			if err := s.scrollback.SaveTo(m.scrollbackPath(id)); err != nil {
				log.Printf("manager: failed to save scrollback for %s: %v", id, err)
			}
		}
	}

	for id, tab := range m.tabs {
		state.Tabs[id] = tab
	}

	m.mu.RUnlock()

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		log.Printf("manager: failed to marshal state: %v", err)
		return
	}

	if err := os.WriteFile(m.stateFilePath(), data, 0600); err != nil {
		log.Printf("manager: failed to write state file: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Start begins background goroutines for periodic state saving and CWD polling.
func (m *SessionManager) Start(ctx context.Context) {
	m.ctx, m.cancel = context.WithCancel(ctx)
	go m.periodicSave()
	go m.pollCWDs()
}

// Stop cancels background goroutines, saves state, and kills all alive sessions.
func (m *SessionManager) Stop() {
	m.cancel()

	m.mu.RLock()
	alive := make([]*Session, 0)
	for _, s := range m.sessions {
		if s.Alive {
			alive = append(alive, s)
		}
	}
	m.mu.RUnlock()

	for _, s := range alive {
		s.Kill()
	}

	m.SaveState()
	log.Printf("manager: stopped, %d sessions killed", len(alive))
}

// periodicSave runs SaveState on a ticker until the context is cancelled.
func (m *SessionManager) periodicSave() {
	ticker := time.NewTicker(savePeriod)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.SaveState()
		}
	}
}

// pollCWDs periodically updates the CWD of alive sessions by inspecting the
// foreground process of each PTY.
func (m *SessionManager) pollCWDs() {
	ticker := time.NewTicker(cwdPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.mu.RLock()
			sessions := make([]*Session, 0)
			for _, s := range m.sessions {
				if s.Alive && s.cmd != nil && s.cmd.Process != nil {
					sessions = append(sessions, s)
				}
			}
			m.mu.RUnlock()

			for _, s := range sessions {
				cwd, err := getCWD(s.cmd.Process.Pid)
				if err == nil && cwd != "" {
					s.mu.Lock()
					s.CWD = cwd
					s.mu.Unlock()
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Session CRUD
// ---------------------------------------------------------------------------

// CreateSession creates a new PTY session with default dimensions.
func (m *SessionManager) CreateSession() (*Session, error) {
	id := generateID()
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "/"
	}

	histFile := m.historyPath(id)

	s, err := NewSession(id, m.shell, home, 80, 24, histFile)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()

	log.Printf("manager: created session %s (shell=%s, cwd=%s)", id, m.shell, home)
	return s, nil
}

// GetSession returns a session by ID, or nil if not found.
func (m *SessionManager) GetSession(id string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[id]
}

// ListSessions returns info snapshots of all sessions.
func (m *SessionManager) ListSessions() []SessionInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	list := make([]SessionInfo, 0, len(m.sessions))
	for _, s := range m.sessions {
		list = append(list, s.Info())
	}
	return list
}

// KillSession terminates a session's PTY.
func (m *SessionManager) KillSession(id string) error {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("session %s not found", id)
	}

	s.Kill()
	log.Printf("manager: killed session %s", id)
	return nil
}

// DeleteSession kills (if alive) and removes a session from the registry.
func (m *SessionManager) DeleteSession(id string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("session %s not found", id)
	}
	delete(m.sessions, id)
	m.mu.Unlock()

	if s.Alive {
		s.Kill()
	}

	// Clean up scrollback and history files.
	os.Remove(m.scrollbackPath(id))
	os.Remove(m.historyPath(id))

	log.Printf("manager: deleted session %s", id)
	return nil
}

// ReviveSession re-spawns a dead session with the same ID, CWD, and scrollback.
func (m *SessionManager) ReviveSession(id string) (*Session, error) {
	m.mu.Lock()
	old, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("session %s not found", id)
	}
	if old.Alive {
		m.mu.Unlock()
		return old, nil // Already alive, nothing to do.
	}
	m.mu.Unlock()

	// Capture old state.
	cwd := old.CWD
	cols := old.Cols
	rows := old.Rows

	// Validate CWD still exists, fall back to HOME.
	if _, err := os.Stat(cwd); err != nil {
		cwd, _ = os.UserHomeDir()
		if cwd == "" {
			cwd = "/"
		}
	}

	histFile := m.historyPath(id)
	s, err := NewSession(id, m.shell, cwd, cols, rows, histFile)
	if err != nil {
		return nil, fmt.Errorf("revive session: %w", err)
	}

	// Old scrollback is NOT copied into the new session's ring buffer.
	// The handler captures it before reviving and sends it separately,
	// avoiding interleaving with the new PTY's startup output.

	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()

	log.Printf("manager: revived session %s (cwd=%s)", id, cwd)
	return s, nil
}

// ---------------------------------------------------------------------------
// Tab CRUD
// ---------------------------------------------------------------------------

// CreateTab creates a new tab with a single-pane layout backed by a new session.
func (m *SessionManager) CreateTab(name string) (*Tab, error) {
	s, err := m.CreateSession()
	if err != nil {
		return nil, fmt.Errorf("create tab: %w", err)
	}

	tabID := generateID()
	tab := NewTab(tabID, name, s.ID)

	m.mu.Lock()
	m.tabs[tabID] = tab
	m.mu.Unlock()

	log.Printf("manager: created tab %s (%q) with session %s", tabID, name, s.ID)
	return tab, nil
}

// GetTab returns a tab by ID, or nil if not found.
func (m *SessionManager) GetTab(id string) *Tab {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tabs[id]
}

// UpdateTab updates a tab's name and/or layout.
func (m *SessionManager) UpdateTab(id string, name string, layout *LayoutNode) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	tab, ok := m.tabs[id]
	if !ok {
		return fmt.Errorf("tab %s not found", id)
	}

	if name != "" {
		tab.Name = name
	}
	if layout != nil {
		tab.Layout = layout
	}
	tab.LastActive = time.Now()
	return nil
}

// DeleteTab removes a tab and optionally kills its sessions.
func (m *SessionManager) DeleteTab(id string, killSessions bool) error {
	m.mu.Lock()
	tab, ok := m.tabs[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("tab %s not found", id)
	}
	delete(m.tabs, id)
	m.mu.Unlock()

	if killSessions {
		for _, sid := range tab.SessionIDs() {
			m.KillSession(sid)
		}
	}

	log.Printf("manager: deleted tab %s (killSessions=%v)", id, killSessions)
	return nil
}

// ListTabs returns info snapshots of all tabs.
func (m *SessionManager) ListTabs() []TabInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	list := make([]TabInfo, 0, len(m.tabs))
	for _, tab := range m.tabs {
		list = append(list, tab.Info(m.sessions))
	}
	return list
}

// ---------------------------------------------------------------------------
// Tab Client (ownership WebSocket) management
// ---------------------------------------------------------------------------

// IsTabActive returns true if the tab has an active owner WS connection.
func (m *SessionManager) IsTabActive(tabID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, tc := range m.tabClients[tabID] {
		if tc.mode == "owner" {
			return true
		}
	}
	return false
}

// DisconnectTabOwner sends a "disconnected" message to the current owner
// and closes their tab WS connection (used for takeover).
func (m *SessionManager) DisconnectTabOwner(tabID string) {
	m.mu.Lock()
	clients := m.tabClients[tabID]
	var remaining []*tabClient
	for _, tc := range clients {
		if tc.mode == "owner" {
			msg, _ := json.Marshal(map[string]string{
				"type":   "disconnected",
				"reason": "takeover",
			})
			tc.conn.WriteMessage(websocket.TextMessage, msg)
			tc.conn.Close()
			log.Printf("tab-ws: disconnected owner for tab %s (takeover)", tabID)
		} else {
			remaining = append(remaining, tc)
		}
	}
	m.tabClients[tabID] = remaining
	m.mu.Unlock()
}

// RegisterTabClient adds a tab WS client to the tracking map.
func (m *SessionManager) RegisterTabClient(tabID string, tc *tabClient) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tabClients[tabID] = append(m.tabClients[tabID], tc)
	log.Printf("tab-ws: registered %s client for tab %s (total=%d)", tc.mode, tabID, len(m.tabClients[tabID]))
}

// UnregisterTabClient removes a specific tab WS client.
func (m *SessionManager) UnregisterTabClient(tabID string, tc *tabClient) {
	m.mu.Lock()
	defer m.mu.Unlock()

	clients := m.tabClients[tabID]
	for i, c := range clients {
		if c == tc {
			m.tabClients[tabID] = append(clients[:i], clients[i+1:]...)
			break
		}
	}
	log.Printf("tab-ws: unregistered %s client for tab %s (remaining=%d)", tc.mode, tabID, len(m.tabClients[tabID]))
}

// BroadcastTabEvent sends a JSON message to all tab WS clients for a given tab.
func (m *SessionManager) BroadcastTabEvent(tabID string, msg interface{}) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}

	m.mu.RLock()
	clients := make([]*tabClient, len(m.tabClients[tabID]))
	copy(clients, m.tabClients[tabID])
	m.mu.RUnlock()

	for _, tc := range clients {
		tc.conn.WriteMessage(websocket.TextMessage, data)
	}
}

// ---------------------------------------------------------------------------
// Tab-driven session operations (split, close, replace)
// ---------------------------------------------------------------------------

// SplitPane creates a new session and inserts it into the tab's layout tree
// beside the pane with the given sessionID. Returns the new session and the
// updated layout root.
func (m *SessionManager) SplitPane(tabID, sessionID, direction string) (*Session, *LayoutNode, error) {
	m.mu.RLock()
	tab, ok := m.tabs[tabID]
	m.mu.RUnlock()
	if !ok {
		return nil, nil, fmt.Errorf("tab %s not found", tabID)
	}

	newSession, err := m.CreateSession()
	if err != nil {
		return nil, nil, err
	}

	m.mu.Lock()
	tab.Layout = splitLayoutNode(tab.Layout, sessionID, newSession.ID, direction)
	tab.LastActive = time.Now()
	layout := tab.Layout
	m.mu.Unlock()

	m.SaveState()
	return newSession, layout, nil
}

// ClosePane kills the session and removes it from the tab's layout tree.
// If this is the last pane, a fresh replacement session is created.
// Returns the updated layout root.
func (m *SessionManager) ClosePane(tabID, sessionID string) (*LayoutNode, error) {
	m.mu.RLock()
	tab, ok := m.tabs[tabID]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("tab %s not found", tabID)
	}

	// Check if this is the only pane (root is a pane with this session ID).
	m.mu.RLock()
	isLastPane := tab.Layout != nil && tab.Layout.Type == "pane" && tab.Layout.SessionID == sessionID
	m.mu.RUnlock()

	if isLastPane {
		// Create a replacement session to keep the tab alive.
		newSession, err := m.CreateSession()
		if err != nil {
			return nil, fmt.Errorf("create replacement session: %w", err)
		}
		m.mu.Lock()
		tab.Layout = &LayoutNode{Type: "pane", SessionID: newSession.ID}
		tab.LastActive = time.Now()
		layout := tab.Layout
		m.mu.Unlock()

		m.DeleteSession(sessionID)
		m.SaveState()
		return layout, nil
	}

	// Remove from layout.
	m.mu.Lock()
	tab.Layout = closePaneInLayout(tab.Layout, sessionID)
	tab.LastActive = time.Now()
	layout := tab.Layout
	m.mu.Unlock()

	// Kill the session.
	m.DeleteSession(sessionID)
	m.SaveState()

	return layout, nil
}

// ReplaceTabSessions replaces all sessions in a tab with fresh ones.
// The layout structure is preserved but session IDs change.
// Returns the new layout.
func (m *SessionManager) ReplaceTabSessions(tabID string) (*LayoutNode, error) {
	m.mu.RLock()
	tab, ok := m.tabs[tabID]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("tab %s not found", tabID)
	}

	// Generate new layout with fresh session IDs.
	newLayout, mapping := replaceAllSessions(tab.Layout)

	// Create new sessions for each new ID.
	for _, newID := range mapping {
		home, _ := os.UserHomeDir()
		if home == "" {
			home = "/"
		}
		histFile := m.historyPath(newID)
		s, err := NewSession(newID, m.shell, home, 80, 24, histFile)
		if err != nil {
			return nil, fmt.Errorf("create replacement session: %w", err)
		}
		m.mu.Lock()
		m.sessions[newID] = s
		m.mu.Unlock()
	}

	// Update tab layout.
	m.mu.Lock()
	tab.Layout = newLayout
	tab.LastActive = time.Now()
	m.mu.Unlock()

	m.SaveState()
	log.Printf("manager: replaced %d sessions in tab %s", len(mapping), tabID)
	return newLayout, nil
}
