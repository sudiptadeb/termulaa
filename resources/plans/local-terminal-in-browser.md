# Local Terminal in Browser Tab — Implementation Plan

> **Goal**: Chrome extension + local native agent that renders xterm.js in a browser tab, connected to a local PTY. Prototype to validate feasibility for AI coding tools (Claude Code, Codex CLI, OpenCode).

---

## 1. Architecture Overview

```
+---------------------------+          +---------------------------+
|   Ulaa Browser (Chromium) |          |   Local Native Agent      |
|                           |          |   (Go binary)             |
|  +---------------------+ |   WS     |  +---------------------+  |
|  | Extension Page       |<---------->|  | WebSocket Server    |  |
|  | (chrome-extension:/) | | :17380  ||  | localhost:17380      |  |
|  |                      | |          |  +---------------------+  |
|  |  xterm.js + WebGL    | |          |  | PTY Manager         |  |
|  |  + fit + unicode11   | |          |  | (creack/pty | conpty)|  |
|  +---------------------+ |          |  +---------------------+  |
|                           |          |  | Session Recorder    |  |
|  +---------------------+ |  NM      |  | (Asciicast v2)      |  |
|  | Service Worker       |<---------->|  +---------------------+  |
|  | (lifecycle mgmt)     | | bootstrap|                           |
|  +---------------------+ |          +---------------------------+
+---------------------------+
```

**Three components**:

| Component | Role | Language |
|-----------|------|----------|
| **Native Agent** (`ulaa-terminal-agent`) | Local WebSocket server, PTY spawning, session recording | Go |
| **Extension Page** (`terminal.html`) | xterm.js rendering, WebSocket client, tab lifecycle | JS (MV3) |
| **Service Worker** (`background.js`) | Agent lifecycle, native messaging bootstrap, tab tracking | JS (MV3) |

### Why This Architecture

- **Extension page, not content script**: `chrome-extension://` pages have full extension API access, no CSP conflicts, no DOM collision. They behave like real tabs and integrate with Ulaa workspaces.
- **WebSocket over native messaging**: Native messaging is JSON-framed and synchronous — incompatible with raw PTY byte streams. WebSocket gives binary frames, backpressure, and matches our existing xterm.js protocol.
- **Native messaging for bootstrap only**: Used once at startup to exchange a one-time auth token, then WebSocket takes over. This avoids the "random localhost app can connect" problem.

---

## 2. Component Breakdown

### 2.1 Native Agent (`src/cmd/terminal-agent/`)

A standalone Go binary that runs on the user's machine. No Docker, no TLS, no JWT — this is local-only.

**Responsibilities**:
- Listen on `localhost` WebSocket (reject non-loopback connections)
- Spawn PTY sessions (one per WebSocket connection)
- Manage shell lifecycle (start, resize, signal, kill)
- Record sessions in Asciicast v2 format (reuse `recorder.go` logic)
- Authenticate connections via one-time token (issued during native messaging bootstrap)
- Self-terminate when no active sessions and no heartbeat from extension

**Key design decisions**:

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Port selection | Dynamic (OS-assigned), reported via native messaging | Avoids port conflicts. Agent binds `:0`, OS assigns port, agent sends port back to extension via native messaging response. |
| Auth model | One-time token per agent launch | Extension generates `crypto.randomUUID()`, passes via native messaging. Agent accepts only the first WebSocket connection bearing this token. Subsequent connections require a new token exchange. |
| Shell | User's default shell (`$SHELL` / `COMSPEC`) | Not bash-hardcoded. Respects user's zsh/fish/pwsh preference. |
| Working directory | `$HOME` or CWD passed by extension | Extension can specify per-tab working directory. |
| PTY buffer | 16KB read buffer (vs. 1KB in remote terminal) | Larger buffer for heavy AI tool output. Reduces syscall overhead. |
| Windows PTY | `conpty` via `golang.org/x/sys/windows` | `creack/pty` is Unix-only. Windows needs `conpty` API calls. |
| Lifecycle | On-demand, self-terminating | Extension launches via native messaging. Agent exits after 30s idle (no sessions, no heartbeat). No system service, no daemon. |

