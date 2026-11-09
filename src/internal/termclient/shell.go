package termclient

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

// ShellOptions describe one interactive session. The caller owns the local
// terminal: it puts it in raw mode, reports its size, and restores it.
type ShellOptions struct {
	Machine string
	// In is the local keyboard; Out the local terminal (both raw).
	In  io.Reader
	Out io.Writer
	// Status receives connection notices written in raw-terminal form, or nil.
	Status io.Writer
	// Size returns the local terminal size; Resized fires when it changes.
	Size    func() (cols, rows int)
	Resized <-chan struct{}
	// ReconnectFor bounds how long a lost connection is retried; zero means
	// DefaultReconnectFor.
	ReconnectFor time.Duration
}

// resetTerminal precedes every snapshot: a full reset plus a cleared
// scrollback, so the local terminal shows exactly the remote session's
// picture and history and nothing of its own stale frames.
const resetTerminal = "\x1bc\x1b[3J"

// Shell runs an interactive login shell on opts.Machine in a new tab and
// bridges it to In and Out until the shell exits, returning its exit code.
// A dropped connection is rebuilt and the screen repainted from the
// server's snapshot; keystrokes typed while disconnected are dropped. When
// the shell exits the tab is removed. A returned error means the session
// was left running: Result.TabID says which tab.
func Shell(ctx context.Context, creds Credentials, opts ShellOptions) (ExecResult, error) {
	if opts.ReconnectFor <= 0 {
		opts.ReconnectFor = DefaultReconnectFor
	}
	c, err := SignIn(ctx, creds)
	if err != nil {
		return ExecResult{}, err
	}
	m, err := c.Find(ctx, opts.Machine)
	if err != nil {
		return ExecResult{}, err
	}
	tab, err := c.CreateTab(ctx, m, tabName("shell", ""), "")
	if err != nil {
		return ExecResult{}, err
	}

	var cur struct {
		sync.Mutex
		conn *Conn
	}
	current := func() *Conn {
		cur.Lock()
		defer cur.Unlock()
		return cur.conn
	}
	// Keyboard and resizes go to whichever connection is current.
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := opts.In.Read(buf)
			if n > 0 {
				if conn := current(); conn != nil {
					conn.Input(buf[:n])
				}
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		for range opts.Resized {
			if conn := current(); conn != nil {
				conn.Resize(opts.Size())
			}
		}
	}()

	token := ""
	for {
		conn, err := reconnect(ctx, creds, opts.Machine, tab, opts.Status, opts.ReconnectFor, func(ctx context.Context) (*Conn, error) {
			cols, rows := opts.Size()
			return tab.Attach(ctx, AttachOptions{ResumeToken: token, From: -1, Cols: cols, Rows: rows})
		})
		if err != nil {
			return ExecResult{TabID: tab.ID}, err
		}
		token = conn.Token
		cur.Lock()
		cur.conn = conn
		cur.Unlock()

		code, done, err := bridge(ctx, conn, opts.Out)
		cur.Lock()
		cur.conn = nil
		cur.Unlock()
		conn.Close()
		if done {
			if err := tab.Delete(context.Background()); err != nil {
				return ExecResult{Code: code, TabID: tab.ID}, fmt.Errorf("shell exited (%d) but its tab could not be removed: %w", code, err)
			}
			return ExecResult{Code: code}, nil
		}
		if ctx.Err() != nil {
			return ExecResult{TabID: tab.ID}, ctx.Err()
		}
		if opts.Status != nil {
			fmt.Fprintf(opts.Status, "\r\n\x1b[0mtermulaa: connection lost (%v); the session keeps running, reconnecting\r\n", err)
		}
	}
}

// bridge paints one attachment's frames until exit or a connection error.
func bridge(ctx context.Context, conn *Conn, out io.Writer) (code int, done bool, err error) {
	stop := make(chan struct{})
	defer close(stop)
	frames, errs := conn.frames(stop)
	for {
		select {
		case <-ctx.Done():
			return 0, false, ctx.Err()
		case err := <-errs:
			return 0, false, err
		case f := <-frames:
			switch f.Kind {
			case Snapshot:
				if _, err := io.WriteString(out, resetTerminal); err != nil {
					return 0, false, err
				}
				if _, err := out.Write(f.Data); err != nil {
					return 0, false, err
				}
			case Output:
				if _, err := out.Write(f.Data); err != nil {
					return 0, false, err
				}
			case Exit:
				return f.Code, true, nil
			}
		}
	}
}
