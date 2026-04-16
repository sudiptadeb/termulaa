# CLAUDE.md

## HARD RULES — Never Violate These

1. **NEVER run `go build` directly** — always use `build/build.sh` (handles cross-compilation, versioning, dist layout)
2. **ALL binaries go in `dist/`** — never create binaries anywhere else (`src/`, project root, etc.)
3. **ALL temp/debug files go in `.tmp/`** — configs, dumps, test outputs. `.tmp/` is gitignored.
4. **NEVER commit secrets** — `.env`, API keys, local state files with real user data
5. **Loopback bind is non-negotiable** — the HTTP listener in `src/cmd/terminal-agent/main.go` MUST bind to `127.0.0.1` only. Do not change to `0.0.0.0` or any non-loopback address. See the security posture comment at the top of `main.go`.

## Project Context

`terminal-agent` is a personal developer tool: a local HTTP/WebSocket server that spawns PTY sessions and serves a browser-based terminal UI (xterm.js). Single-user, loopback-only. Runs on one developer's machine.

Sessions persist across browser tab close (PTY stays alive). Tabs group panes in a binary split layout. State persists to `~/.terminal-agent/`.

**Build**: `build/build.sh` (or `build/build.sh terminal-agent`)
**Run**: `./dist/darwin/terminal-agent-arm64-v1.0.0` (or via `resources/scripts/run-terminal-agent.sh`)
**URL**: http://127.0.0.1:17380/

## Security Posture

See the package doc at the top of `src/cmd/terminal-agent/main.go`. Summary:

- Local-only, single-user, never shipped to end users
- HTTP listener binds to `127.0.0.1` ONLY
- No per-request auth, wildcard CORS, `CheckOrigin` returns true on the WebSocket upgrader — acceptable *only* because the port is loopback-only
- Any webpage opened in a browser on the same machine can call the API and attach to a PTY. Known, accepted risk for a single-user dev tool.
- If this ever needs to ship to users or listen on a non-loopback interface, it MUST first add real per-request auth (bearer secret or OS-local credential), a locked-down CORS policy, and a strict origin check on the WebSocket upgrader. Until then, relaxing the localhost bind is a security incident.

## Design Philosophy

**"Simple is Hard, Easy is Easy"** — small surface, minimal deps, no frameworks. Two external Go deps: `creack/pty` and `gorilla/websocket`. Frontend is vendored — no npm, no bundler, no build step.

## Key Patterns

### Session / Tab / Pane

| Term | What it is |
|------|-----------|
| **Session** | A PTY + scrollback ring buffer + cwd. Backend-only. |
| **Tab** | A named layout of panes. What the user sees. |
| **Pane** | A slot in a tab's binary split tree. Points to a session ID. |

A session's PTY stays alive after the browser tab closes. Reattach replays the scrollback ring buffer from the live session's memory; dead-session revival replays the on-disk scrollback file, then spawns a fresh shell in the saved cwd.

### State persistence

```
~/.terminal-agent/
  config.json             # user settings (port, shell, scrollback size, ...)
  state.json              # tabs + session metadata
  scrollback/<id>.raw     # per-session raw PTY output (ring buffer, flushed periodically)
  history/<id>.hist       # per-session shell HISTFILE
```

### WebSocket API

```
/ws/tab/{tabID}           # tab ownership + layout commands (split, close_pane, update_layout)
/ws/session/{sessionID}   # per-session PTY I/O; ?mode=watch for read-only
```

### CWD tracking

Per-session background poll. Linux: `/proc/<pid>/cwd`. macOS: `lsof -p <pid> -Fn`. Platform files: `cwd_linux.go`, `cwd_darwin.go`, `cwd_other.go`.

## Configuration

Stored in `~/.terminal-agent/config.json`. Editable via `GET/PUT /api/settings` or the settings page at `http://127.0.0.1:17380/settings`.

## Logging

`terminal-agent` uses the standard library `log` package, not a structured logger. Keep log lines short and factual. Always log when something is skipped or fails — never silently swallow.

## Key Docs (read when working on specific areas)

| Area | Doc |
|------|-----|
| v2 design overview | `resources/plans/terminal-agent-v2-design.md` |
| Tab ownership (in-progress) | `resources/plans/terminal-agent-tab-ownership.md` |
| History (v1) | `resources/plans/local-terminal-in-browser.md` |

## Scripts (`resources/scripts/`)

| Script | Purpose |
|--------|---------|
| `run-terminal-agent.sh` | Kill existing, build if missing, start binary |
| `terminal-bench.sh` | Throughput / ANSI / cursor / unicode / stress benchmarks (run in different terminals and compare) |

## Documentation Conventions

- **Code over comments** — default to writing no comments. Only add one when the WHY is non-obvious.
- **Plans in `resources/plans/`** — design docs that outlive a single task. Update the doc after behavior changes.
- **Scripts are runbooks** — flat list of commands, variables at the top, no arg parsing beyond what's essential.
- **ASCII diagrams** — plain `|` for vertical, box-drawing only for boxes.
