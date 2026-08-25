package main

// Remote-control (rc) reverse tunnel agent — a dumb byte pump. It never
// starts the terminal server and never touches the loopback bind: it dials
// OUT to the memd rendezvous, holds a small pool of smux tunnels, and
// splices each incoming stream to the local termulaa on 127.0.0.1.

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xtaci/smux"
)

const (
	// defaultRCServer is the reference rendezvous. Any server implementing
	// docs/rc-protocol.md works: override with -rc-server (persisted to
	// ~/.termulaa/rc.json) to point at your own implementation.
	//
	// This is the rendezvous host (where /rc and /rc/tunnel live), NOT the
	// host a browser opens the terminal on. A rendezvous may serve the viewer
	// from a different hostname; the agent never needs to know it.
	defaultRCServer   = "https://memd.debkosh.com"
	defaultRCTunnels  = 4
	maxRCTunnels      = 8
	initialBackoff    = time.Second
	maxBackoff        = 30 * time.Second
	stableConnAge     = 30 * time.Second
	handshakeTimeout  = 15 * time.Second
	localDialTimeout  = 10 * time.Second
	tunnelPath        = "/rc/tunnel"
	pairPath          = "/rc"
	spliceCopyBufSize = 32 * 1024
)

// TunnelConfig is the runtime configuration for the rc agent.
type TunnelConfig struct {
	Server   string // rendezvous base URL, e.g. https://memd.debkosh.com
	Target   string // local termulaa address, e.g. 127.0.0.1:17380
	Token    string // tunnel token (overrides the saved one when set)
	Label    string // agent label shown on the rendezvous (default: hostname)
	Tunnels  int    // pool size: number of concurrent smux tunnels
	Insecure *bool  // skip TLS verification (dev only); nil = use saved value
}

// rcState is persisted to ~/.termulaa/rc.json.
type rcState struct {
	Server   string `json:"server"`
	Token    string `json:"token"`
	Insecure bool   `json:"insecure,omitempty"`
}

func rcStatePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".termulaa", "rc.json")
}

func loadRCState() rcState {
	var st rcState
	data, err := os.ReadFile(rcStatePath())
	if err != nil {
		return st
	}
	json.Unmarshal(data, &st)
	return st
}

func saveRCState(st rcState) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	os.MkdirAll(filepath.Dir(rcStatePath()), 0700)
	return os.WriteFile(rcStatePath(), data, 0600)
}

// muxConfig MUST stay byte-identical with the memd side; a mismatch causes
// silent stalls (see the tunnel contract).
func muxConfig() *smux.Config {
	c := smux.DefaultConfig()
	c.Version = 2
	c.KeepAliveInterval = 10 * time.Second
	c.KeepAliveTimeout = 30 * time.Second
	c.MaxFrameSize = 32 * 1024
	c.MaxReceiveBuffer = 4 * 1024 * 1024
	c.MaxStreamBuffer = 512 * 1024
	return c
}

// rcDialer is the single construction point for the WebSocket dialer; every
// dial path must use it so TLS settings can never diverge between paths.
func rcDialer(insecure bool) *websocket.Dialer {
	d := &websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: handshakeTimeout,
	}
	if insecure {
		d.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return d
}

