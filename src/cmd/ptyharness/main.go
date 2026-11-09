// ptyharness drives `termulaa shell` under a real pseudo-terminal for
// resources/scripts/client-e2e.sh and checks
// the interactive behaviours: initial size, resize via SIGWINCH, a
// full-screen program, reconnect across an agent restart (the -hook command
// does the restart), the remote exit code, and that the local terminal's
// settings are restored when the client exits.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/creack/pty"
)

var (
	hook   = flag.String("hook", "", "shell command run between the before and after checks (e.g. restart the agent)")
	failed = 0
)

type capture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *capture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// expect waits until the output since mark contains want.
func (c *capture) expect(name string, mark int, want string, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	for {
		s := c.String()
		if len(s) >= mark && strings.Contains(s[mark:], want) {
			fmt.Printf("PASS %s\n", name)
			return len(s)
		}
		if time.Now().After(deadline) {
			tail := s
			if len(tail) > 600 {
				tail = tail[len(tail)-600:]
			}
			fmt.Printf("FAIL %s: did not see %q; tail: %q\n", name, want, tail)
			failed++
			return len(s)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func termios(f *os.File) (syscall.Termios, error) {
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(ioctlGetTermios), uintptr(unsafe.Pointer(&t)))
	if errno != 0 {
		return t, errno
	}
	return t, nil
}

func main() {
	flag.Parse()
	args := flag.Args()
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: ptyharness [-hook cmd] <termulaa shell command...>")
		os.Exit(2)
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = os.Environ()
	ptmx, tty, err := pty.Open()
	if err != nil {
		panic(err)
	}
	pty.Setsize(ptmx, &pty.Winsize{Cols: 80, Rows: 24})
	before, err := termios(tty)
	if err != nil {
		panic(err)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		panic(err)
	}
	tty.Close()

	out := &capture{}
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				out.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	send := func(s string) { ptmx.Write([]byte(s)) }

	mark := 0
	// The remote prompt appears after the login shell starts; a size query
	// proves the resize sent on attach took.
	time.Sleep(1500 * time.Millisecond)
	send("stty size; echo SIZE-DONE\n")
	mark = out.expect("initial size 24x80 reaches the remote PTY", mark, "24 80", 10*time.Second)

	pty.Setsize(ptmx, &pty.Winsize{Cols: 100, Rows: 40})
	time.Sleep(500 * time.Millisecond)
	send("stty size; echo RESIZE-DONE\n")
	mark = out.expect("SIGWINCH resize to 40x100 reaches the remote PTY", mark, "40 100", 10*time.Second)

	send("vi\n")
	mark = out.expect("full-screen app enters the alternate screen", mark, "\x1b[?1049h", 10*time.Second)
	time.Sleep(500 * time.Millisecond)
	send("\x1b:q!\n")
	mark = out.expect("full-screen app leaves the alternate screen", mark, "\x1b[?1049l", 10*time.Second)

	send("echo before-$((20+1))\n")
	mark = out.expect("command before the drop", mark, "before-21", 10*time.Second)
	if *hook != "" {
		h := exec.Command("/bin/sh", "-c", *hook)
		h.Stdout, h.Stderr = os.Stdout, os.Stderr
		if err := h.Run(); err != nil {
			fmt.Printf("FAIL hook: %v\n", err)
			failed++
		}
		mark = out.expect("client notices the drop and reconnects", mark, "reconnecting", 30*time.Second)
		// The repaint is a full reset followed by the snapshot, which ends
		// with the prompt line we last saw.
		mark = out.expect("screen repainted from snapshot after reconnect", mark, "\x1bc\x1b[3J", 60*time.Second)
		time.Sleep(500 * time.Millisecond)
		send("echo after-$((40+2))\n")
		mark = out.expect("command after reconnect runs in the same shell", mark, "after-42", 20*time.Second)
	}

	send("exit 5\n")
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	select {
	case err := <-waitDone:
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		if code == 5 {
			fmt.Println("PASS client exits with the remote shell's code 5")
		} else {
			fmt.Printf("FAIL exit code = %d (err %v), want 5\n", code, err)
			failed++
		}
	case <-time.After(20 * time.Second):
		fmt.Println("FAIL client did not exit after remote shell exited")
		failed++
		cmd.Process.Kill()
	}

	after, err := termios(ptmx)
	if err != nil {
		fmt.Printf("SKIP termios check: %v\n", err)
	} else if after.Lflag == before.Lflag && after.Iflag == before.Iflag && after.Oflag == before.Oflag {
		fmt.Println("PASS local terminal settings restored on exit")
	} else {
		fmt.Printf("FAIL terminal not restored: lflag %x->%x iflag %x->%x oflag %x->%x\n", before.Lflag, after.Lflag, before.Iflag, after.Iflag, before.Oflag, after.Oflag)
		failed++
	}
	if failed > 0 {
		os.Exit(1)
	}
}
