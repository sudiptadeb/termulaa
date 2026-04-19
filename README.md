# termulaa

A small, local HTTP + WebSocket server that spawns PTY sessions and renders
them in your browser via xterm.js. Tabs, binary pane splits, persistent
sessions that survive tab close, scrollback replay on reconnect, per-session
CWD tracking.

**Single-user. Loopback-only. Personal developer tool.** See
[SECURITY.md](SECURITY.md) for the threat model.

## Quick start

```bash
# Build (cross-compiles to dist/<os>/<binary>-<arch>-v<version>)
build/build.sh

# Run (macOS arm64 example)
./dist/darwin/terminal-agent-arm64-v1.0.0

# Open
open http://127.0.0.1:17380/
```

Or via the helper:

```bash
bash resources/scripts/run-terminal-agent.sh
```

## Why

`ttyd` + `tmux` gets you "browser-rendered PTY with persistence," but with
seams — reattaching doesn't replay scrollback cleanly, layout lives in tmux,
and `ttyd` has no tab/pane concept of its own. termulaa folds the pieces
into one small binary:

- **Persistent sessions** — the PTY stays alive after the browser tab
  closes; reconnecting replays the scrollback ring buffer.
- **Tabs + binary pane splits** — layout is first-class, persisted per tab.
- **Dead-session revival** — if the PTY has exited, the on-disk scrollback
  replays and a new shell spawns in the last-known cwd.
- **Per-session CWD tracking** — follows `/proc/<pid>/cwd` on Linux,
  `lsof -p` on macOS.
- **Shell history** — per-session `HISTFILE`.

## Layout

```
build/build.sh           # cross-compile to dist/<os>/
src/cmd/terminal-agent/  # Go sources + embedded ui/
resources/plans/         # design docs
resources/scripts/       # run + benchmark helpers
```

Two Go dependencies: [`creack/pty`](https://github.com/creack/pty) and
[`gorilla/websocket`](https://github.com/gorilla/websocket). Frontend is
vendored — Alpine.js, Twind, xterm.js + addons — no npm, no bundler, no
build step.

## Runtime state

```
~/.terminal-agent/
  config.json             # user settings (port, shell, scrollback size, ...)
  state.json              # tabs + session metadata
  scrollback/<id>.raw     # per-session raw PTY output (ring buffer)
  history/<id>.hist       # per-session shell HISTFILE
```

Settings are editable in-app at `http://127.0.0.1:17380/settings` or via
`GET/PUT /api/settings`.

## Platform support

Builds produced by `build/build.sh`:

- `darwin/amd64`, `darwin/arm64`
- `linux/amd64`, `linux/arm64`

Windows is not supported (PTY handling via `creack/pty` is POSIX-only).

## Security

Loopback-only, no auth, wildcard CORS. Any page open in a browser on the
same machine can attach to a PTY. Accepted for a single-user dev tool —
see [SECURITY.md](SECURITY.md) for the full posture and what would need to
change before exposing on a non-loopback interface.

## License

[MIT](LICENSE)
