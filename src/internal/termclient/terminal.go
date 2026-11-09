package termclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const wsHandshakeTimeout = 20 * time.Second

// Tab is one tab on one machine, created by this client.
type Tab struct {
	c         *Client
	base      string // the machine's proxied base path, with trailing slash
	ID        string
	SessionID string
}

// CreateTab makes a tab on m. With a command the tab's session runs it
// under a login shell and ends when it does; otherwise it is an
// interactive shell.
func (c *Client) CreateTab(ctx context.Context, m Machine, name, command string) (*Tab, error) {
	var tab struct {
		ID     string `json:"id"`
		Layout struct {
			SessionID string `json:"session_id"`
		} `json:"layout"`
	}
	body := map[string]string{"name": name}
	if command != "" {
		body["command"] = command
	}
	if err := c.api(ctx, http.MethodPost, m.URL+"api/tabs", body, &tab); err != nil {
		return nil, fmt.Errorf("create tab on %s: %w", m.Label, err)
	}
	if tab.ID == "" || tab.Layout.SessionID == "" {
		return nil, fmt.Errorf("create tab on %s: malformed answer", m.Label)
	}
	return &Tab{c: c, base: m.URL, ID: tab.ID, SessionID: tab.Layout.SessionID}, nil
}

// Delete removes the tab and kills its session.
func (t *Tab) Delete(ctx context.Context) error {
	err := t.c.api(ctx, http.MethodDelete, t.base+"api/tabs/"+t.ID, nil, nil)
	var nf *notFoundError
	if errors.As(err, &nf) {
		return nil
	}
	return err
}

// rebind points the tab at a fresh signed-in client (after a reconnect).
func (t *Tab) rebind(c *Client) { t.c = c }

// AttachOptions configure one attachment to a tab's session.
type AttachOptions struct {
	// ResumeToken is the owner token from a previous attachment; with it the
	// tab is taken back silently if the old connection is still registered.
	ResumeToken string
	// From, when >= 0, asks for the raw output stream from that byte offset
	// (a replay frame says where it really starts). -1 asks for a terminal
	// snapshot instead.
	From int64
	// Cols and Rows size the remote terminal before the first byte is
	// replayed.
	Cols, Rows int
}

// Conn is one live attachment: the tab socket that holds ownership and the
// session socket that carries the PTY.
type Conn struct {
	Token string

	tabWS  *websocket.Conn
	wmu    sync.Mutex // serialises writes to sessWS
	mu     sync.Mutex // guards sessWS and tabErr
	sessWS *websocket.Conn
	tabErr error
}

// tabLost records that the tab socket died and ends the session read with
// it: the session token is only valid while the tab socket is registered.
func (c *Conn) tabLost(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tabErr = err
	if c.sessWS != nil {
		c.sessWS.Close()
	}
}

func (c *Conn) setSession(ws *websocket.Conn) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tabErr != nil {
		ws.Close()
		return fmt.Errorf("tab socket: %w", c.tabErr)
	}
	c.sessWS = ws
	return nil
}

// Attach opens the tab socket, settles ownership, then opens the session
// socket and sizes the terminal.
func (t *Tab) Attach(ctx context.Context, opts AttachOptions) (*Conn, error) {
	dialer := websocket.Dialer{Jar: t.c.http.Jar, HandshakeTimeout: wsHandshakeTimeout, Proxy: http.ProxyFromEnvironment}

	q := url.Values{}
	if opts.ResumeToken != "" {
		q.Set("resume", opts.ResumeToken)
	}
	tabURL, err := t.c.wsURL(t.base+"ws/tab/"+t.ID, q)
	if err != nil {
		return nil, err
	}
	tabWS, resp, err := dialer.DialContext(ctx, tabURL, nil)
	if err != nil {
		return nil, t.dialError("tab", resp, err)
	}
	conn := &Conn{tabWS: tabWS}

	var hello struct {
		Type     string   `json:"type"`
		Token    string   `json:"token"`
		Sessions []string `json:"sessions"`
		Message  string   `json:"message"`
	}
	tabWS.SetReadDeadline(time.Now().Add(wsHandshakeTimeout))
	if err := tabWS.ReadJSON(&hello); err != nil {
		conn.Close()
		return nil, fmt.Errorf("tab handshake: %w", err)
	}
	if hello.Type == "conflict" {
		// Someone else (a browser, or our own dead connection the server
		// has not reaped) owns the tab. It is ours; take it.
		if err := tabWS.WriteJSON(map[string]string{"type": "takeover"}); err != nil {
			conn.Close()
			return nil, fmt.Errorf("tab takeover: %w", err)
		}
		if err := tabWS.ReadJSON(&hello); err != nil {
			conn.Close()
			return nil, fmt.Errorf("tab takeover: %w", err)
		}
	}
	if hello.Type != "connected" {
		conn.Close()
		return nil, fmt.Errorf("tab handshake: %s %s", hello.Type, hello.Message)
	}
	conn.Token = hello.Token
	tabWS.SetReadDeadline(time.Time{})
	// Keep reading: the server's pings need their pongs (gorilla answers
	// them inside ReadMessage) and the session token is valid only while
	// this socket is registered.
	go func() {
		for {
			if _, _, err := tabWS.ReadMessage(); err != nil {
				conn.tabLost(err)
				return
			}
		}
	}()

	q = url.Values{"tab_id": {t.ID}, "token": {conn.Token}}
	if opts.From >= 0 {
		q.Set("from", strconv.FormatInt(opts.From, 10))
	}
	sessURL, err := t.c.wsURL(t.base+"ws/session/"+t.SessionID, q)
	if err != nil {
		conn.Close()
		return nil, err
	}
	sessWS, resp, err := dialer.DialContext(ctx, sessURL, nil)
	if err != nil {
		conn.Close()
		return nil, t.dialError("session", resp, err)
	}
	if err := conn.setSession(sessWS); err != nil {
		conn.Close()
		return nil, err
	}
	if err := conn.Resize(opts.Cols, opts.Rows); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func (t *Tab) dialError(what string, resp *http.Response, err error) error {
	if resp != nil {
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return &AuthError{Server: t.c.server, Status: resp.Status}
		case http.StatusNotFound:
			return &TabGoneError{TabID: t.ID}
		}
		return fmt.Errorf("%s socket: %s", what, resp.Status)
	}
	return fmt.Errorf("%s socket: %w", what, err)
}

