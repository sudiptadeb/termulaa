package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

// generateID creates a random hex ID (16 bytes = 32 chars).
func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// RingBuffer — fixed-size circular byte buffer for scrollback storage
// ---------------------------------------------------------------------------

type RingBuffer struct {
	buf  []byte
	pos  int
	full bool
	mu   sync.Mutex
}

func NewRingBuffer(size int) *RingBuffer {
	return &RingBuffer{buf: make([]byte, size)}
}

// Write appends data circularly. Implements io.Writer.
func (r *RingBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	n := len(p)
	bufLen := len(r.buf)

	// If incoming data is larger than the buffer, only keep the tail.
	if n >= bufLen {
		copy(r.buf, p[n-bufLen:])
		r.pos = 0
		r.full = true
		return n, nil
	}

	// How much fits before wrap?
	space := bufLen - r.pos
	if n <= space {
		// Fits without wrapping.
		copy(r.buf[r.pos:], p)
	} else {
		// Wraps around: fill to end, then continue from start.
		copy(r.buf[r.pos:], p[:space])
		copy(r.buf, p[space:])
		r.full = true
	}

	r.pos = (r.pos + n) % bufLen
	return n, nil
}

// ReadAll returns data from oldest to newest.
func (r *RingBuffer) ReadAll() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.full {
		out := make([]byte, r.pos)
		copy(out, r.buf[:r.pos])
		return out
	}

	// Buffer is full: data from pos..end, then 0..pos
	out := make([]byte, len(r.buf))
	n := copy(out, r.buf[r.pos:])
	copy(out[n:], r.buf[:r.pos])
	return out
}

// Len returns the current amount of data in the buffer.
func (r *RingBuffer) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.full {
		return len(r.buf)
	}
	return r.pos
}

// SaveTo dumps the ring buffer contents to a file.
func (r *RingBuffer) SaveTo(path string) error {
	data := r.ReadAll()
	return os.WriteFile(path, data, 0600)
}

// LoadFrom reads a file into the ring buffer, replacing any existing content.
func (r *RingBuffer) LoadFrom(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	bufLen := len(r.buf)
	if len(data) >= bufLen {
		// Only keep the last bufLen bytes.
		copy(r.buf, data[len(data)-bufLen:])
		r.pos = 0
		r.full = true
	} else {
		copy(r.buf[:len(data)], data)
		r.pos = len(data)
		r.full = false
	}
	return nil
}

// ---------------------------------------------------------------------------
// wsClient — an attached WebSocket connection
// ---------------------------------------------------------------------------

type wsClient struct {
	conn *websocket.Conn
	send chan []byte
	done chan struct{}
	mode string // "owner" or "watch" — watch clients cannot send input
}

// ---------------------------------------------------------------------------
// SessionInfo / SessionMeta — JSON-safe snapshots
// ---------------------------------------------------------------------------

// SessionInfo is the JSON-safe snapshot for API responses.
type SessionInfo struct {
	ID         string    `json:"id"`
	Alive      bool      `json:"alive"`
	CWD        string    `json:"cwd"`
	Shell      string    `json:"shell"`
	Cols       int       `json:"cols"`
	Rows       int       `json:"rows"`
	ExitCode   int       `json:"exit_code"`
	CreatedAt  time.Time `json:"created_at"`
	LastActive time.Time `json:"last_active"`
}

// SessionMeta is the serialized metadata for state.json (no runtime fields).
type SessionMeta struct {
	ID         string    `json:"id"`
	CWD        string    `json:"cwd"`
	Shell      string    `json:"shell"`
	Cols       int       `json:"cols"`
	Rows       int       `json:"rows"`
	ExitCode   int       `json:"exit_code"`
	CreatedAt  time.Time `json:"created_at"`
	LastActive time.Time `json:"last_active"`
}

// ---------------------------------------------------------------------------
// Session — persistent PTY session, decoupled from WebSocket
// ---------------------------------------------------------------------------