// runTunnelAgent is the entry point for `termulaa -rc`. It blocks until the
// process is signalled or the token is rejected.
func runTunnelAgent(cfg TunnelConfig) error {
	st := loadRCState()
	if cfg.Server != "" {
		st.Server = cfg.Server
	}
	if cfg.Token != "" {
		st.Token = strings.TrimSpace(cfg.Token)
	}
	if cfg.Insecure != nil {
		st.Insecure = *cfg.Insecure
	}
	if st.Server == "" {
		st.Server = defaultRCServer
	}
	if cfg.Target == "" {
		cfg.Target = fmt.Sprintf("127.0.0.1:%d", loadFullConfig().Port)
	}
	if cfg.Label == "" {
		if hn, err := os.Hostname(); err == nil && hn != "" {
			cfg.Label = hn
		} else {
			cfg.Label = "termulaa"
		}
	}
	if cfg.Tunnels == 0 {
		cfg.Tunnels = defaultRCTunnels
	}
	if cfg.Tunnels < 1 || cfg.Tunnels > maxRCTunnels {
		return fmt.Errorf("-rc-tunnels must be between 1 and %d", maxRCTunnels)
	}

	_, port, err := net.SplitHostPort(cfg.Target)
	if err != nil {
		return fmt.Errorf("invalid target %q (want host:port): %w", cfg.Target, err)
	}
	if _, err := wsURL(st.Server, tunnelPath, nil); err != nil {
		return err
	}

	if strings.TrimSpace(st.Token) == "" {
		tok, err := promptForToken(st.Server)
		if err != nil {
			return err
		}
		st.Token = tok
	}
	if err := saveRCState(st); err != nil {
		log.Printf("rc: warning: could not save state: %v", err)
	}

	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, fail := context.WithCancelCause(sigCtx)
	defer fail(nil)

	if st.Insecure {
		log.Printf("rc: WARNING: TLS verification disabled (-rc-insecure); dev use only")
	}
	log.Printf("rc: agent %q starting; rendezvous=%s target=%s tunnels=%d",
		cfg.Label, st.Server, cfg.Target, cfg.Tunnels)

	dialer := rcDialer(st.Insecure)
	status := &poolStatus{total: cfg.Tunnels}

	var wg sync.WaitGroup
	for i := 0; i < cfg.Tunnels; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			runTunnel(ctx, fail, tunnelParams{
				dialer: dialer,
				server: st.Server,
				token:  st.Token,
				label:  cfg.Label,
				port:   port,
				target: cfg.Target,
				index:  idx,
				status: status,
			})
		}(i)
	}
	wg.Wait()

	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		return cause
	}
	log.Println("rc: shutting down")
	return nil
}

type tunnelParams struct {
	dialer *websocket.Dialer
	server string
	token  string
	label  string
	port   string
	target string
	index  int
	status *poolStatus
}

// poolStatus tracks live tunnels so logs always show pool health at a glance.
type poolStatus struct {
	mu    sync.Mutex
	live  int
	total int
}

func (p *poolStatus) up(idx int) {
	p.mu.Lock()
	p.live++
	live := p.live
	p.mu.Unlock()
	log.Printf("rc: tunnel %d connected (%d/%d tunnels up)", idx, live, p.total)
}

func (p *poolStatus) down(idx int, err error) {
	p.mu.Lock()
	p.live--
	live := p.live
	p.mu.Unlock()
	log.Printf("rc: tunnel %d lost: %v (%d/%d tunnels up)", idx, err, live, p.total)
}

// authError marks a terminal authentication failure (do not retry).
type authError struct{ status string }

func (e *authError) Error() string { return "rendezvous rejected the token: " + e.status }

// runTunnel keeps one pooled tunnel alive, reconnecting with jittered
// exponential backoff. An auth failure fails the whole agent via fail().
func runTunnel(ctx context.Context, fail context.CancelCauseFunc, p tunnelParams) {
	backoff := initialBackoff
	for ctx.Err() == nil {
		start := time.Now()
		err := dialAndServeTunnel(ctx, p)
		if ctx.Err() != nil {
			return
		}
		var ae *authError
		if errors.As(err, &ae) {
			fail(fmt.Errorf("%v\n     the token may be expired — mint a fresh one at %s%s",
				ae, strings.TrimRight(p.server, "/"), pairPath))
			return
		}
		if time.Since(start) >= stableConnAge {
			backoff = initialBackoff
		}
		delay := jitter(backoff)
		log.Printf("rc: tunnel %d: reconnecting in %s (%v)", p.index, delay.Round(time.Millisecond), err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		backoff = nextBackoff(backoff)
	}
}

func nextBackoff(d time.Duration) time.Duration {
	d *= 2
	if d > maxBackoff {
		d = maxBackoff
	}
	return d
}

// jitter spreads retries across the pool so tunnels never reconnect in
// lockstep after a rendezvous restart.
func jitter(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.5 + rand.Float64()))
}

