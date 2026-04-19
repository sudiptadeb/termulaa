# Terminal Agent v2 — Persistent Sessions Design

## Terminology

| Term | What it is |
|------|-----------|
| **Session** | A PTY + scrollback + cwd. Backend-only, never shown directly to user. |
| **Tab** | A named layout of panes. What the user sees and manages. |
| **Pane** | A slot in a tab's binary split tree. Points to a session ID. |

## Data Model

```
Tab {
  id:         string      // uuid
  name:       string      // user-editable, default "Terminal N"
  layout:     Node        // root of binary split tree
  created:    timestamp
  lastActive: timestamp
}

Node = Split | Leaf

Split {
  direction: "v" | "h"   // vertical = side-by-side, horizontal = top/bottom
  ratio:     float        // 0-1, first child's share
  first:     Node
  second:    Node
}

Leaf {
  sessionID: string
}

Session {
  id:         string      // uuid
  pid:        int         // shell PID (0 if dead)
  cwd:        string      // last known working directory
  shell:      string      // e.g. /opt/homebrew/bin/bash
  cols, rows: int
  alive:      bool
  created:    timestamp
  lastOutput: timestamp
  historyFile: string     // path to per-session shell history
}
```

## Persistence

```
~/.termulaa/
  state.json              # tabs + session metadata (small, ~KB)
  scrollback/
    <sessionID>.raw       # raw terminal output per session (capped ~1MB each)
  history/
    <sessionID>           # per-session shell history (HISTFILE)
```

### Save triggers
- Tab create / close / rename
- Split / close pane
- Every 5s for scrollback + cwd (periodic)
- On graceful agent shutdown (SIGINT/SIGTERM)

### Scrollback format
Raw bytes from PTY output, ring buffer capped at 1MB per session. Not JSON — just the byte stream. On replay, sent as-is to xterm.js which parses ANSI codes natively.

### CWD tracking
Poll `/proc/PID/cwd` (Linux) or `lsof -p PID -Fn` (macOS) every 5s per live session.

### Shell history per session
Each session gets its own history file at `~/.termulaa/history/<sessionID>`. On session creation, the agent sets `HISTFILE=~/.termulaa/history/<sessionID>` in the shell's environment. This gives each pane independent up-arrow history. On revival, the same `HISTFILE` is set so history carries over across agent restarts.

## Agent serves UI

All HTML/CSS/JS embedded in the Go binary via `embed`. No build step.

**Frontend stack:**
- **Alpine.js** — reactive UI (tab list, settings page), vendored (~15KB)
- **Twind** — Tailwind-in-the-browser, vendored (~13KB). Same utility classes, zero build.
- **xterm.js + addons** — terminal rendering, already vendored
- No bundler, no npm, no build step — just script tags

```
localhost:17380/              → Landing page (tab list)
localhost:17380/tab/<id>      → Tab view (split layout with terminals)
localhost:17380/settings      → Settings page
```

## API

```
GET  /api/tabs                → [{id, name, paneCount, alive, lastActive}]
POST /api/tabs                → Create tab → {id}
PUT  /api/tabs/:id            → Update (name, layout)
DELETE /api/tabs/:id          → Close tab + kill its sessions

GET  /api/sessions/:id        → {id, alive, cwd, cols, rows}
POST /api/sessions            → Create session → {id}
DELETE /api/sessions/:id      → Kill session

WS   /ws/<sessionID>          → Attach to session
```

## WebSocket Protocol

Session ID is in the URL path. On connect:

```
Server → Client:  {"type":"scrollback","data":"<base64 raw bytes>"}
Server → Client:  binary frames (live PTY output, ongoing)
Client → Server:  binary frames (keystrokes)
Client → Server:  {"type":"resize","cols":N,"rows":N}
Client → Server:  {"type":"file","name":"...","data":"<base64>"}
```

## Page Flow

1. Open `localhost:17380` → tab list (green/red dots for alive/dead)
2. Click tab → `/tab/<id>` → JS fetches layout via API, creates split panes, connects each pane WebSocket to `/ws/<sessionID>`
3. Close browser tab → WebSockets close, sessions stay alive, agent keeps PTYs open
4. Reopen → tab still in list, click to reattach seamlessly
5. Agent restart → tabs shown with red dot, click → auto-revive all sessions (new shell in saved cwd, scrollback replayed above fresh prompt)

