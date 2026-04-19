// Package main implements termulaa, a personal developer tool.
//
// SECURITY POSTURE — do not relax without explicit approval:
//
//   - Local-only, single-user. Runs on one developer's machine and is
//     never meant to ship to end users or run on a non-loopback
//     interface.
//   - HTTP listener binds to 127.0.0.1 ONLY (see main() below). Do not
//     change to 0.0.0.0 or any non-loopback address. The port must
//     never be reachable from LAN, VPN, or any remote host.
//   - No per-request auth, wildcard CORS, and CheckOrigin returns true
//     on the WebSocket upgrader (see handler.go). This is acceptable
//     *only* because the port is loopback-only — any webpage opened in
//     a browser on the same machine can call the API and attach to a
//     PTY. Known, accepted risk for a single-user dev tool.
//   - If this ever needs to run on a non-loopback interface, it MUST
//     first add real per-request auth (bearer secret or OS-local
//     credential), a locked-down CORS policy, and a strict origin check
//     on the WebSocket upgrader. Until then, relaxing the localhost
//     bind is a security incident.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var Version = "dev"

//go:embed ui
var uiFS embed.FS

// FullConfig holds all terminal-agent settings.
// Persisted to ~/.terminal-agent/config.json.
type FullConfig struct {
	Port             int    `json:"port"`
	Shell            string `json:"shell"`
	DeleteOnClose    bool   `json:"deleteOnClose"`
	CleanupAfterDays int    `json:"cleanupAfterDays"`
	ScrollbackLimit  string `json:"scrollbackLimit"`
	HistoryLimit     int    `json:"historyLimit"`
	DefaultCols      int    `json:"defaultCols"`
	DefaultRows      int    `json:"defaultRows"`
	CWDPollInterval  string `json:"cwdPollInterval"`
	SavePeriod       string `json:"savePeriod"`

	// Appearance (passed through to frontend)
	Theme      string `json:"theme,omitempty"`
	FontFamily string `json:"fontFamily,omitempty"`
	FontSize   int    `json:"fontSize,omitempty"`
}

func defaultFullConfig() *FullConfig {
	return &FullConfig{
		Port:             17380,
		Shell:            "",
		DeleteOnClose:    false,
		CleanupAfterDays: 30,
		ScrollbackLimit:  "1MB",
		HistoryLimit:     10000,
		DefaultCols:      120,
		DefaultRows:      30,
		CWDPollInterval:  "5s",
		SavePeriod:       "5s",
		Theme:            "dark",
		FontFamily:       "'JetBrains Mono', monospace",
		FontSize:         14,
	}
}

func configPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".terminal-agent", "config.json")
}

func loadFullConfig() *FullConfig {
	cfg := defaultFullConfig()
	data, err := os.ReadFile(configPath())
	if err != nil {
		return cfg
	}
	json.Unmarshal(data, cfg)
	// Fill any zero values with defaults
	def := defaultFullConfig()
	if cfg.Port == 0 {
		cfg.Port = def.Port
	}
	if cfg.CleanupAfterDays == 0 {
		cfg.CleanupAfterDays = def.CleanupAfterDays
	}
	if cfg.ScrollbackLimit == "" {
		cfg.ScrollbackLimit = def.ScrollbackLimit
	}
	if cfg.DefaultCols == 0 {
		cfg.DefaultCols = def.DefaultCols
	}
	if cfg.DefaultRows == 0 {
		cfg.DefaultRows = def.DefaultRows
	}
	if cfg.CWDPollInterval == "" {
		cfg.CWDPollInterval = def.CWDPollInterval
	}
	if cfg.SavePeriod == "" {
		cfg.SavePeriod = def.SavePeriod
	}
	if cfg.FontSize == 0 {
		cfg.FontSize = def.FontSize
	}
	return cfg
}

func saveFullConfig(cfg *FullConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	os.MkdirAll(filepath.Dir(configPath()), 0700)
	return os.WriteFile(configPath(), data, 0600)
}

func main() {
	portFlag := flag.Int("port", 0, "listen port (overrides config)")
	flag.Parse()

	log.SetFlags(log.Ltime | log.Lmicroseconds)

	// Load config
	fullCfg := loadFullConfig()
	if *portFlag != 0 {
		fullCfg.Port = *portFlag
	}

	// Create manager with minimal Config (what manager needs)
	mgrCfg := &Config{Port: fullCfg.Port}
	mgr := NewSessionManager(mgrCfg)
	mgr.LoadState()
	mgr.Start(context.Background())

	// Set up HTTP routes
	mux := http.NewServeMux()
	registerRoutes(mux, mgr, fullCfg)

	// Loopback-only bind. Non-negotiable — see the package doc at the top
	// of this file. Changing this is a security incident, not a config tweak.
	addr := fmt.Sprintf("127.0.0.1:%d", fullCfg.Port)
	srv := &http.Server{
		Addr:    addr,
		Handler: corsMiddleware(mux),
	}

	log.Printf("terminal-agent %s starting on http://%s", Version, addr)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	<-sigCh
	log.Println("Shutting down...")

	mgr.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("Shutdown error: %v", err)
	}
	log.Println("Server stopped")
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func parseDuration(s string, fallback time.Duration) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		return fallback
	}
	return d
}

func parseByteSize(s string) int {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0
	}
	upper := strings.ToUpper(s)
	var multiplier int
	var numStr string
	switch {
	case strings.HasSuffix(upper, "MB"):
		multiplier = 1024 * 1024
		numStr = s[:len(s)-2]
	case strings.HasSuffix(upper, "KB"):
		multiplier = 1024
		numStr = s[:len(s)-2]
	default:
		multiplier = 1
		numStr = s
	}
	var n int
	fmt.Sscanf(strings.TrimSpace(numStr), "%d", &n)
	if n <= 0 {
		return 1024 * 1024 // default 1MB
	}
	return n * multiplier
}