**File structure**:
```
src/cmd/terminal-agent/
    main.go              # Entry, flag parsing, native messaging bootstrap, WebSocket server
    pty_unix.go          # Unix PTY spawning (creack/pty), build tag: !windows
    pty_windows.go       # Windows conpty, build tag: windows
    session.go           # Session struct, I/O loops (reuse protocol from service/terminal)
    auth.go              # Token validation, loopback enforcement
    recorder.go          # Asciicast v2 recording (extracted from service/terminal/recorder.go)
```

### 2.2 Extension Page (`src/extension/secure_access/terminal/`)

A full-page xterm.js terminal that opens as a browser tab.

**Responsibilities**:
- Render terminal via xterm.js + WebGL addon
- Connect to agent's WebSocket, authenticate with one-time token
- Handle tab lifecycle (close tab = kill PTY)
- Support multiple terminal tabs (each gets its own WebSocket + PTY)
- Expose minimal UI: tab title shows CWD, reconnect on agent restart

**xterm.js stack**:
```
xterm.js 5.3.0              # Core terminal emulator
@xterm/addon-webgl          # GPU-accelerated rendering (critical for performance)
@xterm/addon-fit            # Auto-resize to container
@xterm/addon-unicode11      # Full Unicode support (emoji in AI tool output)
@xterm/addon-serialize      # Terminal state snapshot (for tab restore, optional)
```

**Why WebGL from day one**: Without WebGL, xterm.js uses canvas2d which repaints the entire viewport on every frame. WebGL only updates changed cells. For Claude Code streaming hundreds of lines/sec, this is the difference between smooth and janky. VS Code's terminal uses the same approach.

**File structure**:
```
src/extension/secure_access/terminal/
    terminal.html         # Full-page terminal (minimal HTML, loads JS/CSS)
    terminal.js           # xterm.js init, WebSocket client, resize handling, lifecycle
    terminal.css          # Terminal container styling (full viewport, no chrome)
    lib/                  # Vendored xterm.js + addons (extension can't use CDN)
        xterm.js
        xterm.css
        addon-webgl.js
        addon-fit.js
        addon-unicode11.js
```

### 2.3 Service Worker Updates (`background.js`)

Additions to the existing secure_access service worker.

**Responsibilities**:
- Launch native agent on demand (first terminal tab open)
- Bootstrap auth: generate token, send to agent via native messaging, receive port
- Track terminal tabs, pass connection info (port + token) to extension pages
- Kill agent when last terminal tab closes (or let it self-terminate)
- Handle agent crashes: detect WebSocket disconnect, offer "Restart" button

**Native messaging flow** (one-time per agent launch):
```
Extension                          Agent Binary
   |                                    |
   |--- connectNative("ulaa_terminal") --->|
   |                                    | (agent starts, binds :0)
   |<-- { "port": 17380 } -------------|
   |--- { "token": "abc-123-..." } --->|
   |<-- { "ready": true } -------------|
   |                                    |
   | (native messaging port stays open  |
   |  for heartbeat / lifecycle only)   |
   |                                    |
   | Extension opens WebSocket to       |
   | ws://127.0.0.1:17380/terminal?     |
   |   token=abc-123-...                |
```

---

## 3. WebSocket Protocol

Reuse the exact protocol from the remote terminal service (`service/terminal/session.go`). The extension page already speaks this protocol — no translation layer needed.

| Direction | Type | Format | Purpose |
|-----------|------|--------|---------|
| Client -> Agent | Binary | Raw bytes | Keyboard input → PTY stdin |
| Agent -> Client | Binary | Raw bytes (16KB chunks) | PTY stdout → terminal |
| Client -> Agent | Text | `{"type":"resize","cols":N,"rows":N}` | Terminal resize |
| Client -> Agent | Text | `{"type":"signal","signal":"SIGINT"}` | Send signal to shell |
| Agent -> Client | Text | `{"type":"title","title":"..."}` | Update tab title (CWD changes) |
| Agent -> Client | Text | `{"type":"exit","code":N}` | Shell exited |

**Additions over remote protocol**:
- `signal` message: lets the extension send SIGINT/SIGTSTP without relying on terminal escape codes
- `title` message: agent watches PTY for OSC title sequences, forwards to extension to update tab title
- `exit` message: clean notification that the shell process has exited (vs. just closing the WebSocket)

---

## 4. Security Model

### Threat: Random local app connects to agent