## Revival (after agent restart)

When opening a tab with dead sessions:
1. Load scrollback from `~/.termulaa/scrollback/<sessionID>.raw`
2. Start new shell in saved `cwd`
3. Send scrollback to client first, then live PTY output
4. User sees old output above the fresh prompt — seamless experience

## Go Package Structure

```
src/cmd/termulaa/
  main.go           # HTTP server, embed UI, signal handling, load/save state
  session.go        # Session struct, PTY lifecycle, scrollback buffer
  manager.go        # SessionManager: map[id]*Session, create/kill/revive
  tabs.go           # Tab/Layout data model, CRUD, persistence
  handler.go        # HTTP handlers (API + WS)
  cwd.go            # CWD polling (platform-specific)
  ui/               # Embedded static files (go:embed)
    index.html      # Landing page — tab list (Alpine.js)
    tab.html        # Tab view — split layout with terminals
    settings.html   # Settings page (Alpine.js)
    app.js          # Split management, WS handling, shared utils
    app.css         # Dark theme styles
    lib/
      alpine.min.js           # Alpine.js (vendored, ~15KB)
      twind.min.js            # Twind (vendored, ~13KB) — Tailwind classes in browser
      xterm.min.js            # xterm.js 5.3.0 (already have)
      xterm.css               # xterm styles (already have)
      xterm-addon-fit.min.js  # fit addon (already have)
      xterm-addon-webgl.min.js # WebGL addon (already have)
```

## What changes from v1

| v1 (current) | v2 |
|---|---|
| Extension-only UI | Agent serves HTML directly |
| 1 WS = 1 PTY, dies on close | Session stays alive after WS close |
| No persistence | Tabs + sessions saved to disk |
| No session list | Landing page shows tabs |
| No reconnect | Scrollback replay on reattach |
| No cwd tracking | Periodic cwd poll |
| No revival | Dead sessions auto-revive on tab open |

## Configuration

Stored in `~/.termulaa/config.json`. Editable via settings page in UI.

```json
{
  "port": 17380,
  "shell": "",

  "deleteOnClose": false,
  "cleanupAfterDays": 30,
  "scrollbackLimit": "1MB",
  "historyLimit": 10000,

  "defaultCols": 120,
  "defaultRows": 30,
  "cwdPollInterval": "5s",
  "savePeriod": "5s"
}
```

| Setting | Default | Description |
|---------|---------|-------------|
| `port` | 17380 | Agent listen port |
| `shell` | `""` (auto-detect) | Shell binary path. Empty = auto-detect (homebrew bash > system bash > sh) |
| `deleteOnClose` | false | Delete tab data (sessions, scrollback, history) when browser tab is closed. Detected via WebSocket `beforeunload` + no reconnect within 5s grace period. |
| `cleanupAfterDays` | 30 | Auto-delete dead tab data older than N days. 0 = never auto-delete. Runs on agent startup. |
| `scrollbackLimit` | `"1MB"` | Max scrollback per session. Accepts KB/MB suffixes. Ring buffer — oldest output discarded. 0 = no scrollback. |
| `historyLimit` | 10000 | Max shell history lines per session. 0 = no per-session history (uses default HISTFILE). |
| `defaultCols` | 120 | Default terminal columns for new sessions |
| `defaultRows` | 30 | Default terminal rows for new sessions |
| `cwdPollInterval` | `"5s"` | How often to poll each session's working directory |
| `savePeriod` | `"5s"` | How often to flush scrollback + state to disk |

### Tab close detection

When `deleteOnClose` is true:
1. Browser fires `beforeunload` → JS sends `{"type":"closing"}` on all WebSockets
2. Agent marks those sessions as "pending close"
3. If no WebSocket reconnects within 5s grace period → delete tab + sessions + files
4. If reconnect happens (e.g., page refresh) → cancel deletion

This avoids accidental data loss from page refreshes while still honoring intentional tab closes.

### Settings API

```
GET  /api/settings          → current config
PUT  /api/settings          → update config (partial merge, saves to disk)
```

## Not in v2

- Auth / multi-user
- Remote access (localhost only)
- Session recording / playback
- Tab reordering in UI
- Search in scrollback
- Windows support