type Session struct {
	ID         string
	CWD        string
	Shell      string
	Cols       int
	Rows       int
	ExitCode   int
	Alive      bool
	CreatedAt  time.Time
	LastActive time.Time

	cmd        *exec.Cmd
	pty        *os.File
	scrollback *RingBuffer

	mu      sync.Mutex
	clients []*wsClient
	done    chan struct{}
}

const (
	scrollbackSize = 256 * 1024 // 256 KB ring buffer
	ptyReadBuf     = 16384      // 16 KB read buffer
)

// NewSession spawns a new PTY session with the given parameters.
// histFile is the path to use for HISTFILE (shell history persistence).
func NewSession(id, shell, cwd string, cols, rows int, histFile string) (*Session, error) {
	// Login-shell convention: argv[0] = "-bash" so the shell reads all startup files.
	cmd := exec.Command(shell)
	cmd.Args = []string{"-" + filepath.Base(shell)}
	cmd.Dir = cwd

	// Inherit full environment, override TERM and HISTFILE.
	env := os.Environ()
	hasTerm := false
	hasHist := false
	for i, e := range env {
		if strings.HasPrefix(e, "TERM=") {
			env[i] = "TERM=xterm-256color"
			hasTerm = true
		}
		if strings.HasPrefix(e, "HISTFILE=") {
			env[i] = "HISTFILE=" + histFile
			hasHist = true
		}
	}
	if !hasTerm {
		env = append(env, "TERM=xterm-256color")
	}
	if !hasHist && histFile != "" {
		env = append(env, "HISTFILE="+histFile)
	}
	cmd.Env = env

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to start pty: %w", err)
	}

	// Set initial window size.
	pty.Setsize(ptmx, &pty.Winsize{
		Cols: uint16(cols),
		Rows: uint16(rows),
	})

	s := &Session{
		ID:         id,
		CWD:        cwd,
		Shell:      shell,
		Cols:       cols,
		Rows:       rows,
		Alive:      true,
		CreatedAt:  time.Now(),
		LastActive: time.Now(),

		cmd:        cmd,
		pty:        ptmx,
		scrollback: NewRingBuffer(scrollbackSize),
		clients:    make([]*wsClient, 0),
		done:       make(chan struct{}),
	}

	go s.readPump()
	return s, nil
}

// readPump continuously reads PTY output, writes to scrollback, and broadcasts
// to all attached clients. Runs until the PTY returns an error (EOF on exit).
func (s *Session) readPump() {
	buf := make([]byte, ptyReadBuf)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])

			s.scrollback.Write(data)
			s.Broadcast(data)
			s.LastActive = time.Now()
		}
		if err != nil {
			break
		}
	}

	// Shell exited — collect exit code.
	exitCode := 0
	if err := s.cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	s.mu.Lock()
	s.ExitCode = exitCode
	s.Alive = false
	s.mu.Unlock()

	// Signal all writer goroutines — they handle sending the exit JSON
	// as a TextMessage (not BinaryMessage) so xterm.js doesn't render it
	// as raw terminal output.
	close(s.done)
	log.Printf("Session %s: shell exited with code %d", s.ID, exitCode)
}

// AttachClient adds a WebSocket client to this session's broadcast list.
// If replayScrollback is true, the full scrollback buffer is sent first so
// the client sees all previous terminal output (e.g., after a tab reopen).
func (s *Session) AttachClient(c *wsClient, replayScrollback bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if replayScrollback {
		data := s.scrollback.ReadAll()
		if len(data) > 0 {
			msg, _ := json.Marshal(map[string]string{
				"type": "scrollback",
				"data": base64.StdEncoding.EncodeToString(data),
			})
			c.conn.WriteMessage(websocket.TextMessage, msg)
		}
	}

	s.clients = append(s.clients, c)
}

// DetachClient removes a WebSocket client from this session.
// The PTY is NOT closed — the session keeps running headless.
func (s *Session) DetachClient(c *wsClient) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, cl := range s.clients {
		if cl == c {
			s.clients = append(s.clients[:i], s.clients[i+1:]...)
			break
		}
	}
}

