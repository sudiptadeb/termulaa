# termulaa

Local HTTP/WebSocket terminal agent. Spawns PTY sessions and serves a browser-based terminal UI (xterm.js) on `127.0.0.1:17380`.

Single-user, loopback-only, personal developer tool.

## Quick start

```bash
# Build
build/build.sh

# Run (macOS arm64 example)
./dist/darwin/terminal-agent-arm64-v1.0.0

# Open
open http://127.0.0.1:17380/
```

Or via the helper script:

```bash
bash resources/scripts/run-terminal-agent.sh
```

## Features

- Persistent sessions — PTYs stay alive after browser tab close
- Tab / pane split layout
- Per-session scrollback ring buffer and shell history (HISTFILE)
- Dead-session revival with scrollback replay
- Embedded frontend (Alpine.js + Twind + xterm.js) — no npm, no build step

## Layout

```
build/build.sh           # build (cross-compile to dist/<os>/)
src/cmd/terminal-agent/  # Go sources + embedded ui/
resources/plans/         # design docs
resources/scripts/       # run + benchmark helpers
```

See `CLAUDE.md` for rules, `resources/plans/terminal-agent-v2-design.md` for the data model.
