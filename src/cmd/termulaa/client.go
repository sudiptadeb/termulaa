package main

// The terminal client: `termulaa pair|machines|shell|exec|help`. It talks
// to a rendezvous the way the phone app does and never starts the server,
// the agent, or amarit; main dispatches here before any of those.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sudiptadeb/termulaa/src/internal/termclient"
)

// Exit codes of the client commands. A remote command's own status passes
// through unchanged; these are the client's.
const (
	exitUsage     = 2   // bad arguments
	exitInterrupt = 130 // the client was interrupted before the remote answered
	exitTransport = 255 // the client itself failed, as ssh reports it
)

// Terminal size used for exec when stdout is not a terminal.
const (
	execDefaultCols = 120
	execDefaultRows = 30
)

var clientCommands = map[string]bool{"pair": true, "machines": true, "shell": true, "exec": true, "help": true}

// isClientCommand reports whether argv[1] selects the client.
func isClientCommand(arg string) bool { return clientCommands[arg] }

// runClient runs one client command and returns the process exit code.
func runClient(args []string, stdout, stderr io.Writer) int {
	switch args[0] {
	case "help":
		return runHelp(args[1:], stdout, stderr)
	case "pair":
		return runPair(args[1:], stdout, stderr)
	case "machines":
		return runMachines(args[1:], stdout, stderr)
	case "exec":
		return runExec(args[1:], stdout, stderr)
	case "shell":
		return runShell(args[1:], stdout, stderr)
	}
	return exitUsage
}

// newFlags returns a flag set whose -h/--help prints the command's guide.
func newFlags(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("termulaa "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, helpFor(name)) }
	return fs
}

// parse handles the flag set's outcome: 0 to go on, otherwise an exit code.
func parse(fs *flag.FlagSet, args []string) (int, bool) {
	err := fs.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		return 0, true
	}
	if err != nil {
		return exitUsage, true
	}
	return 0, false
}

func fail(stderr io.Writer, err error) int {
	fmt.Fprintln(stderr, "termulaa:", err)
	var ae *termclient.AuthError
	if errors.Is(err, termclient.ErrNotPaired) {
		fmt.Fprintln(stderr, "  run: termulaa pair -server <url> <code>   (the code comes from the rendezvous dashboard)")
	} else if errors.As(err, &ae) {
		fmt.Fprintf(stderr, "  run: termulaa pair -server %s <code>   (get a fresh code from its dashboard)\n", ae.Server)
	}
	return exitTransport
}

