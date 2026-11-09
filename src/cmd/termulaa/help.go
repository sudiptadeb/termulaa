package main

import (
	"fmt"
	"io"
	"strings"
)

// usage is the top-level guide printed by `termulaa --help`, `termulaa
// help`, and bad arguments. The server's own flags follow it (flag.Usage).
const usage = `termulaa — a terminal server, its remote-access agent, and a terminal client

Server (the terminal in a browser tab, loopback-only):
  termulaa [-port N]               serve http://127.0.0.1:17380/
  termulaa -rc [flags]             run the remote-access agent (dials out to a rendezvous)
  termulaa update                  update this binary to the latest release
  termulaa -version                print the version

Client (reach a machine running the agent, from any terminal or script):
  termulaa pair -server <url> <code>   pair this device with a rendezvous (one-time code)
  termulaa machines [--json]           list the machines connected right now
  termulaa shell <machine>             interactive terminal on a machine
  termulaa exec <machine> -- <cmd>     run a command there, stream its output, exit with its code
  termulaa help <command>              the full guide for one command
  termulaa help agents                 driving termulaa from an automation agent (no TTY)

Server and agent flags:
`

const helpPair = `usage: termulaa pair -server <url> [-label <name>] <code>

Pairs this device with a rendezvous (a memd server). On the rendezvous
dashboard, open the termulaa page and choose "pair a device": it shows a
short one-time code that expires within minutes. Redeem it here.

What it stores: ~/.termulaa/client.json (mode 0600) with the server URL
and the app token the rendezvous issued. The token is never printed. It
stays valid until revoked on the dashboard (paired devices list).

Examples:
  termulaa pair -server https://memd.example.com 483-920
  termulaa pair -server https://memd.example.com -label "ci runner" 483-920

Exit codes:
  0    paired
  2    bad arguments
  255  the rendezvous refused the code (expired or already used: get a
       fresh one), could not be reached, or the file could not be written
`

const helpMachines = `usage: termulaa machines [--json]

Lists the machines connected to the rendezvous right now, one per line:
  <label>  <id>  tunnels=<n>  since=<time>
A machine not listed is offline: its agent (termulaa -rc) is not running
or cannot reach the rendezvous. Use the label (or the id, when two
machines share a label) as <machine> in shell and exec.

--json prints an array of {"id","label","port","tunnels","connected_at","url"}.

Exit codes:
  0    listed (an empty list prints a note on stderr and still exits 0)
  255  not paired (run termulaa pair), the token was rejected (pair
       again), or the rendezvous could not be reached
`

const helpExec = `usage: termulaa exec <machine> -- <command...>

Runs <command> on <machine> and streams its output here, then exits with
the command's own exit code. For a script or an agent this is the remote
equivalent of running the command locally.

How the command runs:
  The words after -- are joined with single spaces into ONE string and
  run on the machine as:  $SHELL -lc '<string>'
  (the machine's login shell, so its PATH and profile apply). Quote for
  the remote shell as you would for ssh: wrap the whole command in single
  quotes locally and it arrives intact, pipes, globs, $VARS and all.
  It runs in a pseudo-terminal (PTY): stdout and stderr arrive merged,
  in the order they were written. The bytes are exactly the command's
  own (the PTY does no output translation: \n stays \n, binary output
  is byte-clean). There is no stdin from here: a command that reads
  stdin waits for input it will never get, so give it </dev/null or a
  here-string.
  The command's own stdout is this process's stdout; termulaa's notices
  go to stderr prefixed "termulaa:".

Exit codes:
  <n>    the command's exit status, as the remote shell reports it
         (a command killed by a signal gives 128+signal, e.g. 137)
  %d    interrupted: Ctrl-C (SIGINT) is sent to the remote command as a
         keystroke, so a normal command exits with 130 of its own accord.
         A second Ctrl-C, or SIGTERM/SIGHUP, stops the command and exits.
  %d    termulaa itself failed: not paired, token rejected, machine
         offline, or the connection could not be rebuilt in time. The
         message on stderr says which and what to do.

While it runs:
  The command runs in its own tab on the machine, independent of this
  connection. If the connection drops (rendezvous restart, network
  change), termulaa reconnects with backoff for up to 15 minutes and
  resumes the output exactly where it left off; the command keeps
  running meanwhile and its exit code is still delivered even if it
  finished while disconnected. If more output arrived meanwhile than
  the machine keeps (its scrollback limit, 1 MB by default), the oldest
  part is skipped and a stderr notice says how many bytes. When
  reconnecting fails for 15 minutes, termulaa exits %d and names the
  tab, which is still visible in the browser UI on the rendezvous.
  On success the tab is removed; nothing is left behind.

Examples:
  termulaa exec laptop -- uname -a
  termulaa exec laptop -- 'cd ~/src/app && make test 2>&1 | tail -50'
  termulaa exec laptop -- 'cd ~/src/app && ./gradlew assembleRelease'   # long build: safe across network drops
  if termulaa exec laptop -- 'test -d ~/src/app'; then echo present; fi
`

