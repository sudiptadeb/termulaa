package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io/fs"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// wsMaxMessageSize caps a single WebSocket frame. Frames larger than this
// close the connection — guards against runaway input / buggy clients.
const wsMaxMessageSize = 1 << 20 // 1 MiB

// attachGrace is how long a joining session client is given to send its
// first resize before it is attached and replayed at the PTY's current size.
const attachGrace = 2 * time.Second

// upgrader allowlists loopback origins only. Non-browser clients that omit
// the Origin header are permitted so CLIs like `websocat` keep working.
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return isOriginAllowed(r.Header.Get("Origin"))
	},
}

// setCSP writes a Content-Security-Policy header for HTML responses.
// `unsafe-eval`/`unsafe-inline` are needed by Alpine.js + Twind; everything
// else stays same-origin.
func setCSP(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; "+
			"script-src 'self' 'unsafe-eval' 'unsafe-inline'; "+
			"style-src 'self' 'unsafe-inline'; "+
			"img-src 'self' data: blob:; "+
			"font-src 'self' data:; "+
			"connect-src 'self' ws:; "+
			"object-src 'none'; "+
			"base-uri 'self'; "+
			"frame-ancestors 'none'")
}

// maxForwardedPrefixLen bounds X-Forwarded-Prefix before any other check.
const maxForwardedPrefixLen = 256

// prefixSegmentPattern allowlists one path segment of a forwarded prefix.
var prefixSegmentPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)

// sanitizePrefix validates an X-Forwarded-Prefix header value: the base-path
// prefix a reverse proxy stripped before forwarding. The value is
// attacker-influenceable input that ends up inside rendered HTML, so anything
// that is not a strict absolute path made of allowlisted segments — no
// scheme/host, no empty or dots-only segments, no trailing slash — is
// rejected and the page renders as if served at the root.
func sanitizePrefix(p string) string {
	if p == "" || len(p) > maxForwardedPrefixLen {
		return ""
	}
	if !strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") {
		return ""
	}
	for _, seg := range strings.Split(p[1:], "/") {
		if !prefixSegmentPattern.MatchString(seg) || strings.Trim(seg, ".") == "" {
			return ""
		}
	}
	return p
}

var basePlaceholder = []byte(`<base href="/">`)

