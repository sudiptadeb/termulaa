//go:build !darwin && !linux

package main

import (
	"errors"
	"os"
	"syscall"

	"github.com/creack/pty"
)

type ttyState struct{}

func openPTY(bool) (*os.File, *os.File, error) { return pty.Open() }

func isTerminal(*os.File) bool { return false }

func rawMode(*os.File) (*ttyState, error) {
	return nil, errors.New("interactive shell is not supported on this platform")
}

func (*ttyState) restore() {}

func terminalSize(*os.File) (int, int, error) {
	return 0, 0, errors.New("no terminal")
}

var sigwinch = syscall.Signal(0)
