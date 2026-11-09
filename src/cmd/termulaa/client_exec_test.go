package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sudiptadeb/termulaa/src/internal/termclient"
)

// fakeRendezvous stands in for memd: it signs an app token in for a cookie,
// lists one machine, and proxies /rc/t/<id>/ to the real termulaa handlers
// with the prefix stripped, exactly as the path-mode rendezvous does.
func fakeRendezvous(t *testing.T) *httptest.Server {
	t.Helper()
	termulaa := httptest.NewServer(newTestMux(t))
	t.Cleanup(termulaa.Close)
	target, _ := url.Parse(termulaa.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	const agent = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const prefix = "/rc/t/" + agent + "/"

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/app/session", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mat_test" {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "memd_session", Value: "ok", Path: "/"})
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"user":{}}`))
	})
	signedIn := func(r *http.Request) bool {
		c, err := r.Cookie("memd_session")
		return err == nil && c.Value == "ok"
	}
	mux.HandleFunc("GET /rc/api/agents", func(w http.ResponseWriter, r *http.Request) {
		if !signedIn(r) {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"agents": []map[string]interface{}{{"id": agent[:8], "label": "box", "port": 1, "tunnels": 1, "connected_at": time.Now().UTC().Format(time.RFC3339), "url": prefix}},
		})
	})
	mux.HandleFunc(prefix, func(w http.ResponseWriter, r *http.Request) {
		if !signedIn(r) {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
		r.URL.Path = "/" + strings.TrimPrefix(r.URL.Path, prefix)
		r.Host = target.Host
		proxy.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestClientExecStreamsOutputAndExitCode(t *testing.T) {
	srv := fakeRendezvous(t)
	creds := termclient.Credentials{Server: srv.URL, Token: "mat_test"}

	var out bytes.Buffer
	res, err := termclient.Exec(context.Background(), creds, termclient.ExecOptions{
		Machine: "box",
		Command: "echo hello; echo oops >&2; exit 7",
		Cols:    80, Rows: 24,
		Output: &out,
		Status: io.Discard,
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.Code != 7 || res.TabID != "" {
		t.Fatalf("result = %+v, want exit 7 and no tab left", res)
	}
	if got := out.String(); !strings.Contains(got, "hello\r\n") || !strings.Contains(got, "oops\r\n") {
		t.Fatalf("output = %q, want both streams as the PTY shows them", got)
	}

	_, err = termclient.Exec(context.Background(), creds, termclient.ExecOptions{Machine: "nosuch", Command: "true", Output: io.Discard})
	var off *termclient.OfflineError
	if !errors.As(err, &off) || len(off.Connected) != 1 || off.Connected[0] != "box" {
		t.Fatalf("unknown machine error = %v, want OfflineError naming box", err)
	}
}

func TestClientShellBridgesKeyboardAndExitCode(t *testing.T) {
	srv := fakeRendezvous(t)
	creds := termclient.Credentials{Server: srv.URL, Token: "mat_test"}

	keys, typer := io.Pipe()
	var screen bytes.Buffer
	go func() {
		time.Sleep(time.Second) // let the login shell print its prompt
		typer.Write([]byte("echo shell-$((40+2))\nexit 5\n"))
	}()
	resized := make(chan struct{})
	res, err := termclient.Shell(context.Background(), creds, termclient.ShellOptions{
		Machine: "box",
		In:      keys,
		Out:     &screen,
		Status:  io.Discard,
		Size:    func() (int, int) { return 80, 24 },
		Resized: resized,
	})
	if err != nil {
		t.Fatalf("Shell: %v", err)
	}
	if res.Code != 5 || res.TabID != "" {
		t.Fatalf("result = %+v, want exit 5 and no tab left", res)
	}
	if !strings.Contains(screen.String(), "shell-42") {
		t.Fatalf("screen = %q, want the typed command's output", screen.String())
	}
}

func TestClientSignInRejectedIsAuthError(t *testing.T) {
	srv := fakeRendezvous(t)
	_, err := termclient.SignIn(context.Background(), termclient.Credentials{Server: srv.URL, Token: "mat_wrong"})
	var ae *termclient.AuthError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v, want AuthError", err)
	}
}
