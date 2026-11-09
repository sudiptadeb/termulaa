//go:build darwin || linux

package main

import (
	"os"
	"syscall"

	"github.com/creack/pty"
)

// ttyState is a terminal's settings before rawMode changed them.
type ttyState struct {
	fd      uintptr
	termios syscall.Termios
}

// isTerminal reports whether f is a terminal.
func isTerminal(f *os.File) bool {
	var t syscall.Termios
	return ioctlTermios(f.Fd(), ioctlGetTermios, &t) == nil
}

// rawMode puts the terminal in raw mode (what cfmakeraw does): every byte
// reaches the remote PTY as typed, nothing is echoed or translated locally,
// and Ctrl-C is a keystroke rather than a signal.
func rawMode(f *os.File) (*ttyState, error) {
	var t syscall.Termios
	if err := ioctlTermios(f.Fd(), ioctlGetTermios, &t); err != nil {
		return nil, err
	}
	saved := &ttyState{fd: f.Fd(), termios: t}
	t.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	t.Oflag &^= syscall.OPOST
	t.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	t.Cflag &^= syscall.CSIZE | syscall.PARENB
	t.Cflag |= syscall.CS8
	t.Cc[syscall.VMIN] = 1
	t.Cc[syscall.VTIME] = 0
	if err := ioctlTermios(f.Fd(), ioctlSetTermios, &t); err != nil {
		return nil, err
	}
	return saved, nil
}

// restore puts the terminal back as rawMode found it.
func (s *ttyState) restore() {
	ioctlTermios(s.fd, ioctlSetTermios, &s.termios)
}

// openPTY opens a pseudo-terminal pair. With rawOutput the slave's output
// post-processing (OPOST, and so ONLCR) is turned off before any process
// is attached, so what is written to it is what the master reads.
func openPTY(rawOutput bool) (ptmx, tty *os.File, err error) {
	ptmx, tty, err = pty.Open()
	if err != nil {
		return nil, nil, err
	}
	if rawOutput {
		var t syscall.Termios
		if err = ioctlTermios(tty.Fd(), ioctlGetTermios, &t); err == nil {
			t.Oflag &^= syscall.OPOST
			err = ioctlTermios(tty.Fd(), ioctlSetTermios, &t)
		}
		if err != nil {
			ptmx.Close()
			tty.Close()
			return nil, nil, err
		}
	}
	return ptmx, tty, nil
}

// terminalSize returns the terminal's columns and rows.
func terminalSize(f *os.File) (cols, rows int, err error) {
	rows, cols, err = pty.Getsize(f)
	return cols, rows, err
}

var sigwinch = syscall.SIGWINCH
