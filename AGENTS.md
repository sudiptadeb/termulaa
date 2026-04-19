# AGENTS.md

Guide for AI coding agents (Claude Code, Codex, OpenCode, Cursor, etc.)
and human contributors working on this repo. Read this first.

## Hard rules — never violate

1. **Never run `go build` directly.** Always use `build/build.sh` (handles
   cross-compilation, versioning, dist layout). Running `go build` inside
   `src/` drops a stray binary at `src/terminal-agent` that violates rule 2.
2. **All binaries go in `dist/`.** Never create binaries anywhere else
   (`src/`, project root, etc.). `dist/` is gitignored.
3. **All temp / debug files go in `.tmp/`.** Configs, dumps, test outputs.
   `.tmp/` is gitignored.
4. **Never commit secrets.** `.env`, API keys, local state files with real
   user data. See `.gitignore` for what's blocked.
5. **Loopback bind is non-negotiable.** The HTTP listener in
   `src/cmd/terminal-agent/main.go` MUST bind to `127.0.0.1` only. Do not
   change to `0.0.0.0` or any non-loopback address. See the security
   posture comment at the top of `main.go` and [SECURITY.md](SECURITY.md).

## Project context

**termulaa** is a personal developer tool — a local HTTP + WebSocket
server that spawns PTY sessions and serves a browser-based terminal UI
(xterm.js). Single-user, loopback-only. Runs on one developer's machine.

Sessions persist across browser tab close (PTY stays alive). Tabs group
panes in a binary-split layout. State persists to `~/.terminal-agent/`.

**Build**: `build/build.sh` (or `build/build.sh terminal-agent 0.1.0`)
**Run**: `./dist/darwin/terminal-agent-arm64-v0.1.0` (or via
`resources/scripts/run-terminal-agent.sh`)
**URL**: http://127.0.0.1:17380/

## Security posture (summary)

Full threat model in [SECURITY.md](SECURITY.md). Short version:

- Local-only, single-user, never shipped to end users.
- HTTP listener binds to `127.0.0.1` only.
- Host-header allowlist (`127.0.0.1`, `localhost`, `::1` on the
  configured port). Mismatches return 421.
- Origin allowlist on both HTTP and WebSocket upgrades. Cross-origin
  browsers hit 403. Non-browser clients that omit Origin are allowed.
- No per-request auth, no wildcard CORS. These are acceptable *only*
  because the port is loopback-only.
- Before exposing on a non-loopback interface: real per-request auth,
  strict CORS, tight WS origin check, TLS, rate limiting. Relaxing the
  loopback bind without those is a security incident.

## Design philosophy

**"Simple is Hard, Easy is Easy"** — small surface, minimal deps, no
frameworks. Two Go deps: `creack/pty` and `gorilla/websocket`. Frontend
is vendored — no npm, no bundler, no build step.

## Key patterns

### Session / Tab / Pane

| Term | What it is |
|------|-----------|
| **Session** | A PTY + scrollback ring buffer + cwd. Backend-only. |
| **Tab** | A named layout of panes. What the user sees. |
| **Pane** | A slot in a tab's binary split tree. Points to a session ID. |

A session's PTY stays alive after the browser tab closes. Reattach
replays the scrollback ring buffer from the live session's memory;
dead-session revival replays the on-disk scrollback file, then spawns a
fresh shell in the saved cwd.

### State persistence

```
~/.terminal-agent/
  config.json             # user settings (port, shell, scrollback size, ...)
  state.json              # tabs + session metadata
  scrollback/<id>.raw     # per-session raw PTY output (ring buffer)
  history/<id>.hist       # per-session shell HISTFILE
```

### WebSocket API

```
/ws/tab/{tabID}           # tab ownership + layout commands
/ws/session/{sessionID}   # per-session PTY I/O; ?mode=watch for read-only
```

### CWD tracking

Per-session background poll. Linux: `/proc/<pid>/cwd`. macOS:
`lsof -p <pid> -Fn`. Platform files: `cwd_linux.go`, `cwd_darwin.go`,
`cwd_other.go`.

### Input validation

Every HTTP route that takes a path param validates the ID against
`^[A-Za-z0-9_-]{1,64}$` via `isValidID`. Files derived from IDs
(`scrollback/<id>.raw`, `history/<id>.hist`) depend on that guard — do
not bypass.

## Coding style

- Standard Go formatting. **`gofmt -w` on every changed file**; CI fails
  if `gofmt -l` finds anything.
- `go vet ./...` clean before submit.
- Go tabs, frontend 2-space (see `.editorconfig` if present, otherwise
  match surrounding code).
- Package names: short, lowercase. Exported: `CamelCase`. Unexported:
  `camelCase`. OS-specific files use `_darwin.go` / `_linux.go` /
  `_other.go` suffixes.
- In `ui/`: prefer small, framework-light changes. The stack is plain
  Alpine.js + Twind + vendored xterm.js. No build step.
- **Default to no comments.** Only add a comment when the WHY is
  non-obvious. Don't explain WHAT; the code does that.

## Testing

Seed tests live in `src/cmd/terminal-agent/*_test.go`. Run from `src/`:

```bash
go test ./...
```

New behavior should add focused `*_test.go` coverage. Table-driven tests
for handler / session / state logic. Coverage is light today — adding
meaningful tests is always welcome (see issue #3).

## Commit conventions

- Short imperative subject, lowercase. Prefixes when they clarify the
  category: `chore:`, `fix:`, `sec:`, `docs:`, `release:`.
- Body explains *why*, not *what*. Reference issues when relevant.
- **Create new commits rather than amending.** Pre-commit hooks that
  fail do NOT commit, so `--amend` would modify a *different* commit.
- Never `git push --force` to `main`. Tags push forward; branches don't
  rewrite.

## Ship cadence

- `main` is the default branch. PRs welcome but small changes can be
  pushed directly by the maintainer.
- Releases tagged `v0.x.y` / `v1.x.y` — pushing a tag triggers
  `.github/workflows/release.yml`, which cross-builds 4 platforms and
  cuts a GitHub Release with binaries attached.
- Binary naming at release: `termulaa-<os>-<arch>-v<tag>` (the workflow
  renames `build/build.sh`'s internal `terminal-agent-...` names).

## Files agents commonly touch

| File | Purpose |
|------|---------|
| `src/cmd/terminal-agent/main.go` | Entry point, config, security middleware |
| `src/cmd/terminal-agent/handler.go` | HTTP routes + WebSocket upgraders |
| `src/cmd/terminal-agent/manager.go` | Sessions, tabs, state persistence |
| `src/cmd/terminal-agent/session.go` | PTY, scrollback ring buffer |
| `src/cmd/terminal-agent/tabs.go` | Binary split-tree layout helpers |
| `src/cmd/terminal-agent/ui/app.js` | Frontend — tabs, panes, xterm.js |
| `build/build.sh` | Cross-compile to `dist/<os>/` |
| `.github/workflows/build.yml` | gofmt, go vet, build on push/PR |
| `.github/workflows/release.yml` | Release on tag push |

## Don't do

- Don't introduce new deps lightly. Two is the current total. Adding a
  third needs a real justification.
- Don't add frameworks to the frontend. Plain HTML + Alpine + Twind is
  the aesthetic.
- Don't add auth, TLS, or multi-user features to this repo. Those
  belong in a fork or a separate project — not here (see SECURITY.md).
- Don't "refactor for clarity" alongside a feature change. Split them.
- Don't write long comment blocks or multi-paragraph docstrings. One
  short line max, and only when the WHY is non-obvious.