**Mitigation**: One-time token bootstrapped via native messaging.
- Native messaging is extension-ID-locked (Chrome enforces this at the OS level via the native host manifest)
- Token is `crypto.randomUUID()` — 128 bits of entropy
- Agent validates token on WebSocket upgrade, rejects all connections without valid token
- Token is single-use per session (not per connection — allows reconnect within same agent lifecycle)

### Threat: Non-localhost connection to agent

**Mitigation**: Agent binds to `127.0.0.1` only + validates `RemoteAddr` on every connection.
```go
listener, _ := net.Listen("tcp", "127.0.0.1:0")
// In WebSocket upgrade handler:
if !isLoopback(r.RemoteAddr) {
    http.Error(w, "forbidden", 403)
    return
}
```

### Threat: Agent persists after browser closes

**Mitigation**: Heartbeat + idle timeout.
- Extension sends heartbeat every 10s over native messaging port
- If no heartbeat for 30s AND no active sessions, agent exits
- Native messaging port EOF (browser crash) also triggers shutdown timer

### Local = Trusted

The shell is NOT sandboxed. This is the user's own machine running their own shell — same as opening Terminal.app or Windows Terminal. Sandboxing would break the primary use case (AI coding tools need full filesystem/network access).

The remote terminal service uses Docker isolation because it runs on a shared server. Different threat model entirely.

---

## 5. Performance Analysis & Benchmarks

### 5.1 Can xterm.js + WebGL Match Native Terminals?

**Short answer**: Yes, for this use case. Here's why:

| Factor | Native (Alacritty) | xterm.js + WebGL | Gap |
|--------|-------------------|-------------------|-----|
| Rendering | Custom OpenGL | WebGL via Chrome's GPU process | ~Same GPU path |
| Text shaping | HarfBuzz direct | Chrome's text shaper | Chrome's is battle-tested |
| Input latency | ~2ms | ~5-8ms (JS event loop + IPC) | Imperceptible for typing |
| Throughput | 500+ MB/s render | ~80-150 MB/s render | Sufficient — PTY is the bottleneck |
| Memory baseline | ~30MB | ~80MB (tab overhead) | Acceptable |

**Why throughput gap doesn't matter**: The PTY itself maxes out at ~200-400 MB/s on modern hardware. `cat` of a 10MB file through a PTY takes ~50ms. xterm.js at 100 MB/s render throughput handles this fine. The real bottleneck is always the application generating output (Claude Code, cargo build), not the terminal.

**VS Code's terminal uses xterm.js + WebGL** in production with millions of users. This validates the approach.

### 5.2 Benchmark Targets

| Metric | Target | How to Measure |
|--------|--------|----------------|
| Keystroke-to-echo latency | < 10ms p99 | Inject keystroke timestamp, measure echo round-trip via agent instrumentation |
| Throughput (sustained) | > 50 MB/s displayed | `cat` large file, measure time to final newline in xterm.js |
| Throughput (burst) | > 100 MB/s peak | `yes \| head -1000000` — measures raw line throughput |
| Frame rate under load | 60 FPS during Claude Code streaming | Chrome DevTools Performance panel, watch for dropped frames |
| Memory after 1hr idle | < 120 MB (tab + agent) | Chrome Task Manager + `ps` for agent process |
| Memory after 1hr active | < 200 MB | Same, during active Claude Code session |
| Reconnect time | < 500ms | Kill agent, measure time to "reconnected" state |
| Agent startup time | < 200ms | Timestamp from native messaging connect to WebSocket ready |

### 5.3 Test Scenarios

| # | Scenario | What It Tests | Pass Criteria |
|---|----------|---------------|---------------|
| 1 | `cat /dev/urandom \| base64 \| head -100000` | Raw throughput, no output parsing | > 50 MB/s, no frame drops |
| 2 | `yes \| head -500000` | Rapid small-line output | Smooth scrolling, < 200MB memory |
| 3 | Run Claude Code, generate a 500-line file | Real-world AI tool streaming | Visually smooth, no input lag during output |
| 4 | Run `cargo build` on large Rust project | Heavy interleaved stdout/stderr + ANSI colors | Correct rendering, responsive input |
| 5 | `npm install` in large project | Progress bars, cursor movement, \r overwrites | Correct rendering of progress indicators |
| 6 | Open vim, edit file, `:wq` | Full-screen TUI application | All keybindings work, no rendering artifacts |
| 7 | Run `htop` for 1 hour | Long-running full-screen app, memory stability | Memory stays under 200MB, no leaks |
| 8 | 5 terminal tabs simultaneously | Multi-session resource usage | < 500MB total, no cross-tab interference |
| 9 | Type during heavy `cat` output | Input responsiveness under load | Keystrokes echoed within 20ms during output flood |
| 10 | Close tab during active session | Cleanup correctness | PTY killed within 2s, agent reports session end |

