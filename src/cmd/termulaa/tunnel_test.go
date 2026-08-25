package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xtaci/smux"
)

func TestWSURL(t *testing.T) {
	tests := []struct {
		name    string
		server  string
		path    string
		q       url.Values
		want    string
		wantErr bool
	}{
		{
			name:   "https becomes wss",
			server: "https://memd.example.com",
			path:   "/rc/tunnel",
			want:   "wss://memd.example.com/rc/tunnel",
		},
		{
			name:   "http becomes ws",
			server: "http://127.0.0.1:8080",
			path:   "/rc/tunnel",
			want:   "ws://127.0.0.1:8080/rc/tunnel",
		},
		{
			name:   "trailing slash stripped",
			server: "https://memd.example.com/",
			path:   "/rc/tunnel",
			want:   "wss://memd.example.com/rc/tunnel",
		},
		{
			name:   "base path joined",
			server: "https://example.com/memd/",
			path:   "/rc/tunnel",
			want:   "wss://example.com/memd/rc/tunnel",
		},
		{
			name:   "wss passthrough",
			server: "wss://memd.example.com",
			path:   "/rc/tunnel",
			want:   "wss://memd.example.com/rc/tunnel",
		},
		{
			name:   "ws passthrough",
			server: "ws://memd.example.com",
			path:   "/rc/tunnel",
			want:   "ws://memd.example.com/rc/tunnel",
		},
		{
			name:   "query params encoded",
			server: "https://memd.example.com",
			path:   "/rc/tunnel",
			q:      url.Values{"agent": {"my host"}, "session": {"0"}, "port": {"17380"}},
			want:   "wss://memd.example.com/rc/tunnel?agent=my+host&port=17380&session=0",
		},
		{
			name:    "bad scheme rejected",
			server:  "ftp://memd.example.com",
			path:    "/rc/tunnel",
			wantErr: true,
		},
		{
			name:    "missing scheme rejected",
			server:  "memd.example.com",
			path:    "/rc/tunnel",
			wantErr: true,
		},
		{
			name:    "garbage rejected",
			server:  "://nope",
			path:    "/rc/tunnel",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := wsURL(tt.server, tt.path, tt.q)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("wsURL(%q) = %q, want error", tt.server, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("wsURL(%q): %v", tt.server, err)
			}
			if got != tt.want {
				t.Fatalf("wsURL(%q) = %q, want %q", tt.server, got, tt.want)
			}
		})
	}
}

func TestRCStateRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if got := loadRCState(); got != (rcState{}) {
		t.Fatalf("loadRCState with no file = %+v, want zero value", got)
	}

	want := rcState{Server: "https://memd.example.com", Token: "v1.abc.def", Label: "Ai test", Insecure: true}
	if err := saveRCState(want); err != nil {
		t.Fatalf("saveRCState: %v", err)
	}
	if got := loadRCState(); got != want {
		t.Fatalf("loadRCState = %+v, want %+v", got, want)
	}
}

func TestRCStateLabelBackwardCompat(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// rc.json written before the label existed must still load, with Label empty.
	if err := saveRCState(rcState{Server: "https://memd.example.com", Token: "v1.abc.def"}); err != nil {
		t.Fatalf("saveRCState: %v", err)
	}
	got := loadRCState()
	if got.Label != "" {
		t.Fatalf("Label = %q, want empty", got.Label)
	}
	if got.Token != "v1.abc.def" {
		t.Fatalf("Token = %q, want round-tripped token", got.Token)
	}
}

func TestRunTunnelAgentValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := runTunnelAgent(TunnelConfig{Tunnels: 9, Target: "127.0.0.1:1"}); err == nil ||
		!strings.Contains(err.Error(), "between 1 and 8") {
		t.Fatalf("Tunnels=9: got %v, want pool size error", err)
	}
	if err := runTunnelAgent(TunnelConfig{Tunnels: 1, Target: "nonsense"}); err == nil ||
		!strings.Contains(err.Error(), "invalid target") {
		t.Fatalf("bad target: got %v, want target error", err)
	}
	if err := runTunnelAgent(TunnelConfig{Tunnels: 1, Target: "127.0.0.1:1", Server: "ftp://x"}); err == nil ||
		!strings.Contains(err.Error(), "must be http(s)") {
		t.Fatalf("bad server: got %v, want scheme error", err)
	}
}

