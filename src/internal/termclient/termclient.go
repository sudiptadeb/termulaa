// Package termclient is a non-browser client for termulaa terminals reached
// through a rendezvous (docs/terminal-protocol.md): pairing with an app
// code, listing connected machines, and speaking the tab and session
// WebSockets to run a command or drive an interactive shell on one of them.
// It knows nothing about the local terminal; the CLI and other front ends
// bring their own.
package termclient

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNotPaired is returned when no credentials are stored.
var ErrNotPaired = errors.New("not paired with a rendezvous")

// AuthError means the stored token was rejected: it was revoked, or the
// server no longer knows it. The fix is to pair again.
type AuthError struct {
	Server string
	Status string
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("%s rejected the stored token (%s): pair again with a fresh code from its dashboard", e.Server, e.Status)
}

// OfflineError means the named machine is not connected right now.
type OfflineError struct {
	Machine   string
	Connected []string
}

func (e *OfflineError) Error() string {
	if len(e.Connected) == 0 {
		return fmt.Sprintf("%q is not connected, and no machine is", e.Machine)
	}
	return fmt.Sprintf("%q is not connected; connected: %s", e.Machine, strings.Join(e.Connected, ", "))
}

// OldMachineError means the machine's termulaa predates command sessions:
// it cannot run a command and report its exit code. shell still works.
type OldMachineError struct{ Machine string }

func (e *OldMachineError) Error() string {
	return fmt.Sprintf("the termulaa on %s is too old to run commands (it ignores them and opens a shell): run `termulaa update` on %s, then retry", e.Machine, e.Machine)
}

// TabGoneError means the tab the client was using no longer exists on the
// machine (deleted, or the server lost its state), so there is nothing to
// resume.
type TabGoneError struct{ TabID string }

func (e *TabGoneError) Error() string {
	return fmt.Sprintf("tab %s no longer exists on the machine", e.TabID)
}