// serveUIPage renders one embedded HTML page. When the request carries a
// valid X-Forwarded-Prefix (set by a reverse proxy hosting termulaa under a
// path), the page's <base href="/"> becomes <base href="<prefix>/"> so its
// base-relative URLs resolve under that prefix; routes never change — the
// proxy strips the prefix before forwarding.
func serveUIPage(w http.ResponseWriter, r *http.Request, file string) {
	data, err := uiFS.ReadFile("ui/" + file)
	if err != nil {
		http.Error(w, file+" not found", http.StatusInternalServerError)
		return
	}
	if prefix := sanitizePrefix(r.Header.Get("X-Forwarded-Prefix")); prefix != "" {
		rendered := []byte(`<base href="` + html.EscapeString(prefix) + `/">`)
		data = bytes.Replace(data, basePlaceholder, rendered, 1)
	}
	setCSP(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func registerRoutes(mux *http.ServeMux, mgr *SessionManager, cfg *FullConfig) {
	// Static UI files (embedded)
	sub, err := fs.Sub(uiFS, "ui")
	if err != nil {
		log.Fatalf("Failed to create sub filesystem: %v", err)
	}
	fileServer := http.FileServer(http.FS(sub))

	// HTML pages
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		serveUIPage(w, r, "index.html")
	})
	mux.HandleFunc("GET /tab/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !isValidID(r.PathValue("id")) {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		serveUIPage(w, r, "tab.html")
	})
	mux.HandleFunc("GET /settings", func(w http.ResponseWriter, r *http.Request) {
		serveUIPage(w, r, "settings.html")
	})

	// REST API — Tabs
	mux.HandleFunc("GET /api/tabs", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, mgr.ListTabs())
	})
	mux.HandleFunc("POST /api/tabs", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Name == "" {
			req.Name = "Terminal"
		}
		tab, err := mgr.CreateTab(req.Name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, tab)
	})
	mux.HandleFunc("GET /api/tabs/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !isValidID(id) {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		tab := mgr.GetTab(id)
		if tab == nil {
			http.Error(w, "tab not found", http.StatusNotFound)
			return
		}
		jsonResponse(w, tab)
	})
	mux.HandleFunc("PUT /api/tabs/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !isValidID(id) {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var req struct {
			Name   string      `json:"name"`
			Layout *LayoutNode `json:"layout"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if err := mgr.UpdateTab(id, req.Name, req.Layout); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		mgr.SaveState()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("DELETE /api/tabs/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !isValidID(id) {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		if err := mgr.DeleteTab(id, true); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		mgr.SaveState()
		w.WriteHeader(http.StatusOK)
	})

	// REST API — Sessions (read/delete only; creation is via tab WS)
	mux.HandleFunc("GET /api/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !isValidID(id) {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		s := mgr.GetSession(id)
		if s == nil {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		jsonResponse(w, s.Info())
	})
	mux.HandleFunc("DELETE /api/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !isValidID(id) {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		if err := mgr.DeleteSession(id); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		mgr.SaveState()
		w.WriteHeader(http.StatusOK)
	})

	// REST API — Settings
	mux.HandleFunc("GET /api/settings", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, cfg)
	})
	mux.HandleFunc("PUT /api/settings", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(cfg); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if err := saveFullConfig(cfg); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	// WebSocket — tab ownership channel
	mux.HandleFunc("/ws/tab/{tabID}", func(w http.ResponseWriter, r *http.Request) {
		handleTabWS(w, r, mgr)
	})

	// WebSocket — per-session PTY I/O
	mux.HandleFunc("/ws/session/{sessionID}", func(w http.ResponseWriter, r *http.Request) {
		handleSessionWS(w, r, mgr)
	})

	// Health
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, map[string]interface{}{
			"status":   "ok",
			"version":  Version,
			"sessions": len(mgr.ListSessions()),
			"tabs":     len(mgr.ListTabs()),
		})
	})

	// Static files (catch-all, must be last)
	mux.Handle("/", fileServer)
}

// ---------------------------------------------------------------------------
// handleTabWS — tab ownership WebSocket
// ---------------------------------------------------------------------------

func handleTabWS(w http.ResponseWriter, r *http.Request, mgr *SessionManager) {
	tabID := r.PathValue("tabID")
	if !isValidID(tabID) {
		http.Error(w, "invalid tab id", http.StatusBadRequest)
		return
	}
	tab := mgr.GetTab(tabID)
	if tab == nil {
		http.Error(w, "tab not found", http.StatusNotFound)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("tab-ws: upgrade failed: %v", err)
		return
	}
	conn.SetReadLimit(wsMaxMessageSize)

	if mgr.IsTabActive(tabID) {
		// Tab already has an owner — send conflict and wait for resolution.
		conflictMsg, _ := json.Marshal(map[string]string{
			"type":    "conflict",
			"message": "Tab is active in another window",
		})
		if err := conn.WriteMessage(websocket.TextMessage, conflictMsg); err != nil {
			conn.Close()
			return
		}

		_, msg, err := conn.ReadMessage()
		if err != nil {
			conn.Close()
			return
		}

		var resolution struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(msg, &resolution); err != nil {
			conn.Close()
			return
		}

		switch resolution.Type {
		case "takeover":
			mgr.DisconnectTabOwner(tabID)
		case "new":
			_, err := mgr.ReplaceTabSessions(tabID)
			if err != nil {
				errMsg, _ := json.Marshal(map[string]string{"type": "error", "message": err.Error()})
				conn.WriteMessage(websocket.TextMessage, errMsg)
				conn.Close()
				return
			}
			mgr.DisconnectTabOwner(tabID)
		default:
			conn.Close()
			return
		}
	}

	tc := &tabClient{
		conn:  conn,
		tabID: tabID,
		token: generateID(),
	}

	mgr.RegisterTabClient(tabID, tc)
	defer func() {
		mgr.UnregisterTabClient(tabID, tc)
		conn.Close()
		log.Printf("tab-ws: client disconnected from tab %s", tabID)
	}()

	// Send connected message with current layout.
	tab = mgr.GetTab(tabID)
	connectedMsg, _ := json.Marshal(map[string]interface{}{
		"type":     "connected",
		"layout":   tab.Layout,
		"sessions": tab.SessionIDs(),
		"token":    tc.token,
	})
	if err := conn.WriteMessage(websocket.TextMessage, connectedMsg); err != nil {
		return
	}

	log.Printf("tab-ws: client connected to tab %s (remote=%s)", tabID, r.RemoteAddr)

	// Read loop: handle tab-level commands from the client.
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}

		var cmd struct {
			Type      string      `json:"type"`
			SessionID string      `json:"session_id"`
			Direction string      `json:"direction"`
			Layout    *LayoutNode `json:"layout"`
			Name      string      `json:"name"`
		}
		if err := json.Unmarshal(msg, &cmd); err != nil {
			continue
		}

		switch cmd.Type {
		case "split":
			session, newLayout, err := mgr.SplitPane(tabID, cmd.SessionID, cmd.Direction)
			if err != nil {
				errMsg, _ := json.Marshal(map[string]string{"type": "error", "message": err.Error()})
				conn.WriteMessage(websocket.TextMessage, errMsg)
				continue
			}
			mgr.BroadcastTabEvent(tabID, map[string]interface{}{
				"type":            "session_created",
				"id":              session.ID,
				"pane_session_id": cmd.SessionID,
				"direction":       cmd.Direction,
				"layout":          newLayout,
			})

		case "close_pane":
			newLayout, err := mgr.ClosePane(tabID, cmd.SessionID)
			if err != nil {
				errMsg, _ := json.Marshal(map[string]string{"type": "error", "message": err.Error()})
				conn.WriteMessage(websocket.TextMessage, errMsg)
				continue
			}
			mgr.BroadcastTabEvent(tabID, map[string]interface{}{
				"type":   "pane_closed",
				"id":     cmd.SessionID,
				"layout": newLayout,
			})

		case "update_layout":
			if cmd.Layout != nil {
				mgr.UpdateTab(tabID, "", cmd.Layout)
				mgr.SaveState()
			}

		case "rename":
			if cmd.Name != "" {
				mgr.UpdateTab(tabID, cmd.Name, nil)
				mgr.SaveState()
			}
		}
	}
}

// ---------------------------------------------------------------------------
// handleSessionWS — per-session PTY I/O WebSocket
// ---------------------------------------------------------------------------

func handleSessionWS(w http.ResponseWriter, r *http.Request, mgr *SessionManager) {
	sessionID := r.PathValue("sessionID")
	if !isValidID(sessionID) {
		http.Error(w, "invalid session id", http.StatusBadRequest)
		return
	}
	tabID := r.URL.Query().Get("tab_id")
	token := r.URL.Query().Get("token")
	if !isValidID(tabID) || !isValidID(token) {
		http.Error(w, "invalid session attachment", http.StatusForbidden)
		return
	}
	if !mgr.CanAttachSession(tabID, sessionID, token) {
		http.Error(w, "session attachment forbidden", http.StatusForbidden)
		return
	}

	// Look up session
	sess := mgr.GetSession(sessionID)
	if sess == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	// A dead session is revived on attach. Its terminal state (scrollback
	// and last screen) carries over into the new session, so the client
	// sees the old output followed by the new shell's startup.
	revived := false
	if !sess.Alive {
		var err error
		sess, err = mgr.ReviveSession(sessionID)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to revive session: %v", err), http.StatusInternalServerError)
			return
		}
		revived = true
	}

	// Upgrade to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("session-ws: upgrade failed: %v", err)
		return
	}
	conn.SetReadLimit(wsMaxMessageSize)

	log.Printf("session-ws: client attached to session %s (remote=%s, revived=%v)", sessionID, r.RemoteAddr, revived)

	client := newWSClient(conn, tabID, token)
	go client.writePump(sess)

	defer func() {
		sess.DetachClient(client)
		conn.Close()
		log.Printf("session-ws: client detached from session %s (remote=%s)", sessionID, r.RemoteAddr)
	}()

	// The snapshot must be sized for the terminal that will render it, so
	// the join waits for the client's first resize (sent on open) before
	// snapshotting and attaching; a client that never sends one is joined
	// after attachGrace so it is not left blank. Input received before the
	// join still reaches the PTY.
	sized := make(chan bool, 1) // carries "did the first resize change the PTY size"
	var sizeOnce sync.Once
	go func() {
		sizeChanged := false
		select {
		case sizeChanged = <-sized:
		case <-time.After(attachGrace):
		case <-client.done:
			return
		}
		replayed := sess.AttachClient(client, true)
		// The snapshot is a faithful copy of the terminal state, but a
		// full-screen application repainting from its own model is the
		// last word on what the screen should show. The first resize
		// already delivered a SIGWINCH when it changed the size; otherwise
		// force one now. Line-mode shells only redraw their prompt line on
		// a SIGWINCH, so the nudge is spent when an application holds the
		// alternate screen or nothing could be replayed.
		if !sizeChanged && (sess.InAltScreen() || !replayed) {
			sess.NudgeRedraw()
		}
	}()

	// Reader loop: WebSocket → PTY
	for {
		msgType, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}

		if msgType == websocket.TextMessage {
			var control struct {
				Type string `json:"type"`
				Cols int    `json:"cols"`
				Rows int    `json:"rows"`
				Name string `json:"name"`
				Data string `json:"data"`
			}
			if json.Unmarshal(msg, &control) == nil {
				switch control.Type {
				case "resize":
					changed := sess.Resize(control.Cols, control.Rows)
					sizeOnce.Do(func() { sized <- changed })
					continue
				case "file":
					if path, err := sess.HandleFileDrop(control.Name, control.Data); err != nil {
						log.Printf("File drop error: %v", err)
					} else {
						sess.WriteInput([]byte(shellQuotePath(path)))
					}
					continue
				case "closing":
					log.Printf("session-ws: client closing session %s", sessionID)
					continue
				}
			}
		}

		sess.WriteInput(msg)
	}

	close(client.done)
}

func jsonResponse(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}
