package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSanitizePrefix(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"single segment", "/term", "/term"},
		{"multi segment", "/rc/t/abc123", "/rc/t/abc123"},
		{"agent id shaped", "/rc/t/" + strings.Repeat("a0", 32), "/rc/t/" + strings.Repeat("a0", 32)},
		{"dots inside segment", "/v1.2/app", "/v1.2/app"},
		{"tilde underscore dash", "/~u_x-y", "/~u_x-y"},
		{"bare slash", "/", ""},
		{"trailing slash", "/term/", ""},
		{"no leading slash", "term", ""},
		{"double slash", "//evil.com", ""},
		{"scheme and host", "https://evil.com", ""},
		{"protocol relative in segment", "/a//b", ""},
		{"dot segment", "/a/./b", ""},
		{"dotdot segment", "/a/../b", ""},
		{"leading dotdot", "/..", ""},
		{"triple dot segment", "/.../x", ""},
		{"html tag", "/a<script>", ""},
		{"quote breaks attribute", `/a"onload="x`, ""},
		{"space", "/a b", ""},
		{"percent encoding", "/a%2e%2e", ""},
		{"query", "/a?x=1", ""},
		{"backslash", `/a\b`, ""},
		{"over length", "/" + strings.Repeat("a", maxForwardedPrefixLen), ""},
		{"at max length", "/" + strings.Repeat("a", maxForwardedPrefixLen-1), "/" + strings.Repeat("a", maxForwardedPrefixLen-1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizePrefix(tc.in); got != tc.want {
				t.Errorf("sanitizePrefix(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func newTestMux(t *testing.T) *http.ServeMux {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cfg := defaultFullConfig()
	mgr := NewSessionManager(&Config{Port: cfg.Port})
	mux := http.NewServeMux()
	registerRoutes(mux, mgr, cfg)
	return mux
}

func TestUIPagesRenderBase(t *testing.T) {
	mux := newTestMux(t)
	pages := []struct {
		name string
		path string
	}{
		{"index", "/"},
		{"tab", "/tab/abc123"},
		{"settings", "/settings"},
	}
	cases := []struct {
		name     string
		header   string
		wantBase string
	}{
		{"no header", "", `<base href="/">`},
		{"valid prefix", "/rc/t/agent1", `<base href="/rc/t/agent1/">`},
		{"invalid prefix falls back to root", "/../evil", `<base href="/">`},
		{"injection attempt falls back to root", `/"><script>alert(1)</script>`, `<base href="/">`},
	}
	for _, page := range pages {
		for _, tc := range cases {
			t.Run(page.name+"/"+tc.name, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodGet, page.path, nil)
				if tc.header != "" {
					req.Header.Set("X-Forwarded-Prefix", tc.header)
				}
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("GET %s = %d", page.path, rec.Code)
				}
				body := rec.Body.String()
				if !strings.Contains(body, tc.wantBase) {
					t.Errorf("GET %s with prefix %q: body lacks %s", page.path, tc.header, tc.wantBase)
				}
				if n := strings.Count(body, "<base "); n != 1 {
					t.Errorf("GET %s: %d <base> tags, want exactly 1", page.path, n)
				}
				if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "base-uri 'self'") {
					t.Errorf("GET %s: CSP missing base-uri 'self': %q", page.path, csp)
				}
			})
		}
	}
}

// The pages must never contain root-absolute UI URLs: everything resolves
// against <base>, or routes would break when hosted under a path prefix.
func TestUIPagesUseRelativeURLs(t *testing.T) {
	mux := newTestMux(t)
	for _, path := range []string{"/", "/tab/abc123", "/settings"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		body := rec.Body.String()
		for _, bad := range []string{`src="/`, `href="/lib`, `href="/app`, `fetch('/`, `'/tab/`, `'/api/`, `'/ws/`} {
			if strings.Contains(body, bad) {
				t.Errorf("GET %s: page contains root-absolute URL fragment %q", path, bad)
			}
		}
	}
}

func TestTabPageRejectsInvalidID(t *testing.T) {
	mux := newTestMux(t)
	req := httptest.NewRequest(http.MethodGet, "/tab/bad!id", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("GET /tab/<invalid> = %d, want 400", rec.Code)
	}
}