### 5.4 Comparison Methodology

Run scenarios 1-6 on each:
- **Alacritty** (native baseline)
- **Windows Terminal** (Windows baseline)
- **VS Code integrated terminal** (xterm.js + WebGL baseline)
- **Our prototype** (xterm.js + WebGL + local agent)

Measure: latency, throughput, memory, CPU, dropped frames. Document in `resources/bench-results/local-terminal/`.

---

## 6. Tech Stack Summary

| Component | Technology | Version | Rationale |
|-----------|-----------|---------|-----------|
| Native agent | Go | 1.23+ | Team expertise, cross-compilation, `creack/pty` already in deps |
| WebSocket server | `gorilla/websocket` | 1.5.3 | Already in `go.mod`, proven in remote terminal |
| Unix PTY | `creack/pty` | 1.1.24 | Already in `go.mod`, proven in remote terminal |
| Windows PTY | `conpty` via `x/sys/windows` | — | Only option for Windows PTY (Go) |
| Terminal emulator | xterm.js | 5.3.0 | Already used in remote terminal, industry standard |
| GPU rendering | `@xterm/addon-webgl` | latest | Required for AI tool output performance |
| Extension | Chrome MV3 | — | Already using MV3 in secure_access extension |
| Native messaging | Chrome native messaging | — | Only secure way to bootstrap local agent comms |
| Session recording | Asciicast v2 | — | Already implemented in `service/terminal/recorder.go` |

---

## 7. Folder Structure (Full Prototype)

```
src/
    cmd/
        terminal-agent/
            main.go              # Entry point, native messaging bootstrap, WS server
            pty_unix.go          # Unix PTY (creack/pty), !windows build tag
            pty_windows.go       # Windows conpty, windows build tag
            session.go           # Per-connection session, I/O loops
            auth.go              # Token validation, loopback check
            recorder.go          # Asciicast v2 writer (extracted from service/terminal)
            README.md            # Build, install, usage
    extension/
        secure_access/
            terminal/
                terminal.html    # Full-page terminal UI
                terminal.js      # xterm.js init, WebSocket, lifecycle
                terminal.css     # Styling
                lib/             # Vendored xterm.js + addons
                    xterm.js
                    xterm.css
                    addon-webgl.js
                    addon-fit.js
                    addon-unicode11.js
            background/
                policies/
                    terminal_agent.js   # Agent lifecycle, native messaging, tab tracking
            manifest.json        # Add nativeMessaging permission, terminal.html as web_accessible

resources/
    native-messaging/
        com.ulaa.terminal.json           # macOS/Linux native host manifest
        com.ulaa.terminal.windows.json   # Windows native host manifest
        install-host.sh                  # Register native messaging host (macOS/Linux)
        install-host.bat                 # Register native messaging host (Windows)
    bench-results/
        local-terminal/                  # Benchmark results go here

build/
    build.sh                             # Add terminal-agent component
```

---

## 8. Implementation Phases

### Phase 1: Agent + Basic Terminal (1 week)

**Goal**: xterm.js in a browser tab connected to a local PTY. No auth, no lifecycle management, no recording. Just prove the data path works.

**Tasks**:
1. Create `src/cmd/terminal-agent/main.go` — hardcoded port (17380), no auth
2. `pty_unix.go` — spawn `$SHELL` with `creack/pty`, 16KB buffer
3. `session.go` — WebSocket ↔ PTY I/O loops (reuse protocol from `service/terminal/session.go`)
4. Create `terminal.html` + `terminal.js` — xterm.js + WebGL + fit + unicode11
5. Add `terminal.html` to extension manifest as extension page
6. Add "Open Terminal" context menu / keyboard shortcut to extension
7. Manual testing: open tab, type commands, resize window, run vim

**Deliverable**: Working terminal tab on macOS/Linux. Agent started manually from CLI.

**Effort**: ~5 days

