package termclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// Reconnection policy shared by Exec and Shell: a dropped connection is
// retried with backoff until ReconnectFor has passed without success.
const (
	DefaultReconnectFor = 15 * time.Minute
	reconnectMin        = time.Second
	reconnectMax        = 10 * time.Second
)

// ExecOptions describe one remote command run.
type ExecOptions struct {
	Machine string
	Command string
	// Cols and Rows size the PTY the command runs in.
	Cols, Rows int
	// Output receives the command's PTY output (stdout and stderr merged,
	// as the terminal would show it).
	Output io.Writer
	// Status receives one-line notices about the connection, or nil.
	Status io.Writer
	// Interrupt delivers Ctrl-C to the remote command each time it fires.
	Interrupt <-chan struct{}
	// ReconnectFor bounds how long a lost connection is retried; zero means
	// DefaultReconnectFor.
	ReconnectFor time.Duration
}

// ExecResult is how a run ended.
type ExecResult struct {
	// Code is the command's exit status (128+signal for a signal death).
	Code int
	// TabID names the tab left on the machine: because the run could not
	// be finished from here, or because the tab could not be removed after
	// it was. Empty when nothing was left behind.
	TabID string
}

// Exec runs opts.Command on opts.Machine and streams its output until it
// exits. The command runs in its own tab on the machine; the connection may
// drop and be rebuilt any number of times without disturbing it, and the
// exit code is retrieved even if the command ended while the client was
// disconnected. The tab is removed when the exit code is in hand, or when
// ctx is cancelled (which kills the command). A returned error means the
// run could not be completed from here; Result.TabID then says where it
// still is.
func Exec(ctx context.Context, creds Credentials, opts ExecOptions) (ExecResult, error) {
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
	tab, err := c.CreateTab(ctx, m, tabName("exec", opts.Command), opts.Command)
	if err != nil {
		return ExecResult{}, err
	}

	var offset int64
	token := ""
	for {
		conn, err := reconnect(ctx, creds, opts.Machine, tab, opts.Status, opts.ReconnectFor, func(ctx context.Context) (*Conn, error) {
			return tab.Attach(ctx, AttachOptions{ResumeToken: token, From: offset, Cols: opts.Cols, Rows: opts.Rows})
		})
		if err != nil {
			if ctx.Err() != nil {
				return abort(tab, opts.Status), ctx.Err()
			}
			return ExecResult{TabID: tab.ID}, err
		}
		token = conn.Token

		code, done, err := pump(ctx, conn, opts, &offset)
		conn.Close()
		if done {
			return ExecResult{Code: code, TabID: removeTab(tab, opts.Status)}, nil
		}
		if ctx.Err() != nil {
			return abort(tab, opts.Status), ctx.Err()
		}
		notice(opts.Status, "connection lost (%v); the command keeps running, reconnecting", err)
	}
}

// pump streams one attachment until exit (done) or a connection error.
func pump(ctx context.Context, conn *Conn, opts ExecOptions, offset *int64) (code int, done bool, err error) {
	stop := make(chan struct{})
	defer close(stop)
	frames, errs := conn.frames(stop)
	for {
		select {
		case <-ctx.Done():
			return 0, false, ctx.Err()
		case <-opts.Interrupt:
			if err := conn.Input([]byte{3}); err != nil {
				return 0, false, err
			}
		case err := <-errs:
			return 0, false, err
		case f := <-frames:
			switch f.Kind {
			case Replay:
				if f.Offset > *offset {
					notice(opts.Status, "%d bytes of output were lost while disconnected", f.Offset-*offset)
				}
				*offset = f.Offset
			case Output:
				if _, err := opts.Output.Write(f.Data); err != nil {
					return 0, false, fmt.Errorf("writing output: %w", err)
				}
				*offset += int64(len(f.Data))
			case Exit:
				return f.Code, true, nil
			}
		}
	}
}

// abort removes the tab, killing the command, on a cancelled run.
func abort(tab *Tab, status io.Writer) ExecResult {
	return ExecResult{TabID: removeTab(tab, status)}
}

// removeTabFor bounds the retries of a tab removal: the exit code is already
// in hand, so this is housekeeping that must not hold the caller long, but
// a machine mid-restart deserves a moment.
const removeTabFor = 30 * time.Second

// removeTab deletes the tab, retrying briefly. It returns the tab id when
// the tab is still there, after telling the user.
func removeTab(tab *Tab, status io.Writer) string {
	deadline := time.Now().Add(removeTabFor)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
		err := tab.Delete(ctx)
		cancel()
		if err == nil {
			return ""
		}
		if time.Now().After(deadline) || !isTransient(err) {
			notice(status, "tab %s could not be removed (%v); it is still listed on the machine", tab.ID, err)
			return tab.ID
		}
		time.Sleep(2 * time.Second)
	}
}

// reconnect calls attach until it succeeds or the budget runs out. Every
// retry signs in afresh and re-resolves the machine, since the session
// cookie or the agent's connection may be what dropped.
func reconnect(ctx context.Context, creds Credentials, machine string, tab *Tab, status io.Writer, budget time.Duration, attach func(context.Context) (*Conn, error)) (*Conn, error) {
	deadline := time.Now().Add(budget)
	delay := reconnectMin
	first := true
	for {
		if !first {
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("could not reconnect within %s", budget)
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
			delay = min(delay*2, reconnectMax)
			c, err := SignIn(ctx, creds)
			if err != nil {
				if !isTransient(err) {
					return nil, err
				}
				continue
			}
			if _, err := c.Find(ctx, machine); err != nil {
				if !isTransient(err) {
					return nil, err
				}
				var off *OfflineError
				if errors.As(err, &off) && delay == reconnectMax {
					notice(status, "%s is offline, waiting for it", machine)
				}
				continue
			}
			tab.rebind(c)
		}
		first = false
		conn, err := attach(ctx)
		if err == nil {
			return conn, nil
		}
		if !isTransient(err) {
			return nil, err
		}
	}
}

func notice(w io.Writer, format string, args ...interface{}) {
	if w != nil {
		fmt.Fprintf(w, "termulaa: "+format+"\n", args...)
	}
}

// tabName is what the tab is called in the browser UI: the command's first
// line, shortened.
func tabName(kind, command string) string {
	for i, r := range command {
		if r == '\n' || i >= 60 {
			return kind + ": " + command[:i] + "…"
		}
	}
	if command == "" {
		return "termulaa " + kind
	}
	return kind + ": " + command
}