func dialAndServeTunnel(ctx context.Context, p tunnelParams) error {
	u, err := wsURL(p.server, tunnelPath, url.Values{
		"agent":   {p.label},
		"session": {strconv.Itoa(p.index)},
		"port":    {p.port},
	})
	if err != nil {
		return err
	}
	header := http.Header{"Authorization": {"Bearer " + p.token}}
	ws, resp, err := p.dialer.DialContext(ctx, u, header)
	if err != nil {
		if resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
			return &authError{status: resp.Status}
		}
		return err
	}
	p.status.up(p.index)
	err = serveTunnel(ctx, ws, p.target)
	p.status.down(p.index, err)
	return err
}

// serveTunnel runs one smux tunnel over an established WebSocket: accept
// streams, splice each to the local termulaa. Blocks until the tunnel dies.
func serveTunnel(ctx context.Context, ws *websocket.Conn, target string) error {
	conn := newWSConn(ws)
	defer conn.Close()

	sess, err := smux.Server(conn, muxConfig())
	if err != nil {
		return err
	}
	defer sess.Close()

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			sess.Close()
		case <-done:
		}
	}()

	for {
		stream, err := sess.AcceptStream()
		if err != nil {
			return err
		}
		go serveStream(stream, target)
	}
}

func serveStream(stream net.Conn, target string) {
	defer stream.Close()
	local, err := net.DialTimeout("tcp", target, localDialTimeout)
	if err != nil {
		log.Printf("rc: local dial %s failed: %v", target, err)
		return
	}
	splice(stream, local)
}

// splice copies bytes both ways until either side closes, then closes both.
func splice(a, b io.ReadWriteCloser) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		copyBytes(a, b)
		a.Close()
		b.Close()
	}()
	copyBytes(b, a)
	a.Close()
	b.Close()
	<-done
}

func copyBytes(dst io.Writer, src io.Reader) {
	buf := make([]byte, spliceCopyBufSize)
	io.CopyBuffer(dst, src, buf)
}

// promptForToken tells the user where to mint a token and reads it from stdin.
func promptForToken(server string) (string, error) {
	base := strings.TrimRight(server, "/")
	fmt.Fprintf(os.Stderr, "\ntermulaa remote control\n")
	fmt.Fprintf(os.Stderr, "  1. Open %s%s in your browser and sign in.\n", base, pairPath)
	fmt.Fprintf(os.Stderr, "  2. Create a tunnel token and copy it.\n")
	fmt.Fprintf(os.Stderr, "  3. Paste it here.\n\n")
	fmt.Fprintf(os.Stderr, "Tunnel token: ")

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("reading token: %w", err)
	}
	tok := strings.TrimSpace(line)
	if tok == "" {
		return "", fmt.Errorf("no token entered")
	}
	return tok, nil
}

// wsURL derives a WebSocket URL from the rendezvous base URL: https -> wss,
// http -> ws, path appended to any base path.
func wsURL(server, path string, q url.Values) (string, error) {
	u, err := url.Parse(strings.TrimRight(server, "/"))
	if err != nil {
		return "", fmt.Errorf("invalid rendezvous url %q: %w", server, err)
	}
	switch u.Scheme {
	case "https", "wss":
		u.Scheme = "wss"
	case "http", "ws":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("rendezvous url must be http(s): %q", server)
	}
	if u.Host == "" {
		return "", fmt.Errorf("invalid rendezvous url %q: missing host", server)
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	if q != nil {
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}
