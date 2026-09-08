package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	data, _ := r.readAll()
	return data
}

// ReadReplay returns the buffer contents trimmed to a boundary a terminal
// can safely start parsing from. A full ring has overwritten its oldest
// bytes at an arbitrary offset, so the head may be the tail of an escape
// sequence or of a multi-byte UTF-8 character; replaying that verbatim makes
// the terminal swallow real text (a truncated CSI eats characters until its
// final byte). See cleanReplayStart.
func (r *RingBuffer) ReadReplay() []byte {
	data, wrapped := r.readAll()
	return cleanReplayStart(data, wrapped)
}

func (r *RingBuffer) readAll() ([]byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.full {
		out := make([]byte, r.pos)
		copy(out, r.buf[:r.pos])
		return out, false
	}

	// Buffer is full: data from pos..end, then 0..pos
	out := make([]byte, len(r.buf))
	n := copy(out, r.buf[r.pos:])
	copy(out[n:], r.buf[:r.pos])
	return out, true
}

// cleanReplayStart drops the unparseable head of a wrapped ring dump. It
// skips UTF-8 continuation bytes, then everything before the first ESC: an
// ESC always begins a fresh sequence (inside an OSC string it can only be the
// ST terminator, which a parser treats as a harmless no-op), so parsing from
// there is well-defined. Plain text with no ESC at all is cut at its first
// newline instead. An unwrapped buffer started at byte zero of the session
// and is returned unchanged.
func cleanReplayStart(data []byte, wrapped bool) []byte {
	if !wrapped || len(data) == 0 {
		return data
	}
	i := 0
	for i < len(data) && data[i]&0xC0 == 0x80 {
		i++
	}
	data = data[i:]
	if esc := bytes.IndexByte(data, 0x1b); esc >= 0 {
		return data[esc:]
	}
	if nl := bytes.IndexByte(data, '\n'); nl >= 0 {
		return data[nl+1:]
	}
	return data
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
	conn       *websocket.Conn
	done       chan struct{}
	tabID      string
	ownerToken string

	// Outbound queue. PTY output is never dropped for a slow client: bytes
	// are coalesced into a bounded backlog that writePump drains, and a
	// client that cannot keep up within maxClientBacklog is disconnected
	// so it reconnects and resyncs from a fresh replay. Dropping arbitrary
	// chunks instead would leave the terminal parsing a torn escape stream
	// until the next full redraw.
	outMu    sync.Mutex
	out      []outFrame
	outBytes int
	notify   chan struct{}
	overflow bool
}

// outFrame is one queued WebSocket message: binary PTY bytes or a JSON
// control message (text).
type outFrame struct {
	text bool
	data []byte
}

// maxClientBacklog caps the bytes queued for one client before it is
// declared too slow and disconnected.
const maxClientBacklog = 8 << 20 // 8 MiB

// maxCoalescedFrame bounds one coalesced binary WebSocket message so a burst
// still streams to the client in pieces instead of one giant frame.
const maxCoalescedFrame = 256 << 10 // 256 KiB

func newWSClient(conn *websocket.Conn, tabID, ownerToken string) *wsClient {
	return &wsClient{
		conn:       conn,
		done:       make(chan struct{}),
		tabID:      tabID,
		ownerToken: ownerToken,
		notify:     make(chan struct{}, 1),
	}
}

// queueBinary appends PTY output for the client, merging it into the last
// pending binary frame up to maxCoalescedFrame.
func (c *wsClient) queueBinary(data []byte) {
	c.outMu.Lock()
	defer c.outMu.Unlock()
	if c.overflow {
		return
	}
	if n := len(c.out); n > 0 && !c.out[n-1].text && len(c.out[n-1].data)+len(data) <= maxCoalescedFrame {
		c.out[n-1].data = append(c.out[n-1].data, data...)
	} else {
		c.out = append(c.out, outFrame{data: append([]byte(nil), data...)})
	}
	c.outBytes += len(data)
	if c.outBytes > maxClientBacklog {
		c.overflow = true
	}
	c.signal()
}