func TestNextBackoff(t *testing.T) {
	tests := []struct{ in, want time.Duration }{
		{time.Second, 2 * time.Second},
		{4 * time.Second, 8 * time.Second},
		{16 * time.Second, 30 * time.Second},
		{30 * time.Second, 30 * time.Second},
	}
	for _, tt := range tests {
		if got := nextBackoff(tt.in); got != tt.want {
			t.Errorf("nextBackoff(%s) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

func TestJitterBounds(t *testing.T) {
	d := 10 * time.Second
	for i := 0; i < 1000; i++ {
		j := jitter(d)
		if j < d/2 || j > 3*d/2 {
			t.Fatalf("jitter(%s) = %s, want within [%s, %s]", d, j, d/2, 3*d/2)
		}
	}
}

func wsPair(t *testing.T) (agentSide, serverSide *websocket.Conn) {
	t.Helper()
	upgrader := websocket.Upgrader{}
	serverCh := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		serverCh <- ws
	}))
	t.Cleanup(srv.Close)

	u, err := wsURL(srv.URL, tunnelPath, nil)
	if err != nil {
		t.Fatalf("wsURL: %v", err)
	}
	ws, _, err := rcDialer(false).Dial(u, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { ws.Close() })

	select {
	case s := <-serverCh:
		t.Cleanup(func() { s.Close() })
		return ws, s
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for server-side websocket")
		return nil, nil
	}
}

func TestWSConnPartialReads(t *testing.T) {
	agentWS, serverWS := wsPair(t)
	conn := newWSConn(agentWS)

	if err := serverWS.WriteMessage(websocket.BinaryMessage, nil); err != nil {
		t.Fatalf("write empty: %v", err)
	}
	if err := serverWS.WriteMessage(websocket.BinaryMessage, []byte("hello, tunnel")); err != nil {
		t.Fatalf("write: %v", err)
	}

	var got []byte
	buf := make([]byte, 5)
	for len(got) < len("hello, tunnel") {
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if n == 0 {
			t.Fatal("Read returned 0 bytes with nil error")
		}
		got = append(got, buf[:n]...)
	}
	if string(got) != "hello, tunnel" {
		t.Fatalf("read %q, want %q", got, "hello, tunnel")
	}

	if _, err := conn.Write([]byte("pong")); err != nil {
		t.Fatalf("write: %v", err)
	}
	mt, data, err := serverWS.ReadMessage()
	if err != nil || mt != websocket.BinaryMessage || string(data) != "pong" {
		t.Fatalf("server read = (%d, %q, %v), want binary %q", mt, data, err, "pong")
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestDialAndServeTunnelAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	err := dialAndServeTunnel(context.Background(), tunnelParams{
		dialer: rcDialer(false),
		server: srv.URL,
		token:  "expired",
		label:  "test",
		port:   "17380",
		target: "127.0.0.1:1",
		status: &poolStatus{total: 1},
	})
	var ae *authError
	if !errors.As(err, &ae) {
		t.Fatalf("got %v, want authError", err)
	}
}

// TestTunnelE2ESplice proves the full path: wsConn adapter + smux config +
// accept loop + splice, against an in-process rendezvous and echo target.
func TestTunnelE2ESplice(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(c)
		}
	}()

	agentWS, serverWS := wsPair(t)

	sess, err := smux.Client(newWSConn(serverWS), muxConfig())
	if err != nil {
		t.Fatalf("smux client: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tunnelDone := make(chan error, 1)
	go func() { tunnelDone <- serveTunnel(ctx, agentWS, ln.Addr().String()) }()

	payload := make([]byte, 100*1024)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("rand: %v", err)
	}

	const streams = 3
	errCh := make(chan error, streams)
	for i := 0; i < streams; i++ {
		go func() {
			stream, err := sess.OpenStream()
			if err != nil {
				errCh <- err
				return
			}
			defer stream.Close()
			go stream.Write(payload)
			got := make([]byte, len(payload))
			if _, err := io.ReadFull(stream, got); err != nil {
				errCh <- err
				return
			}
			if !bytes.Equal(got, payload) {
				errCh <- errors.New("echoed bytes differ from sent bytes")
				return
			}
			errCh <- nil
		}()
	}
	for i := 0; i < streams; i++ {
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("stream %d: %v", i, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for stream echo")
		}
	}

	cancel()
	select {
	case <-tunnelDone:
	case <-time.After(5 * time.Second):
		t.Fatal("serveTunnel did not exit after context cancel")
	}
}