// Broadcast sends binary data to all attached clients' send channels.
// Non-blocking: if a client's channel is full, it is skipped.
func (s *Session) Broadcast(data []byte) {
	s.mu.Lock()
	snapshot := make([]*wsClient, len(s.clients))
	copy(snapshot, s.clients)
	s.mu.Unlock()

	for _, c := range snapshot {
		select {
		case c.send <- data:
		default:
			// Client is slow — drop this frame to avoid blocking the PTY reader.
		}
	}
}

// WriteInput writes user input bytes to the PTY.
func (s *Session) WriteInput(data []byte) {
	if s.pty != nil {
		s.pty.Write(data)
		s.LastActive = time.Now()
	}
}

// Resize updates the PTY window size.
func (s *Session) Resize(cols, rows int) {
	if s.pty != nil {
		pty.Setsize(s.pty, &pty.Winsize{
			Cols: uint16(cols),
			Rows: uint16(rows),
		})
	}
	s.mu.Lock()
	s.Cols = cols
	s.Rows = rows
	s.mu.Unlock()
}

// HandleFileDrop decodes a base64-encoded file and saves it to /tmp.
// Returns the file path on success.
func (s *Session) HandleFileDrop(name, dataB64 string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(dataB64)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}

	// Sanitize filename — keep only the base name.
	name = filepath.Base(name)
	if name == "" || name == "." || name == ".." {
		name = "clipboard"
	}

	// Ensure unique filename with timestamp.
	ext := filepath.Ext(name)
	baseName := strings.TrimSuffix(name, ext)
	if ext == "" {
		ext = ".png" // default for clipboard images
	}
	filename := fmt.Sprintf("%s-%d%s", baseName, time.Now().UnixMilli(), ext)
	path := filepath.Join(os.TempDir(), filename)

	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}

	log.Printf("Session %s: file saved: %s (%d bytes)", s.ID, path, len(data))
	return path, nil
}

// Kill sends SIGTERM, waits up to 2 seconds, then SIGKILL. Closes the PTY.
func (s *Session) Kill() {
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}

	s.cmd.Process.Signal(syscall.SIGTERM)

	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()

	select {
	case <-s.done:
		// Process already exited via readPump.
	case <-timer.C:
		s.cmd.Process.Kill()
	}

	if s.pty != nil {
		s.pty.Close()
	}

	s.mu.Lock()
	s.Alive = false
	s.mu.Unlock()
}

// Info returns a JSON-safe snapshot of the session state.
func (s *Session) Info() SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SessionInfo{
		ID:         s.ID,
		Alive:      s.Alive,
		CWD:        s.CWD,
		Shell:      s.Shell,
		Cols:       s.Cols,
		Rows:       s.Rows,
		ExitCode:   s.ExitCode,
		CreatedAt:  s.CreatedAt,
		LastActive: s.LastActive,
	}
}

// Meta returns a serializable metadata snapshot for state persistence.
func (s *Session) Meta() SessionMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SessionMeta{
		ID:         s.ID,
		CWD:        s.CWD,
		Shell:      s.Shell,
		Cols:       s.Cols,
		Rows:       s.Rows,
		ExitCode:   s.ExitCode,
		CreatedAt:  s.CreatedAt,
		LastActive: s.LastActive,
	}
}

// Done returns a channel that closes when the session's shell exits.
func (s *Session) Done() <-chan struct{} {
	return s.done
}

// ---------------------------------------------------------------------------
// findShell — locate the best available shell binary
// ---------------------------------------------------------------------------

func findShell() string {
	// Prefer Homebrew bash (5.x) over macOS system bash (3.2).
	candidates := []string{"/opt/homebrew/bin/bash", "/usr/local/bin/bash", "/bin/bash", "/usr/bin/bash"}
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	if path, err := exec.LookPath("bash"); err == nil {
		return path
	}
	return "/bin/sh"
}
