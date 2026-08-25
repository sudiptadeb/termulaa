package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// A hard carrier failure must drop the tunnel promptly so the pool reconnects,
// rather than waiting out smux's 30s keepalive timeout. This holds because the
// accept loop blocks in AcceptStream, which returns on a socket read error;
// note that smux's IsClosed() does NOT flip there, so any future change to
// poll IsClosed() instead of blocking would reintroduce a 30s stall.
func TestServeTunnelReturnsPromptlyOnCarrierFailure(t *testing.T) {
	up := websocket.Upgrader{}
	conns := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conns <- c
		select {} // hold the handler; the test kills the conn underneath it
	}))
	defer srv.Close()

	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- serveTunnel(context.Background(), ws, "127.0.0.1:1") }()

	// Let the smux handshake settle, then sever the carrier hard.
	serverConn := <-conns
	time.Sleep(300 * time.Millisecond)
	serverConn.UnderlyingConn().(*net.TCPConn).SetLinger(0)
	serverConn.UnderlyingConn().Close()

	select {
	case <-done:
		// Returned promptly: the carrier error closed the session.
	case <-time.After(10 * time.Second):
		t.Fatal("serveTunnel did not return within 10s of a hard carrier failure; " +
			"it is waiting out smux's 30s keepalive timeout")
	}
}