### Phase 2: Native Messaging Bootstrap + Auth (3-4 days)

**Goal**: Extension launches agent automatically, connection is authenticated.

**Tasks**:
1. Add native messaging host manifest (`com.ulaa.terminal.json`)
2. Create `install-host.sh` / `install-host.bat` — register with Chrome
3. `auth.go` — token validation on WebSocket upgrade
4. `terminal_agent.js` — service worker: launch agent, exchange token, get port
5. `terminal.js` — get connection info from service worker, connect with token
6. Agent self-termination on idle (30s no sessions + no heartbeat)

**Deliverable**: Extension auto-launches agent, authenticated WebSocket connection.

**Effort**: ~4 days

### Phase 3: Windows Support (3-4 days)

**Goal**: Terminal works on Windows with conpty.

**Tasks**:
1. `pty_windows.go` — conpty API via `golang.org/x/sys/windows`
2. Test with PowerShell and cmd.exe as default shells
3. Windows native messaging host registration (registry-based)
4. Test with Windows Terminal as comparison baseline

**Deliverable**: Cross-platform agent (macOS, Linux, Windows).

**Effort**: ~4 days

### Phase 4: Session Recording + Polish (2-3 days)

**Goal**: Asciicast recording, tab title updates, reconnect, multi-tab.

**Tasks**:
1. Extract `recorder.go` from `service/terminal/` into shared package or copy
2. Wire recorder as session subscriber (same pattern as remote terminal)
3. Add `title` message — agent parses OSC title sequences from PTY output
4. Add reconnect logic — extension detects WebSocket close, shows "Reconnect" button
5. Multi-tab support — each tab gets independent session, service worker tracks all
6. Tab close cleanup — extension sends close message, agent kills PTY

**Deliverable**: Production-quality terminal experience with recording.

**Effort**: ~3 days

### Phase 5: Benchmarking + Validation (2-3 days)

**Goal**: Run all benchmark scenarios, compare against native terminals, decide if xterm.js + WebGL is "good enough" or if we need native widget embedding.

**Tasks**:
1. Build benchmark harness (Go tool that drives terminal scenarios)
2. Run all 10 test scenarios on macOS + Windows
3. Run comparison against Alacritty, Windows Terminal, VS Code terminal
4. Document results in `resources/bench-results/local-terminal/`
5. Write go/no-go recommendation based on results

**Deliverable**: Benchmark report with quantified comparison. Decision on whether to proceed with xterm.js or explore native widget.

**Effort**: ~3 days

---

## 9. Build Integration

Add to `build/build.sh`:
```bash
"terminal-agent")
    build_all_platforms "terminal-agent" "./cmd/terminal-agent"
    ;;
```

**Build**: `build/build.sh terminal-agent`
**Output**: `dist/linux/terminal-agent-amd64-v0.1.0`, `dist/darwin/terminal-agent-arm64-v0.1.0`, etc.

---

## 10. Native Messaging Host Manifest

### macOS/Linux (`com.ulaa.terminal.json`)
```json
{
  "name": "com.ulaa.terminal",
  "description": "Ulaa Local Terminal Agent",
  "path": "/usr/local/bin/ulaa-terminal-agent",
  "type": "stdio",
  "allowed_origins": [
    "chrome-extension://<extension-id>/"
  ]
}
```

**Install location**:
- macOS: `~/Library/Application Support/Google/Chrome/NativeMessagingHosts/` (per-user) or `/Library/Google/Chrome/NativeMessagingHosts/` (system)
- Linux: `~/.config/google-chrome/NativeMessagingHosts/` (per-user)
- Ulaa (Chromium fork): May use custom path — **needs investigation**

### Windows (Registry)
```
HKCU\Software\Google\Chrome\NativeMessagingHosts\com.ulaa.terminal
  (Default) = "C:\path\to\com.ulaa.terminal.json"
```

> **Open question**: Ulaa as a Chromium fork may use a different registry path / config directory. Need to confirm with the Ulaa browser team.

---

## 11. Known Risks & Open Questions

### Risks