// FrameKind says what a session frame carries.
type FrameKind int

const (
	// Output is raw PTY bytes.
	Output FrameKind = iota
	// Snapshot is the terminal's picture as an ANSI stream for a freshly
	// reset terminal (snapshot attachments only).
	Snapshot
	// Replay opens a raw attachment: Offset is where the bytes that follow
	// start in the session's output stream.
	Replay
	// Exit ends the session: Code is the process's exit status.
	Exit
)

// Frame is one message from the session socket.
type Frame struct {
	Kind   FrameKind
	Data   []byte
	Offset int64
	Code   int
}

// Read returns the next frame. Control frames the client does not need
// (pongs, unknown types) are skipped. A dropped tab socket ends the
// attachment too, since the session token dies with it.
func (c *Conn) Read() (Frame, error) {
	for {
		kind, msg, err := c.sessWS.ReadMessage()
		if err != nil {
			c.mu.Lock()
			tabErr := c.tabErr
			c.mu.Unlock()
			if tabErr != nil {
				return Frame{}, fmt.Errorf("tab socket: %w", tabErr)
			}
			return Frame{}, err
		}
		if kind == websocket.BinaryMessage {
			return Frame{Kind: Output, Data: msg}, nil
		}
		var ctl struct {
			Type   string `json:"type"`
			Code   int    `json:"code"`
			Offset int64  `json:"offset"`
			Data   string `json:"data"`
		}
		if json.Unmarshal(msg, &ctl) != nil {
			continue
		}
		switch ctl.Type {
		case "exit":
			return Frame{Kind: Exit, Code: ctl.Code}, nil
		case "replay":
			return Frame{Kind: Replay, Offset: ctl.Offset}, nil
		case "scrollback":
			data, err := base64.StdEncoding.DecodeString(ctl.Data)
			if err != nil {
				continue
			}
			return Frame{Kind: Snapshot, Data: data}, nil
		}
	}
}

// Input sends keystrokes. Always a binary frame: a text frame that happens
// to parse as a control message would be swallowed by the server.
func (c *Conn) Input(b []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.sessWS.WriteMessage(websocket.BinaryMessage, b)
}

// Resize sets the remote terminal size.
func (c *Conn) Resize(cols, rows int) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.sessWS.WriteJSON(map[string]interface{}{"type": "resize", "cols": cols, "rows": rows})
}

// Close drops both sockets. The session keeps running on the machine.
func (c *Conn) Close() {
	c.mu.Lock()
	if c.sessWS != nil {
		c.sessWS.Close()
	}
	c.mu.Unlock()
	c.tabWS.Close()
}

// frames feeds Read's results to a channel until stop is closed or the read
// fails, so a select loop can watch other events alongside.
func (c *Conn) frames(stop <-chan struct{}) (<-chan Frame, <-chan error) {
	frames := make(chan Frame)
	errs := make(chan error, 1)
	go func() {
		for {
			f, err := c.Read()
			if err != nil {
				errs <- err
				return
			}
			select {
			case frames <- f:
			case <-stop:
				return
			}
		}
	}()
	return frames, errs
}
