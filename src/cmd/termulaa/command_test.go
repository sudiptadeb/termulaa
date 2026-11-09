package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// commandTab creates a command tab over the API and returns its id and
// session id.
func commandTab(t *testing.T, srv *httptest.Server, command string) (tabID, sessionID string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"name": "cmd", "command": command})
	resp, err := http.Post(srv.URL+"/api/tabs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/tabs: %v", err)
	}
	defer resp.Body.Close()
	var tab Tab
	if err := json.NewDecoder(resp.Body).Decode(&tab); err != nil {
		t.Fatalf("decode tab: %v", err)
	}
	return tab.ID, tab.Layout.SessionID
}

// ownTab opens the tab socket and returns it with the owner token. The
// caller keeps it open: the session attach token is valid only meanwhile.
func ownTab(t *testing.T, srv *httptest.Server, tabID string) (*websocket.Conn, string) {
	t.Helper()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/tab/"+tabID, nil)
	if err != nil {
		t.Fatalf("dial tab ws: %v", err)
	}
	var hello struct {
		Type  string `json:"type"`
		Token string `json:"token"`
	}
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := ws.ReadJSON(&hello); err != nil || hello.Type != "connected" {
		t.Fatalf("tab handshake: %v %q", err, hello.Type)
	}
	return ws, hello.Token
}

func attachSession(t *testing.T, srv *httptest.Server, tabID, sessionID, token, extra string) *websocket.Conn {
	t.Helper()
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/session/" + sessionID + "?tab_id=" + tabID + "&token=" + token + extra
	ws, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatalf("dial session ws: %v", err)
	}
	ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	ws.WriteJSON(map[string]interface{}{"type": "resize", "cols": 80, "rows": 24})
	return ws
}

// readUntilExit drains a session socket, returning the exit code and the
// binary output seen on the way.
func readUntilExit(t *testing.T, ws *websocket.Conn) (code int, output []byte) {
	t.Helper()
	for {
		kind, msg, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("session read: %v", err)
		}
		if kind == websocket.BinaryMessage {
			output = append(output, msg...)
			continue
		}
		var ctl struct {
			Type string `json:"type"`
			Code int    `json:"code"`
		}
		if json.Unmarshal(msg, &ctl) == nil && ctl.Type == "exit" {
			return ctl.Code, output
		}
	}
}

func TestCommandSessionExitsOnceAndIsNotRevived(t *testing.T) {
	srv := httptest.NewServer(newTestMux(t))
	defer srv.Close()

	mark := filepath.Join(t.TempDir(), "ran")
	tabID, sessionID := commandTab(t, srv, "echo ran >> "+mark+"; exit 7")
	tabWS, token := ownTab(t, srv, tabID)
	defer tabWS.Close()

	first := attachSession(t, srv, tabID, sessionID, token, "")
	code, _ := readUntilExit(t, first)
	first.Close()
	if code != 7 {
		t.Fatalf("exit code = %d, want 7", code)
	}

	second := attachSession(t, srv, tabID, sessionID, token, "")
	code, _ = readUntilExit(t, second)
	second.Close()
	if code != 7 {
		t.Fatalf("exit code on reattach = %d, want 7", code)
	}
	if runs, _ := os.ReadFile(mark); strings.Count(string(runs), "ran") != 1 {
		t.Fatalf("command ran %d times, want exactly once: %q", strings.Count(string(runs), "ran"), runs)
	}

	resp, err := http.Get(srv.URL + "/api/sessions/" + sessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var info SessionInfo
	json.NewDecoder(resp.Body).Decode(&info)
	if info.Alive || info.Command == "" {
		t.Fatalf("session info = %+v, want a dead command session", info)
	}
}

func TestCommandSessionReportsSignalDeath(t *testing.T) {
	srv := httptest.NewServer(newTestMux(t))
	defer srv.Close()

	tabID, sessionID := commandTab(t, srv, "kill -9 $$")
	tabWS, token := ownTab(t, srv, tabID)
	defer tabWS.Close()

	ws := attachSession(t, srv, tabID, sessionID, token, "")
	defer ws.Close()
	if code, _ := readUntilExit(t, ws); code != 137 {
		t.Fatalf("exit code = %d, want 137 (128+SIGKILL)", code)
	}
}
