package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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

// readReplay reads the replay header of a raw attach and the binary bytes
// that follow it until the next control frame, which it returns too.
func readRaw(t *testing.T, ws *websocket.Conn) (offset int64, output []byte, next map[string]interface{}) {
	t.Helper()
	offset = -1
	for {
		kind, msg, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("session read: %v", err)
		}
		if kind == websocket.BinaryMessage {
			output = append(output, msg...)
			continue
		}
		var ctl map[string]interface{}
		if err := json.Unmarshal(msg, &ctl); err != nil {
			t.Fatalf("bad control frame %q", msg)
		}
		if ctl["type"] == "replay" {
			if offset != -1 {
				t.Fatal("two replay headers")
			}
			offset = int64(ctl["offset"].(float64))
			continue
		}
		return offset, output, ctl
	}
}

func TestRawAttachResumesFromOffset(t *testing.T) {
	srv := httptest.NewServer(newTestMux(t))
	defer srv.Close()

	tabID, sessionID := commandTab(t, srv, "printf 'first\\n'; read x; printf 'second\\n'")
	tabWS, token := ownTab(t, srv, tabID)
	defer tabWS.Close()

	first := attachSession(t, srv, tabID, sessionID, token, "&from=0")
	var got []byte
	for !bytes.Contains(got, []byte("first\n")) {
		kind, msg, err := first.ReadMessage()
		if err != nil {
			t.Fatalf("first attach: %v", err)
		}
		if kind == websocket.BinaryMessage {
			got = append(got, msg...)
		}
	}
	first.Close()

	// The second attach names how much it has seen and must get only the
	// rest: nothing yet, then the output caused by the input it sends.
	second := attachSession(t, srv, tabID, sessionID, token, "&from="+strconv.Itoa(len(got)))
	second.WriteMessage(websocket.BinaryMessage, []byte("\n"))
	offset, rest, ctl := readRaw(t, second)
	second.Close()
	if offset != int64(len(got)) {
		t.Fatalf("replay offset = %d, want %d", offset, len(got))
	}
	if ctl["type"] != "exit" || ctl["code"].(float64) != 0 {
		t.Fatalf("control after output = %v, want exit 0", ctl)
	}
	if !bytes.HasSuffix(rest, []byte("second\n")) || bytes.Contains(rest, []byte("first")) {
		t.Fatalf("resumed output = %q, want only what followed the first attach", rest)
	}

	// After the command ended, the whole stream is still there from 0.
	third := attachSession(t, srv, tabID, sessionID, token, "&from=0")
	offset, all, ctl := readRaw(t, third)
	third.Close()
	if offset != 0 || !bytes.Equal(all, append(got, rest...)) || ctl["type"] != "exit" {
		t.Fatalf("full replay: offset=%d output=%q ctl=%v", offset, all, ctl)
	}
}

func TestRawAttachReportsOverwrittenBytes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := defaultFullConfig()
	mgr := NewSessionManager(&Config{Port: cfg.Port, ScrollbackBytes: 8})
	mux := http.NewServeMux()
	registerRoutes(mux, mgr, cfg)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tabID, sessionID := commandTab(t, srv, "printf 0123456789ABCDEF")
	tabWS, token := ownTab(t, srv, tabID)
	defer tabWS.Close()

	wait := attachSession(t, srv, tabID, sessionID, token, "")
	readUntilExit(t, wait)
	wait.Close()

	ws := attachSession(t, srv, tabID, sessionID, token, "&from=0")
	defer ws.Close()
	offset, output, ctl := readRaw(t, ws)
	if offset != 8 || string(output) != "89ABCDEF" || ctl["type"] != "exit" {
		t.Fatalf("offset=%d output=%q ctl=%v, want the 8 bytes still held from offset 8", offset, output, ctl)
	}
}

func TestCommandOutputIsExactBytesAndPictureIsNotStairStepped(t *testing.T) {
	srv := httptest.NewServer(newTestMux(t))
	defer srv.Close()

	tabID, sessionID := commandTab(t, srv, "printf 'a\\nb\\n'; read x; printf 'c\\nd\\n'")
	tabWS, token := ownTab(t, srv, tabID)
	defer tabWS.Close()

	// The stream face: the command's bytes, no \r added by the PTY.
	raw := attachSession(t, srv, tabID, sessionID, token, "&from=0")
	var got []byte
	for !bytes.Contains(got, []byte("b\n")) {
		kind, msg, err := raw.ReadMessage()
		if err != nil {
			t.Fatalf("raw attach: %v", err)
		}
		if kind == websocket.BinaryMessage {
			got = append(got, msg...)
		}
	}
	raw.Close()
	if string(got) != "a\nb\n" {
		t.Fatalf("stream = %q, want the command's exact bytes a\\nb\\n", got)
	}

	// The picture face: the snapshot has b under a, and live output arrives
	// with the \r a terminal needs.
	pic := attachSession(t, srv, tabID, sessionID, token, "")
	defer pic.Close()
	var snapshot, live []byte
	for {
		kind, msg, err := pic.ReadMessage()
		if err != nil {
			t.Fatalf("picture attach: %v", err)
		}
		if kind == websocket.BinaryMessage {
			live = append(live, msg...)
			continue
		}
		var ctl struct {
			Type string `json:"type"`
			Data string `json:"data"`
		}
		json.Unmarshal(msg, &ctl)
		if ctl.Type == "scrollback" {
			snapshot, _ = base64.StdEncoding.DecodeString(ctl.Data)
			break
		}
	}
	if !bytes.Contains(snapshot, []byte("a\r\nb")) {
		t.Fatalf("snapshot = %q, want b rendered at the start of the line under a", snapshot)
	}
	pic.WriteMessage(websocket.BinaryMessage, []byte("\n"))
	for !bytes.Contains(live, []byte("d\r\n")) {
		kind, msg, err := pic.ReadMessage()
		if err != nil {
			t.Fatalf("picture live: %v", err)
		}
		if kind == websocket.BinaryMessage {
			live = append(live, msg...)
		}
	}
	if !bytes.Contains(live, []byte("c\r\nd\r\n")) {
		t.Fatalf("live picture bytes = %q, want c\\r\\nd\\r\\n", live)
	}
}
