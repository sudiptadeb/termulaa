package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestNewInstanceID(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := newInstanceID()
		if len(id) != 32 {
			t.Fatalf("instance id %q: want 32 hex chars", id)
		}
		if strings.Trim(id, "0123456789abcdef") != "" {
			t.Fatalf("instance id %q: want lowercase hex", id)
		}
		if seen[id] {
			t.Fatalf("instance id %q repeated", id)
		}
		seen[id] = true
	}
}

// The handshake must carry the per-process instance id on every dial, and the
// takeover marker only when explicitly requested.
func TestDialQueryCarriesInstance(t *testing.T) {
	for _, takeover := range []bool{false, true} {
		var gotInstance, gotTakeover string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotInstance = r.URL.Query().Get("instance")
			gotTakeover = r.URL.Query().Get("takeover")
			http.Error(w, "nope", http.StatusInternalServerError)
		}))
		_ = dialAndServeTunnel(context.Background(), tunnelParams{
			dialer: rcDialer(false), server: srv.URL, token: "tok", label: "x",
			port: "17380", target: "127.0.0.1:1", instance: "abc123", takeover: takeover,
			status: &poolStatus{total: 1},
		})
		srv.Close()
		if gotInstance != "abc123" {
			t.Errorf("takeover=%v: instance param = %q, want abc123", takeover, gotInstance)
		}
		want := ""
		if takeover {
			want = "1"
		}
		if gotTakeover != want {
			t.Errorf("takeover=%v: takeover param = %q, want %q", takeover, gotTakeover, want)
		}
	}
}

func TestDialAndServeTunnelConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":"conflict","label":"handyman","tunnels":4,"connected_at":"2026-01-01T00:00:00Z","connected_secs":720}`))
	}))
	defer srv.Close()

	err := dialAndServeTunnel(context.Background(), tunnelParams{
		dialer: rcDialer(false), server: srv.URL, token: "tok", label: "x",
		port: "17380", target: "127.0.0.1:1", instance: "abc", status: &poolStatus{total: 1},
	})
	var ce *conflictError
	if !errors.As(err, &ce) {
		t.Fatalf("got %v, want conflictError", err)
	}
	if ce.label != "handyman" || ce.tunnels != 4 || ce.age != 12*time.Minute {
		t.Errorf("conflictError = %+v, want handyman/4/12m", ce)
	}
	if msg := ce.Error(); !strings.Contains(msg, `"handyman" (4 tunnels, up 12m)`) {
		t.Errorf("conflict message %q lacks incumbent details", msg)
	}
}

// A superseded close (1008 + "superseded") must surface as errSuperseded from
// serveTunnel — not as the generic failure that would be retried.
func TestServeTunnelSuperseded(t *testing.T) {
	agentWS, serverWS := wsPair(t)
	done := make(chan error, 1)
	go func() { done <- serveTunnel(context.Background(), agentWS, "127.0.0.1:1") }()

	time.Sleep(100 * time.Millisecond) // let smux settle
	deadline := time.Now().Add(time.Second)
	if err := serverWS.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.ClosePolicyViolation, supersededCloseReason), deadline); err != nil {
		t.Fatalf("send close frame: %v", err)
	}

	select {
	case err := <-done:
		if !errors.Is(err, errSuperseded) {
			t.Fatalf("serveTunnel returned %v, want errSuperseded", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveTunnel did not return after the superseded close frame")
	}
}

// A generic close must NOT be mistaken for superseded (that path retries).
func TestServeTunnelGenericCloseIsNotSuperseded(t *testing.T) {
	agentWS, serverWS := wsPair(t)
	done := make(chan error, 1)
	go func() { done <- serveTunnel(context.Background(), agentWS, "127.0.0.1:1") }()

	time.Sleep(100 * time.Millisecond)
	serverWS.UnderlyingConn().Close()

	select {
	case err := <-done:
		if errors.Is(err, errSuperseded) {
			t.Fatal("a hard carrier drop was reported as superseded")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveTunnel did not return after carrier drop")
	}
}

// On a superseded close the whole agent stops: runTunnel fails the shared
// context with an actionable cause and never dials again.
func TestRunTunnelStopsOnSuperseded(t *testing.T) {
	var dials atomic.Int32
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dials.Add(1)
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
		ws.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, supersededCloseReason),
			time.Now().Add(time.Second))
	}))
	defer srv.Close()

	ctx, fail := context.WithCancelCause(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runTunnel(ctx, fail, tunnelParams{
			dialer: rcDialer(false), server: srv.URL, token: "tok", label: "x",
			port: "17380", target: "127.0.0.1:1", instance: "abc", status: &poolStatus{total: 1},
		})
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("runTunnel kept retrying after a superseded close")
	}
	cause := context.Cause(ctx)
	if cause == nil || !strings.Contains(cause.Error(), "took over") {
		t.Errorf("fail cause = %v, want the took-over message", cause)
	}
	if got := dials.Load(); got != 1 {
		t.Errorf("agent dialed %d times after superseded, want exactly 1 (no retry)", got)
	}
}

// A 409 conflict is terminal on the very first dial: the incumbent is left
// alone and this process stops with an actionable message.
func TestRunTunnelStopsOnConflict(t *testing.T) {
	var dials atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dials.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":"conflict","label":"handyman","tunnels":4,"connected_secs":60}`))
	}))
	defer srv.Close()

	ctx, fail := context.WithCancelCause(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runTunnel(ctx, fail, tunnelParams{
			dialer: rcDialer(false), server: srv.URL, token: "tok", label: "x",
			port: "17380", target: "127.0.0.1:1", instance: "abc", status: &poolStatus{total: 1},
		})
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("runTunnel kept retrying after a 409 conflict")
	}
	cause := context.Cause(ctx)
	if cause == nil || !strings.Contains(cause.Error(), "already connected with this token") ||
		!strings.Contains(cause.Error(), "-rc-takeover") {
		t.Errorf("fail cause = %v, want conflict guidance mentioning -rc-takeover", cause)
	}
	if got := dials.Load(); got != 1 {
		t.Errorf("agent dialed %d times after conflict, want exactly 1 (incumbent untouched)", got)
	}
}

func TestFormatAge(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{42 * time.Second, "42s"},
		{12 * time.Minute, "12m"},
		{3*time.Hour + 5*time.Minute, "3h05m"},
	}
	for _, tt := range tests {
		if got := formatAge(tt.in); got != tt.want {
			t.Errorf("formatAge(%s) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