func runPair(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("pair", stderr)
	server := fs.String("server", "", "rendezvous base URL, e.g. https://memd.example.com")
	label := fs.String("label", "", "name for this device on the dashboard (default: termulaa on <hostname>)")
	if code, done := parse(fs, args); done {
		return code
	}
	if *server == "" || fs.NArg() != 1 {
		fs.Usage()
		return exitUsage
	}
	if *label == "" {
		host, _ := os.Hostname()
		*label = "termulaa on " + host
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	creds, err := termclient.Pair(ctx, *server, fs.Arg(0), *label)
	if err != nil {
		return fail(stderr, err)
	}
	path := termclient.CredentialsPath()
	if err := creds.Save(path); err != nil {
		return fail(stderr, fmt.Errorf("saving credentials: %w", err))
	}
	fmt.Fprintf(stdout, "paired with %s; credentials in %s\n", creds.Server, path)
	return 0
}

func runMachines(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("machines", stderr)
	asJSON := fs.Bool("json", false, "print the list as JSON")
	if code, done := parse(fs, args); done {
		return code
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return exitUsage
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	creds, err := termclient.LoadCredentials(termclient.CredentialsPath())
	if err != nil {
		return fail(stderr, err)
	}
	c, err := termclient.SignIn(ctx, creds)
	if err != nil {
		return fail(stderr, err)
	}
	list, err := c.Machines(ctx)
	if err != nil {
		return fail(stderr, err)
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		enc.Encode(list)
		return 0
	}
	if len(list) == 0 {
		fmt.Fprintln(stderr, "no machine is connected (start `termulaa -rc` on one)")
		return 0
	}
	for _, m := range list {
		fmt.Fprintf(stdout, "%s\t%s\ttunnels=%d\tsince=%s\n", m.Label, m.ID, m.Tunnels, m.ConnectedAt.Local().Format(time.RFC3339))
	}
	return 0
}

// signalContext cancels the returned context on SIGTERM or SIGHUP and
// reports interrupts on the returned channel. In raw mode Ctrl-C is a
// keystroke and never arrives here.
func signalContext() (context.Context, context.CancelFunc, <-chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	interrupts := make(chan struct{}, 1)
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for s := range sigs {
			if s != os.Interrupt {
				cancel()
				return
			}
			select {
			case interrupts <- struct{}{}:
			default:
				// A second interrupt before the first was delivered: the
				// user wants out now.
				cancel()
				return
			}
		}
	}()
	return ctx, cancel, interrupts
}

func runExec(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("exec", stderr)
	if code, done := parse(fs, args); done {
		return code
	}
	rest := fs.Args()
	if len(rest) >= 2 && rest[1] == "--" {
		rest = append(rest[:1], rest[2:]...)
	}
	if len(rest) < 2 {
		fs.Usage()
		return exitUsage
	}
	machine, command := rest[0], strings.Join(rest[1:], " ")

	creds, err := termclient.LoadCredentials(termclient.CredentialsPath())
	if err != nil {
		return fail(stderr, err)
	}
	cols, rows := execDefaultCols, execDefaultRows
	if f, ok := stdout.(*os.File); ok {
		if c, r, err := terminalSize(f); err == nil {
			cols, rows = c, r
		}
	}
	ctx, cancel, interrupts := signalContext()
	defer cancel()
	res, err := termclient.Exec(ctx, creds, termclient.ExecOptions{
		Machine:   machine,
		Command:   command,
		Cols:      cols,
		Rows:      rows,
		Output:    stdout,
		Status:    stderr,
		Interrupt: interrupts,
	})
	if errors.Is(err, context.Canceled) {
		fmt.Fprintln(stderr, "termulaa: interrupted")
		return exitInterrupt
	}
	if err != nil {
		if res.TabID != "" {
			fmt.Fprintf(stderr, "termulaa: the command may still be running in tab %s on %s; open it in the browser to see\n", res.TabID, machine)
		}
		return fail(stderr, err)
	}
	return res.Code
}

func runShell(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("shell", stderr)
	if code, done := parse(fs, args); done {
		return code
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return exitUsage
	}
	machine := fs.Arg(0)

	creds, err := termclient.LoadCredentials(termclient.CredentialsPath())
	if err != nil {
		return fail(stderr, err)
	}
	if !isTerminal(os.Stdin) {
		return fail(stderr, errors.New("shell needs a terminal on stdin; use `termulaa exec` to run a command"))
	}
	saved, err := rawMode(os.Stdin)
	if err != nil {
		return fail(stderr, fmt.Errorf("raw mode: %w", err))
	}
	// Every exit path below goes through this restore.
	defer saved.restore()

	size := func() (int, int) {
		cols, rows, err := terminalSize(os.Stdin)
		if err != nil || cols <= 0 || rows <= 0 {
			return 80, 24
		}
		return cols, rows
	}
	resized := make(chan struct{}, 1)
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, sigwinch)
	go func() {
		for range winch {
			select {
			case resized <- struct{}{}:
			default:
			}
		}
	}()

	ctx, cancel, _ := signalContext()
	defer cancel()
	res, err := termclient.Shell(ctx, creds, termclient.ShellOptions{
		Machine: machine,
		In:      os.Stdin,
		Out:     stdout,
		Status:  stderr,
		Size:    size,
		Resized: resized,
	})
	saved.restore()
	fmt.Fprint(stdout, "\r\n")
	if err != nil {
		if res.TabID != "" {
			fmt.Fprintf(stderr, "termulaa: the shell is still running in tab %s on %s; open it in the browser\n", res.TabID, machine)
		}
		return fail(stderr, err)
	}
	return res.Code
}