| Risk | Impact | Mitigation |
|------|--------|------------|
| xterm.js WebGL not performant enough for heavy AI tools | Prototype fails validation | Phase 5 benchmarks will catch this early. Fallback: native terminal widget via Chromium embedding API (significant effort) |
| conpty API complexity on Windows | Windows support delayed | Isolate in `pty_windows.go` behind clean interface. Can ship macOS/Linux first. |
| Native messaging host path differs in Ulaa fork | Agent won't launch | Coordinate with Ulaa browser team early. May need custom path discovery. |
| Agent process leak (user closes browser uncleanly) | Orphan processes accumulate | Heartbeat + idle timeout handles this. Also: agent writes PID file, checks on startup. |
| Multiple browser profiles = multiple agents | Port conflicts, resource waste | Each profile gets its own agent instance (different native messaging channel). Port is dynamic, so no conflicts. |

### Open Questions

1. **Ulaa workspace integration**: How does the extension tell Ulaa to group a terminal tab into a workspace? Is there a `chrome.ulaaInternal` API for this? **Action**: Ask Ulaa browser team.

2. **Ulaa native messaging paths**: Does Ulaa use Chrome's native messaging host paths or custom ones? **Action**: Test with Ulaa browser, check Chromium fork customization.

3. **Extension distribution**: Is the terminal agent bundled with the extension (via extension installer) or distributed separately? **Recommendation**: Separate installer initially (simpler), bundle later via Ulaa's native installer.

4. **Shell environment**: Should the agent inherit the user's full shell environment (`.bashrc`, `.zshrc`)? **Recommendation**: Yes — spawn a login shell (`bash -l`, `zsh -l`) so the user gets their aliases, PATH, etc.

5. **Session persistence across browser restart**: Should terminal state survive browser restart? **Recommendation**: Defer. Agent self-terminates, sessions are ephemeral. If needed later, serialize xterm.js state with the serialize addon.

6. **Copy/paste**: xterm.js handles Cmd+C/Ctrl+C as SIGINT. Copy needs Cmd+Shift+C or right-click. **Recommendation**: Follow VS Code's convention — detect selection state, Cmd+C copies when text is selected, sends SIGINT when not.

7. **Scrollback buffer size**: Default xterm.js scrollback is 1000 lines. AI tools can produce thousands of lines. **Recommendation**: Set to 10,000 lines initially, make configurable. Monitor memory impact in benchmarks.

---

## 12. What We're Explicitly Deferring

| Feature | Why Deferred |
|---------|-------------|
| Tabs/splits within terminal page | Prototype validates single-terminal-per-tab. Splitting is UI polish. |
| Theme customization | Hardcode dark theme. Theming is trivial to add later. |
| Shell profiles (choose bash/zsh/fish) | Use `$SHELL` default. Profile selection is UI work. |
| Ulaa workspace integration | Requires Ulaa-specific APIs we haven't confirmed yet. |
| Remote terminal unification | Remote (Docker) and local (PTY) are different enough to stay separate for now. |
| Auto-update of agent binary | Manual install for prototype. Auto-update is distribution concern. |
| Search in terminal output | xterm.js has a search addon. Add when users ask for it. |
| Image protocol (iTerm2/Kitty) | xterm.js has an image addon. Not needed for prototype validation. |

---

## 13. Estimated Total Effort

| Phase | Duration | Cumulative |
|-------|----------|------------|
| Phase 1: Agent + Basic Terminal | 5 days | 5 days |
| Phase 2: Native Messaging + Auth | 4 days | 9 days |
| Phase 3: Windows Support | 4 days | 13 days |
| Phase 4: Recording + Polish | 3 days | 16 days |
| Phase 5: Benchmarking | 3 days | 19 days |
| **Total** | **~4 weeks** | With buffer for unknowns |

Phases 3 and 4 can run in parallel if two engineers are available, bringing total to ~3 weeks.

---

## 14. Success Criteria for Prototype

The prototype is successful if:

1. **Keystroke latency** < 10ms p99 (measured, not "feels fast")
2. **Claude Code streaming** is visually smooth at 60 FPS (no dropped frames during large diff output)
3. **`cat` 10MB file** completes in < 2s with smooth scrolling
4. **vim/htop** render correctly with no artifacts
5. **Memory** stays under 200MB after 1hr active session
6. **Windows + macOS + Linux** all work (Phase 3 complete)
7. **No orphan processes** after browser crash/close (heartbeat works)

If criteria 1-5 are met, xterm.js + WebGL is validated. If not, the benchmark report (Phase 5) will contain the specific numbers showing where it falls short, informing the decision on native widget embedding.