// queueText appends a JSON control message.
func (c *wsClient) queueText(msg []byte) {
	c.outMu.Lock()
	defer c.outMu.Unlock()
	if c.overflow {
		return
	}
	c.out = append(c.out, outFrame{text: true, data: msg})
	c.outBytes += len(msg)
	c.signal()
}

func (c *wsClient) signal() {
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

// takePending swaps out the queued frames. overflow reports that the
// backlog limit was exceeded; the caller must then close the connection
// after flushing what it has.
func (c *wsClient) takePending() (frames []outFrame, overflow bool) {
	c.outMu.Lock()
	defer c.outMu.Unlock()
	frames, c.out = c.out, nil
	c.outBytes = 0
	return frames, c.overflow
}

// writePump is the single writer for the connection (gorilla permits one
// concurrent writer). It runs until the client is done, the session exits,
// a write fails, or the client overflows its backlog.
func (c *wsClient) writePump(sess *Session) {
	for {
		select {
		case <-c.notify:
			frames, overflow := c.takePending()
			for _, f := range frames {
				msgType := websocket.BinaryMessage
				if f.text {
					msgType = websocket.TextMessage
				}
				if err := c.conn.WriteMessage(msgType, f.data); err != nil {
					return
				}
			}
			if overflow {
				log.Printf("session-ws: client too slow on session %s, disconnecting (backlog > %d bytes)", sess.ID, maxClientBacklog)
				c.conn.Close()
				return
			}
		case <-c.done:
			return
		case <-sess.Done():
			// Session died — flush what is queued, then notify the client.
			// The exit notice is a TextMessage so xterm.js never renders
			// it as terminal output.
			frames, _ := c.takePending()
			for _, f := range frames {
				if f.text {
					continue
				}
				if err := c.conn.WriteMessage(websocket.BinaryMessage, f.data); err != nil {
					return
				}
			}
			exitMsg, _ := json.Marshal(map[string]interface{}{
				"type": "exit",
				"code": sess.ExitCode,
			})
			c.conn.WriteMessage(websocket.TextMessage, exitMsg)
			return
		}
	}
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

	// lastOutput is the unix-nanosecond time of the most recent PTY output
	// byte (0 = never). Kept as an atomic so readPump can stamp it without
	// holding mu and API readers see a race-free value.
	lastOutput atomic.Int64

	// altScreen mirrors whether the application has switched to the
	// alternate screen (DECSET 1049/1047/47). readPump tracks it from the
	// output stream; altTail carries the last bytes of the previous chunk
	// so a sequence split across two PTY reads is still recognised.
	altScreen atomic.Bool
	altTail   []byte

	// outMu serialises "record output, then fan it out" against "snapshot
	// the record, then join the fan-out" so a joining client can neither
	// miss a chunk nor receive one twice.
	outMu sync.Mutex

	mu      sync.Mutex
	clients []*wsClient
	done    chan struct{}
}

const (
	defaultScrollbackBytes = 256 * 1024
	ptyReadBuf             = 16384
)

// NewSession spawns a new PTY session with the given parameters.
// histFile is the path to use for HISTFILE (shell history persistence).
func NewSession(id, shell, cwd string, cols, rows int, histFile string, historyLimit int, scrollbackBytes int) (*Session, error) {
	// Login-shell convention: argv[0] = "-bash" so the shell reads all startup files.
	cmd := exec.Command(shell)
	cmd.Args = []string{"-" + filepath.Base(shell)}
	cmd.Dir = cwd

	// Inherit full environment, override TERM and HISTFILE.
	env := os.Environ()
	hasTerm := false
	hasHist := false
	hasHistSize := false
	hasHistFileSize := false
	historyLimitValue := "0"
	if historyLimit > 0 {
		historyLimitValue = strconv.Itoa(historyLimit)
	}
	for i, e := range env {
		if strings.HasPrefix(e, "TERM=") {
			env[i] = "TERM=xterm-256color"
			hasTerm = true
		}
		if strings.HasPrefix(e, "HISTFILE=") {
			env[i] = "HISTFILE=" + histFile
			hasHist = true
		}
		if strings.HasPrefix(e, "HISTSIZE=") {
			env[i] = "HISTSIZE=" + historyLimitValue
			hasHistSize = true
		}
		if strings.HasPrefix(e, "HISTFILESIZE=") {
			env[i] = "HISTFILESIZE=" + historyLimitValue
			hasHistFileSize = true
		}
	}
	if !hasTerm {
		env = append(env, "TERM=xterm-256color")
	}
	if !hasHist && histFile != "" {
		env = append(env, "HISTFILE="+histFile)
	}
	if !hasHistSize {
		env = append(env, "HISTSIZE="+historyLimitValue)
	}
	if !hasHistFileSize {
		env = append(env, "HISTFILESIZE="+historyLimitValue)
	}
	cmd.Env = env

	if scrollbackBytes <= 0 {
		scrollbackBytes = defaultScrollbackBytes
	}

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
		scrollback: NewRingBuffer(scrollbackBytes),
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

			s.outMu.Lock()
			s.scrollback.Write(data)
			var alt bool
			s.altTail, alt = trackAltScreen(s.altTail, data, s.altScreen.Load())
			s.altScreen.Store(alt)
			s.Broadcast(data)
			s.outMu.Unlock()
			now := time.Now()
			s.LastActive = now
			s.lastOutput.Store(now.UnixNano())
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
//
// prefix is output the caller wants replayed before this session's own
// buffer (a revived session passes its predecessor's scrollback). When
// replayScrollback is true the ring buffer follows it. The replay and the
// join happen under outMu, so the client's view continues exactly where the
// replay ends: no chunk is lost between the snapshot and the first
// broadcast, and none is delivered twice.
//
// While the application holds the alternate screen the ring is not
// replayed at all: it is a history of full-screen frames drawn at earlier
// sizes, and stacking them produces the ghosted picture that a raw replay is
// known for. The client gets a clean terminal and the caller is told to
// nudge the application into a full redraw instead. The returned value
// reports whether anything was replayed.
func (s *Session) AttachClient(c *wsClient, prefix []byte, replayScrollback bool) (replayed bool) {
	s.outMu.Lock()
	defer s.outMu.Unlock()

	var data []byte
	if len(prefix) > 0 {
		data = append(data, prefix...)
	}
	if replayScrollback && !s.altScreen.Load() {
		data = append(data, s.scrollback.ReadReplay()...)
	}
	if len(data) > 0 {
		msg, _ := json.Marshal(map[string]string{
			"type": "scrollback",
			"data": base64.StdEncoding.EncodeToString(data),
		})
		c.queueText(msg)
		replayed = true
	}

	s.mu.Lock()
	s.clients = append(s.clients, c)
	s.mu.Unlock()
	return replayed
}

// InAltScreen reports whether the application currently holds the
// alternate screen, as tracked from its output.
func (s *Session) InAltScreen() bool { return s.altScreen.Load() }

// altTailKeep is how many trailing bytes readPump carries between chunks:
// enough for the longest prefix of a DECSET/DECRST that could be cut, e.g.
// "\x1b[?1049;2004" before its final byte.
const altTailKeep = 24

// trackAltScreen scans one chunk of terminal output for alternate-screen
// switches (CSI ? Pm h / l with 1049, 1047 or 47 among the parameters) and
// returns the updated state plus the bytes to carry into the next call.
// tail is the carry from the previous call; a sequence straddling the two
// chunks is matched on the joined bytes. Re-scanning the carried tail can
// only re-apply a switch that was already applied, which is idempotent.
func trackAltScreen(tail, data []byte, cur bool) (newTail []byte, alt bool) {
	alt = cur
	buf := data
	if len(tail) > 0 {
		buf = make([]byte, 0, len(tail)+len(data))
		buf = append(buf, tail...)
		buf = append(buf, data...)
	}
	for i := 0; i+2 < len(buf); {
		j := bytes.Index(buf[i:], []byte("\x1b[?"))
		if j < 0 {
			break
		}
		p := i + j + 3
		start := p
		for p < len(buf) && (buf[p] >= '0' && buf[p] <= '9' || buf[p] == ';') {
			p++
		}
		if p >= len(buf) {
			break // cut mid-sequence; the tail carry finishes it next time
		}
		if buf[p] == 'h' || buf[p] == 'l' {
			for _, param := range bytes.Split(buf[start:p], []byte(";")) {
				switch string(param) {
				case "1049", "1047", "47":
					alt = buf[p] == 'h'
				}
			}
		}
		i = p
	}
	keep := len(buf)
	if keep > altTailKeep {
		keep = altTailKeep
	}
	newTail = append([]byte(nil), buf[len(buf)-keep:]...)
	return newTail, alt
}

// NudgeRedraw makes the foreground application repaint by delivering two
// SIGWINCHs: the size is bumped one column narrower and then restored.
// Full-screen programs redraw from their model on a size change, which is
// the only way to reconstruct a screen the terminal did not witness.
func (s *Session) NudgeRedraw() {
	s.mu.Lock()
	cols, rows := s.Cols, s.Rows
	s.mu.Unlock()
	if s.pty == nil || cols < 2 || rows < 1 {
		return
	}
	pty.Setsize(s.pty, &pty.Winsize{Cols: uint16(cols - 1), Rows: uint16(rows)})
	pty.Setsize(s.pty, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
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

// DisconnectClients closes clients owned by the given tab/token set.
func (s *Session) DisconnectClients(tabID string, tokens map[string]bool) int {
	s.mu.Lock()
	snapshot := make([]*wsClient, len(s.clients))
	copy(snapshot, s.clients)
	s.mu.Unlock()

	disconnected := 0
	for _, c := range snapshot {
		if c.tabID != tabID || !tokens[c.ownerToken] {
			continue
		}
		c.conn.Close()
		disconnected++
	}
	return disconnected
}

// Broadcast queues binary data for every attached client. It never blocks
// the PTY reader and never drops bytes: a client that falls too far behind
// is disconnected by its writePump instead (see wsClient).
func (s *Session) Broadcast(data []byte) {
	s.mu.Lock()
	snapshot := make([]*wsClient, len(s.clients))
	copy(snapshot, s.clients)
	s.mu.Unlock()

	for _, c := range snapshot {
		c.queueBinary(data)
	}
}

// WriteInput writes user input bytes to the PTY.
func (s *Session) WriteInput(data []byte) {
	if s.pty != nil {
		s.pty.Write(data)
		s.LastActive = time.Now()
	}
}

// Resize updates the PTY window size. It reports whether the size actually
// changed (and so whether the application was sent a SIGWINCH).
func (s *Session) Resize(cols, rows int) (changed bool) {
	if cols <= 0 || rows <= 0 {
		return false
	}
	s.mu.Lock()
	changed = s.Cols != cols || s.Rows != rows
	s.Cols = cols
	s.Rows = rows
	s.mu.Unlock()
	if s.pty != nil && changed {
		pty.Setsize(s.pty, &pty.Winsize{
			Cols: uint16(cols),
			Rows: uint16(rows),
		})
	}
	return changed
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

	if err := os.WriteFile(path, data, 0600); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}

	log.Printf("Session %s: file saved: %s (%d bytes)", s.ID, path, len(data))
	return path, nil
}

func shellQuotePath(path string) string {
	return "'" + strings.ReplaceAll(path, "'", "'\\''") + "' "
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

// LastOutput returns the wall-clock time of the most recent PTY output byte.
// It returns the zero time.Time if the session has never produced output.
func (s *Session) LastOutput() time.Time {
	ns := s.lastOutput.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// Done returns a channel that closes when the session's shell exits.
func (s *Session) Done() <-chan struct{} {
	return s.done
}

// ---------------------------------------------------------------------------
// findShell — locate the best available shell binary
// ---------------------------------------------------------------------------

func resolveShell(preferred string) string {
	if preferred != "" {
		if strings.Contains(preferred, "/") {
			if _, err := os.Stat(preferred); err == nil {
				return preferred
			}
		} else if path, err := exec.LookPath(preferred); err == nil {
			return path
		}
		log.Printf("invalid shell %q, falling back to auto-detect", preferred)
	}

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

func findShell() string {
	return resolveShell("")
}
