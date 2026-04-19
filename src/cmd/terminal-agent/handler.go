package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"

	"github.com/gorilla/websocket"
)

// wsMaxMessageSize caps a single WebSocket frame. Frames larger than this
// close the connection — guards against runaway input / buggy clients.
const wsMaxMessageSize = 1 << 20 // 1 MiB

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

func registerRoutes(mux *http.ServeMux, mgr *SessionManager, cfg *FullConfig) {
	// Static UI files (embedded)
	sub, err := fs.Sub(uiFS, "ui")
	if err != nil {
		log.Fatalf("Failed to create sub filesystem: %v", err)
	}
	fileServer := http.FileServer(http.FS(sub))

	// HTML pages
	mux.HandleFunc("GET /tab/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !isValidID(r.PathValue("id")) {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		data, err := uiFS.ReadFile("ui/tab.html")
		if err != nil {
			http.Error(w, "tab.html not found", http.StatusInternalServerError)
			return
		}
		setCSP(w)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	})
	mux.HandleFunc("GET /settings", func(w http.ResponseWriter, r *http.Request) {
		data, err := uiFS.ReadFile("ui/settings.html")
		if err != nil {
			http.Error(w, "settings.html not found", http.StatusInternalServerError)
			return
		}
		setCSP(w)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
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

	// Capture old scrollback BEFORE reviving so it doesn't get interleaved
	// with the new PTY's startup output in the ring buffer.
	var oldScrollback []byte
	revived := false
	if !sess.Alive {
		oldScrollback = sess.scrollback.ReadAll()
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

	log.Printf("session-ws: client attached to session %s (remote=%s, revived=%v, oldScrollback=%d bytes)", sessionID, r.RemoteAddr, revived, len(oldScrollback))

	// Create client
	client := &wsClient{
		conn:       conn,
		send:       make(chan []byte, 256),
		done:       make(chan struct{}),
		tabID:      tabID,
		ownerToken: token,
	}

	// For revived sessions: send the old scrollback we captured before reviving
	// (clean, not interleaved with new PTY startup). Then attach without replay.
	// For alive sessions: replay from the ring buffer (normal reconnect path).
	if revived && len(oldScrollback) > 0 {
		msg, _ := json.Marshal(map[string]string{
			"type": "scrollback",
			"data": base64.StdEncoding.EncodeToString(oldScrollback),
		})
		client.conn.WriteMessage(websocket.TextMessage, msg)
		sess.AttachClient(client, false)
	} else {
		sess.AttachClient(client, true)
	}

	defer func() {
		sess.DetachClient(client)
		conn.Close()
		log.Printf("session-ws: client detached from session %s (remote=%s)", sessionID, r.RemoteAddr)
	}()

	// Writer goroutine: send channel → WebSocket
	go func() {
		for {
			select {
			case data, ok := <-client.send:
				if !ok {
					return
				}
				if err := conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
					return
				}
			case <-client.done:
				return
			case <-sess.Done():
				// Session died — notify client
				exitMsg, _ := json.Marshal(map[string]interface{}{
					"type": "exit",
					"code": sess.ExitCode,
				})
				conn.WriteMessage(websocket.TextMessage, exitMsg)
				return
			}
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
					sess.Resize(control.Cols, control.Rows)
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
