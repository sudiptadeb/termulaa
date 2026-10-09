# termulaa

[![build](https://github.com/sudiptadeb/termulaa/actions/workflows/build.yml/badge.svg)](https://github.com/sudiptadeb/termulaa/actions/workflows/build.yml)
[![release](https://img.shields.io/github/v/release/sudiptadeb/termulaa?sort=semver)](https://github.com/sudiptadeb/termulaa/releases/latest)
[![license](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![go.mod](https://img.shields.io/github/go-mod/go-version/sudiptadeb/termulaa?filename=src/go.mod)](src/go.mod)

**Put your terminal — and your coding agents — in a browser tab, right
next to the webapp they're working on.** Live reload in one tab, Claude
Code / Codex / OpenCode in the next. If your browser has workspaces or
tab groups, group the terminals with the project they belong to and
switch between projects cleanly.

Persistent PTY sessions that survive tab close, tabs with binary pane
splits, exact screen restore on reconnect (the server keeps a terminal
emulator per session, so full-screen and inline-redrawing programs come
back as they are, not as a replay of stale frames), per-session CWD
tracking. Single small Go binary. Loopback-only — see
[SECURITY.md](SECURITY.md).

![termulaa — a coding agent and a streaming log in a single split terminal](resources/images/hero-real.png)

## Install

### `go install` (recommended if you have Go)

```bash
go install github.com/sudiptadeb/termulaa/src/cmd/termulaa@latest
termulaa
```

### One-liner (downloads the latest release binary)

```bash
curl -fsSL https://raw.githubusercontent.com/sudiptadeb/termulaa/main/install.sh | bash
```

Detects OS (linux/darwin) and arch (amd64/arm64), installs to
`~/.local/bin/termulaa`.

Install, pair and keep the tunnel agent running in one command (see
[Remote access](#remote-access) and
[Run it as a service](#run-it-as-a-service)):

```bash
curl -fsSL https://raw.githubusercontent.com/sudiptadeb/termulaa/main/install.sh \
  | bash -s -- --service --rc-server https://your.server --rc-token <token>
```

### Updating

Nothing updates by itself. There are two ways, both run by you:

**Re-run the install command.** This is the full update. It compares
`termulaa -version` against the latest release and replaces the binary
only when they differ. With `--service` it restarts the tunnel agent
onto the new binary and leaves a terminal server that is already on the
installed version untouched, so open terminals survive; with
`--rc-token` it also re-pairs. Without `--service` it restarts whatever
the service manager runs (server first, then the tunnel agent). A
`termulaa` you started by hand keeps running the old binary — the
installer names those processes but never kills them. Pass
`--no-restart` to stage the binary without touching anything running.

**`termulaa update`.** termulaa updates itself through
[upkeep](https://github.com/sudiptadeb/upkeep): it fetches the release
manifest (`upkeep.json` on the latest GitHub release), downloads the newest
version for this platform, checks size and sha256, smoke-tests it, swaps it
in keeping `termulaa.prev`, and re-execs; then it prints the version and
exits. Running services keep the old binary until restarted. `--update-url
<manifest>` points it at a mirror. `--auto-update` exists but is off by
default, and is only sensible for the `-rc` agent until the server hands its
terminals across a restart.

### Manual

Grab the binary for your platform from the
[Releases page](https://github.com/sudiptadeb/termulaa/releases/latest)
and drop it somewhere on your `PATH`.

### From source

```bash
git clone https://github.com/sudiptadeb/termulaa
cd termulaa
build/build.sh              # cross-compiles to dist/<os>/
./dist/darwin/termulaa-arm64-v0.1.0
```

## Run

```bash
termulaa
```

Then open <http://127.0.0.1:17380/> in your browser.
`termulaa -version` prints the installed version (also shown on
`/health`).

Change the port with `-port 17381`, or edit settings at
`http://127.0.0.1:17380/settings`.

### macOS Gatekeeper

The release binaries aren't signed or notarized. On first run macOS will
refuse to open them. Fix:

```bash
xattr -d com.apple.quarantine ~/.local/bin/termulaa
```

## Remote access

Optional. termulaa itself stays loopback-only — the server never listens
on anything but `127.0.0.1`, even in this mode. Remote access works
through a **reverse tunnel**: `termulaa -rc` runs a separate agent that
dials *out* to a rendezvous server over WebSocket and splices bytes
between the rendezvous and your local termulaa. No inbound port is ever
opened on your machine.

The protocol is owned by this repo and specified in
[docs/rc-protocol.md](docs/rc-protocol.md) — any server implementing it
works. The default rendezvous is the reference implementation (built in
memd); point the agent at your own with `-rc-server https://your.server`
(persisted to `~/.termulaa/rc.json`, so you only pass it once).

The agent holds a small pool of long-lived outbound connections
(tunnels), each carrying an smux session; every browser connection is a
stream multiplexed inside one of them. The agent parses no HTTP and
knows nothing about terminals — it is a dumb byte pump.

Pairing:

1. Start termulaa normally (`termulaa`).
2. Open `<rendezvous>/rc` in a browser, sign in, mint a tunnel token.
3. Run `termulaa -rc` on the same machine and paste the token when
   prompted. It persists to `~/.termulaa/rc.json` along with the server
   URL; later runs reconnect without prompting. `termulaa -rc` stays in
   the foreground and stops with its terminal — to pair without running
   it, use `termulaa -rc-save -rc-token <token>`, which saves the
   settings and exits, then let a [service](#run-it-as-a-service) run
   the agent.
4. Open your terminal from the rendezvous in any browser — either a
   link on its `/rc` page (path-prefix serving) or its dedicated view
   host with the pairing link it gives you, depending on how the
   rendezvous is configured.

The token is an opaque string to termulaa — it is stored and presented
verbatim, never parsed. Tokens expire by design (the rendezvous decides
when); when one does, the agent stops with a message pointing at
`<rendezvous>/rc`. Mint a fresh token and run `-rc` again with
`-rc-token <new token>`, or re-run the installer with `--rc-token`.

Prefer the saved pairing over flags once a machine is paired: a token
passed as `-rc-token` sits on the agent's command line, readable by
every local user in `ps`, for as long as the agent runs.

### From a phone

There is a native Android app in [`android/`](android/): it lists your
connected machines from the rendezvous, opens their terminals, and —
the main point — notifies you when a session produces output while you
are away (useful when long-running agents work on remote machines).
Every release ships the APK as a sideloadable asset; grab
`termulaa.apk` from the
[latest release](https://github.com/sudiptadeb/termulaa/releases/latest).
It signs in with your rendezvous account and needs a path-mode
rendezvous (the default).

The terminal UI itself is also usable straight from a phone browser
(e.g. over the reverse tunnel).
On touch devices a key bar docks above the on-screen keyboard with the
keys mobile keyboards lack: Tab, Esc, Ctrl, Alt, arrows, and characters
like `|` `~` and backtick. Ctrl and Alt are sticky — tap for one-shot,
double-tap to lock — and arrows send the right sequences inside
full-screen programs like vim. The terminal resizes to stay clear of
the keyboard, and the bar can be hidden (and stays hidden) if you use a
hardware keyboard.

Flags:

| Flag | Meaning |
|------|---------|
| `-rc` | run the tunnel agent (does not start the terminal server) |
| `-rc-server URL` | rendezvous base URL |
| `-rc-token TOKEN` | tunnel token (otherwise saved/prompted) |
| `-rc-target HOST:PORT` | local termulaa to splice to (default `127.0.0.1:<port>`) |
| `-rc-label NAME` | agent label shown on the rendezvous (saved to `~/.termulaa/rc.json`, so you only pass it once; default: saved label, else hostname) |
| `-rc-tunnels N` | pooled tunnel connections, 1–8 (default 4) |
| `-rc-insecure` | skip TLS verification — dev-only, for self-signed rendezvous certs |

See [SECURITY.md](SECURITY.md) for why this does not violate the
loopback rule, and what the residual risks are.

## Hosting under a path prefix

termulaa can be served behind a reverse proxy under a subpath (e.g.
`https://example.com/term/`), not just at a site root. The proxy must:

1. strip the prefix before forwarding (termulaa's routes never change —
   it still sees `/api/tabs`, `/ws/session/<id>`, …), and
2. set `X-Forwarded-Prefix` to the stripped prefix (no trailing slash),
   never passing through a client-supplied value.

termulaa then renders its pages with `<base href="<prefix>/">` so the
whole UI — fetches and WebSockets included — resolves under the prefix.
The header is strictly sanitized (see
[docs/rc-protocol.md §6.1](docs/rc-protocol.md) for the exact contract);
anything invalid falls back to root-relative pages. Served directly with
no proxy, nothing changes.

Reverse-proxying does **not** relax the loopback rule: the listener
still binds `127.0.0.1` only, and everything in [SECURITY.md](SECURITY.md)
about exposing termulaa beyond loopback applies to the proxy you put in
front of it.

## Run it as a service

Optional. `install.sh --service` installs the binary and sets both
processes up to start at login/boot and restart on failure — systemd
*user* units on Linux (no sudo), launchd LaunchAgents on macOS:

```bash
curl -fsSL https://raw.githubusercontent.com/sudiptadeb/termulaa/main/install.sh | bash -s -- --service
```

Re-running it is safe; it rewrites the service files in place and
restarts the services onto the freshly installed binary (an already
running unit would otherwise keep executing the old one). The
templates live in [resources/service/](resources/service/) if you prefer
to install them by hand (replace the `@TERMULAA_BIN@` / `@HOME@`
placeholders).

**Pair before the tunnel agent can run as a service.** The agent needs a
token, and under systemd/launchd there is no terminal to prompt on — it
exits immediately with `no token entered`. So the installer starts the
terminal server right away but leaves the agent service installed,
*not* started, until `~/.termulaa/rc.json` holds a token. Give the
installer the token and it does all three steps — save the pairing,
install the services, start the agent:

```bash
curl -fsSL https://raw.githubusercontent.com/sudiptadeb/termulaa/main/install.sh \
  | bash -s -- --service --rc-server https://your.server --rc-token <token> --rc-label my-box
```

When a token expires the agent exits the same way; mint a fresh one and
run that command again with it. A terminal server that is already on
the installed version is left alone on every re-run, because restarting
it closes the terminals it holds — only the tunnel agent is restarted.

### What keeps running

| Started as | Close the terminal | Log out | Restart the machine |
|---|---|---|---|
| macOS LaunchAgents | keeps running | stops | starts when you log in at the desktop |
| Linux systemd user units | keeps running | keeps running | starts at boot, no login needed |
| WSL2 systemd user units | stops soon after the last WSL window closes | stops | starts when WSL is next started |
| Detached (no service manager) | keeps running | keeps running | stays down until you re-run the installer |

**Detached** is what `--service` falls back to where no per-user service
manager can serve the account: SSH into a Mac account that is not logged
in at the desktop (launchd has no `gui/<uid>` domain for it), or a Linux
login without a systemd user session. The installer says so, starts the
terminal server and the tunnel agent with `nohup`, and logs to
`~/.termulaa/logs/`. It replaces only the tunnel agent it started itself
(tracked in `~/.termulaa/rc.pid`); an agent you started by hand is
reported and left alone. Stop everything with `pkill -x termulaa`.

On a Mac, per-user services start only when that user logs in at the
desktop — an SSH login does not count, and with FileVault on nothing
starts until the disk is unlocked. To start at boot with nobody logged
in, run termulaa as a LaunchDaemon instead (needs sudo): copy the two
plists from `~/Library/LaunchAgents/` to `/Library/LaunchDaemons/`, add
a `UserName` key with your account and an `EnvironmentVariables` dict
setting `HOME`, `chown root:wheel` them, and load each with
`sudo launchctl bootstrap system <plist>`. Terminals started that way
run outside your desktop session, so they cannot reach your login
keychain.

### Linux (systemd user units)

Units are written to `~/.config/systemd/user/`. The installer also runs
`loginctl enable-linger $USER` so your user services keep running after
logout and start at boot; if that fails it prints the command for you to
run yourself. `systemctl --user` needs a systemd user session — where
there is none (some containers and SSH setups), the installer says so
and starts termulaa detached instead (see
[What keeps running](#what-keeps-running)).

```bash
systemctl --user enable --now termulaa-rc      # after pairing
systemctl --user status termulaa termulaa-rc   # status
journalctl --user -u termulaa -f               # server logs
journalctl --user -u termulaa-rc -f            # agent logs
```

If the agent service starts without a token it exits and systemd retries
briefly (`StartLimitBurst=5` over 2 minutes), then gives up rather than
looping forever. After pairing, `systemctl --user restart termulaa-rc`.

Disable / remove:

```bash
systemctl --user disable --now termulaa termulaa-rc
rm ~/.config/systemd/user/termulaa.service ~/.config/systemd/user/termulaa-rc.service
systemctl --user daemon-reload
```

### macOS (launchd LaunchAgents)

Plists are written to `~/Library/LaunchAgents/`; logs go to
`~/.termulaa/logs/server.log` and `~/.termulaa/logs/rc.log`.

```bash
launchctl bootstrap gui/$UID ~/Library/LaunchAgents/com.termulaa.rc.plist   # after pairing
launchctl print gui/$UID/com.termulaa.server                                # status
tail -f ~/.termulaa/logs/server.log ~/.termulaa/logs/rc.log                 # logs
```

Without a saved token the agent exits at start and launchd retries every
30 seconds (`ThrottleInterval`), logging to `rc.log`, until you pair.

Disable / remove:

```bash
launchctl bootout gui/$UID/com.termulaa.server
launchctl bootout gui/$UID/com.termulaa.rc
rm ~/Library/LaunchAgents/com.termulaa.server.plist ~/Library/LaunchAgents/com.termulaa.rc.plist
```

## Why

`ttyd` + `tmux` gets you "browser-rendered PTY with persistence," but
with seams — reattaching doesn't replay scrollback cleanly, layout lives
in tmux, and `ttyd` has no tab or pane concept of its own. termulaa
folds the pieces into one small binary and aims it at one use case: a
browser tab you leave open next to whatever you're building.

- **Persistent sessions** — the PTY stays alive after the browser tab
  closes; reopening the tab restores the exact screen and scrollback.
  The server runs a headless terminal emulator per session and hands a
  reconnecting client a snapshot of its state, so `vim`, `htop` or a
  coding agent redrawing inline come back as they are.

  ![reopening a persistent session](resources/images/persistence.gif)

- **Tabs + binary pane splits** — layout is first-class, persisted per
  tab. `Cmd/Ctrl+D` splits vertically, `Cmd/Ctrl+Shift+D` splits
  horizontally, `Cmd/Ctrl+W` closes a pane, `Cmd/Ctrl+]` / `Cmd/Ctrl+[`
  cycle focus. `Cmd/Ctrl+/` (or the `?` in the corner) shows the in-app
  shortcut guide; rebind any of them via the `terminalKeybindings`
  localStorage key.
- **Dead-session revival** — if the PTY exited, the last screen and
  scrollback are restored from disk and a new shell spawns below them in
  the last-known cwd.
- **Per-session CWD tracking** — follows `/proc/<pid>/cwd` on Linux,
  `lsof -p` on macOS.
- **Shell history** — per-session `HISTFILE`.
- **Side-by-side with your work** — it's a browser tab, so workspaces
  and tab groups work out of the box. Flip between projects by flipping
  workspaces; each one keeps its own termulaa tab.

  ![switching browser workspaces, each with its own termulaa tab](resources/images/workspaces.gif)

## Layout

```
build/build.sh       # cross-compile to dist/<os>/
src/cmd/termulaa/    # Go sources + embedded ui/
docs/                # rc-protocol.md — the remote-access protocol spec
resources/plans/     # design docs
resources/scripts/   # run + benchmark helpers
resources/service/   # systemd user unit + launchd plist templates
resources/images/    # README screenshots + GIFs
```

Four Go dependencies: [`creack/pty`](https://github.com/creack/pty),
[`gorilla/websocket`](https://github.com/gorilla/websocket),
[`xtaci/smux`](https://github.com/xtaci/smux) (multiplexes remote-access
viewers over the `-rc` tunnel pool), and
[`sudiptadeb/upkeep`](https://github.com/sudiptadeb/upkeep) (opt-in
self-update from the release manifest, configured by `upkeep.config`). Frontend is vendored — Alpine.js,
Twind, xterm.js + addons — no npm, no bundler, no build step.

## Runtime state

```
~/.termulaa/
  config.json             # user settings (port, shell, scrollback size, ...)
  state.json              # tabs + session metadata
  scrollback/<id>.raw     # per-session raw PTY output (ring buffer)
  history/<id>.hist       # per-session shell HISTFILE
```

Settings are editable in-app at `/settings` or via `GET/PUT
/api/settings`.

## Platform support

Builds produced by `build/build.sh` and the release workflow:

- `darwin/amd64`, `darwin/arm64`
- `linux/amd64`, `linux/arm64`

**Windows is not supported.** PTY handling via `creack/pty` is
POSIX-only; Windows would need a ConPTY port. Tracked as an open issue.

## Security

Loopback-only (`127.0.0.1`). Host-header allowlist and strict Origin
checks on both HTTP and WebSocket. Any page open in a browser on the
same machine can still talk to the server if it uses the right Host —
accepted risk for a single-user dev tool. Full threat model and the
controls required before exposing on a non-loopback interface are in
[SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE)