const helpShell = `usage: termulaa shell <machine>

Opens an interactive login shell on <machine> in this terminal: raw
keyboard, resizes follow the window, full-screen programs (vim, top,
tmux) work. The local terminal is restored on every way out.

The shell runs in a new tab on the machine. If the connection drops,
termulaa reconnects for up to 15 minutes and repaints the screen from
the machine's own copy of it; the shell and whatever runs in it are
untouched. Keystrokes typed while disconnected are dropped. When the
shell exits (exit, Ctrl-D) the tab is removed.

Exit codes:
  <n>    the remote shell's exit status
  %d    the connection could not be rebuilt in time, or SIGTERM/SIGHUP
         ended the client: the tab is left running and its id printed;
         open it in the browser UI on the rendezvous
  2      bad arguments, or stdin is not a terminal (use exec instead)

Examples:
  termulaa shell laptop
  termulaa shell 3fa9c2e1        # by id, when two machines share a label
`

const helpAgents = `Driving termulaa from an automation agent (no TTY assumed)

1. Pair once (a person does this; it needs a code from the dashboard):
     termulaa pair -server https://memd.example.com <code>
   Credentials land in ~/.termulaa/client.json (0600). Nothing to export.

2. Pick a machine:
     termulaa machines --json
   Use "label" as <machine>, or "id" when labels repeat. An empty list
   means no agent is connected; nothing here can start one.

3. Run a command and read its result:
     termulaa exec <machine> -- '<command>'
   stdout is the command's merged stdout+stderr, byte for byte (a PTY
   merges them; it does not alter the bytes); the exit code is the
   command's. Quote the whole command in single
   quotes; it runs under the machine's login shell. Commands that read
   stdin must get it from the command line (</dev/null, here-strings).
   Long builds are fine: a dropped connection is resumed for up to 15
   minutes and the exit code survives a disconnect.

4. Failures, by exit code and stderr text:
   %d   "not paired"                    -> step 1
   %d   "rejected the stored token"     -> pair again (revoked on the dashboard)
   %d   "is not connected; connected:"  -> the machine is offline; the list
                                           names what is online. Wait or
                                           pick another.
   %d   "could not reconnect within"    -> the command may still be running
                                           in the named tab; the result is
                                           lost to this run. Re-run or
                                           inspect in the browser UI.
   %d  interrupted (SIGINT) before the command answered
   other the command's own exit status; read its output

5. Do not use shell from an agent: it needs a real terminal. Everything
   an agent needs is exec.
`

// helpFor renders the guide for one command, with the exit codes filled
// from the constants so the text cannot drift from the behaviour.
func helpFor(name string) string {
	switch name {
	case "pair":
		return helpPair
	case "machines":
		return helpMachines
	case "exec":
		return fmt.Sprintf(helpExec, exitInterrupt, exitTransport, exitTransport)
	case "shell":
		return fmt.Sprintf(helpShell, exitTransport)
	case "agents":
		return fmt.Sprintf(helpAgents, exitTransport, exitTransport, exitTransport, exitTransport, exitInterrupt)
	}
	return ""
}

// helpTopics lists what `termulaa help <x>` accepts.
var helpTopics = []string{"pair", "machines", "shell", "exec", "agents"}

func runHelp(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if text := helpFor(args[0]); text != "" {
		fmt.Fprint(stdout, text)
		return 0
	}
	fmt.Fprintf(stderr, "termulaa help: unknown topic %q; topics: %s\n", args[0], strings.Join(helpTopics, ", "))
	return exitUsage
}
