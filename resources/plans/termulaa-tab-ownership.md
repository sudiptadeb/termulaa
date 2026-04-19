# Plan: Tab Ownership & Watch/Takeover

## Context

Currently, opening the same tab URL in two browser windows causes both to connect to the same sessions — duplicate input, garbled output. Tabs are just layout JSON with no ownership concept. Sessions can exist without tabs.

This plan makes tabs a real construct with ownership via a dedicated WebSocket, and adds watch/takeover when a tab is already active elsewhere.

## Model Change

### Before
```
Browser → /ws/<sessionID>     (one WS per pane, no tab awareness)
```

### After
```
Browser → /ws/tab/<tabID>           (tab ownership WS — lightweight control channel)
        → /ws/session/<sessionID>   (per-pane PTY I/O, one per pane)
```

- Tab WS = ownership. If connected, tab is "active". On disconnect, tab becomes "inactive".
- Sessions cannot exist without a tab. Creating a session always goes through a tab.
- Deleting a tab kills all its sessions.

## Tab WS Protocol

### Client opens tab:
```
Client → WS /ws/tab/<tabID>
Server checks: is another tab WS connected?
  No  → send: {"type":"connected","layout":{...},"sessions":[...]}
  Yes → send: {"type":"conflict","message":"Tab is active in another window"}
```

### Conflict resolution (client sends one of):
```
{"type":"watch"}      → read-only mode: receive layout updates, session output is read-only
{"type":"takeover"}   → disconnect previous owner, become new owner
{"type":"new"}        → create fresh sessions for all panes, keep layout, become owner
```

### Ongoing tab WS messages:
```
Server → Client:  {"type":"layout","layout":{...}}         (layout changed by split/close/resize)
Server → Client:  {"type":"session_created","id":"..."}     (new session from split)
Server → Client:  {"type":"session_died","id":"...","code":N}
Server → Client:  {"type":"disconnected"}                   (sent to old owner on takeover)

Client → Server:  {"type":"split","pane_session_id":"...","direction":"v"|"h"}
Client → Server:  {"type":"close_pane","session_id":"..."}
Client → Server:  {"type":"update_layout","layout":{...}}   (resize ratio changes)
Client → Server:  {"type":"rename","name":"..."}
```

### Session WS changes:
```
/ws/session/<sessionID>?mode=owner    (default — can write input)
/ws/session/<sessionID>?mode=watch    (read-only — receives output but input is ignored)
```

## Go Changes

### `session.go`
- Add `mode` field to `wsClient`: `"owner"` or `"watch"`
- `WriteInput()`: only accept from owner-mode clients (no change needed — handler controls this)
- Session WS handler checks `?mode=watch` query param, if watch: don't write input to PTY

### `manager.go`
- Add `tabClients map[string]*tabClient` field to `SessionManager`
- `tabClient` struct: `conn *websocket.Conn`, `tabID string`, `mode string` ("owner"|"watch")
- `IsTabActive(tabID) bool` — check if a tab WS is connected
- `DisconnectTabClient(tabID)` — close existing tab WS (for takeover)
- Remove standalone `CreateSession()` from public API — sessions only created via tab operations
- Add `SplitPane(tabID, sessionID, direction)` — creates session + updates layout atomically
- Add `ClosePane(tabID, sessionID)` — kills session + updates layout atomically

### `handler.go`
- Change `/ws/{sessionID}` to `/ws/session/{sessionID}`
- Add `/ws/tab/{tabID}` handler
- Remove `POST /api/sessions` (sessions created via tab WS commands or tab creation)
- Session WS: read `?mode=watch` query param, if watch mode: discard all input messages

### Tab WS handler logic (`handleTabWS`):
```go
func handleTabWS(w, r, mgr) {
    tabID := r.PathValue("tabID")
    tab := mgr.GetTab(tabID)
    if tab == nil { 404; return }

    conn := upgrade(w, r)

    if mgr.IsTabActive(tabID) {
        // Send conflict, wait for resolution
        conn.WriteJSON({"type":"conflict"})
        _, msg := conn.ReadMessage()
        switch msg.type:
            "watch":    mode = "watch"
            "takeover": mgr.DisconnectTabClient(tabID); mode = "owner"
            "new":      replace all sessions in tab with fresh ones; mode = "owner"
    }

    // Register as tab client
    mgr.RegisterTabClient(tabID, conn, mode)
    defer mgr.UnregisterTabClient(tabID, conn)

    // Send initial layout
    conn.WriteJSON({"type":"connected","layout":tab.Layout,"mode":mode})

    // Read loop: handle split/close/rename/layout commands
    for {
        msg := conn.ReadMessage()
        switch msg.type:
            "split": session := mgr.SplitPane(tabID, msg.sessionID, msg.direction)
                     // broadcast layout update to all tab clients (watchers too)
            "close_pane": mgr.ClosePane(tabID, msg.sessionID)
            "update_layout": mgr.UpdateTab(tabID, "", msg.layout)
            "rename": mgr.UpdateTab(tabID, msg.name, nil)
    }
}
```

## JS Changes

### `app.js`
- On page load: connect to `/ws/tab/<tabID>` FIRST
- Wait for response:
  - `connected` → proceed to create panes + connect session WSes
  - `conflict` → show modal: Watch / Take Over / New Sessions
    - On choice: send the command, wait for `connected` response
- Split/close/resize: send commands through tab WS (not direct API calls)
- Session WSes: add `?mode=watch` if in watch mode (input disabled)
- In watch mode: disable `term.onData` and `term.onBinary` handlers, show visual indicator (e.g., subtle "WATCHING" badge)
- `reconstructLayout()`: called after receiving layout from tab WS (not fetched via REST)

### Remove from `app.js`:
- Direct `fetch('/api/tabs/' + TAB_ID)` on init (layout comes from tab WS now)
- `fetch('/api/sessions', {method:'POST'})` on split (tab WS handles it)
- `syncLayout()` via REST (layout sync via tab WS)

### Conflict modal (inline HTML, no framework needed):
```html
<div id="conflict-modal" style="display:none">
  <div class="modal-overlay">
    <div class="modal-box">
      <h2>Tab Active Elsewhere</h2>
      <p>This tab is open in another window.</p>
      <button onclick="resolveConflict('watch')">Watch</button>
      <button onclick="resolveConflict('takeover')">Take Over</button>
      <button onclick="resolveConflict('new')">New Sessions</button>
    </div>
  </div>
</div>
```

## Watch Mode Details

When watching:
- All session WSes connect with `?mode=watch`
- Server sends PTY output normally (viewer sees everything)
- Server ignores any binary input from watch clients
- Terminal `onData`/`onBinary` handlers are no-ops (or disconnected)
- Cursor is hidden or dimmed to signal read-only
- Tab WS still receives layout updates (if owner splits/resizes, watcher sees it)

### Preventing garbled output for watchers:
- Watcher connects to session WS AFTER the session is already running
- No scrollback replay for watchers (same as current live reattach)
- SIGWINCH is sent to trigger screen redraw (already implemented)
- Watcher's xterm gets the same binary stream as owner — no duplication
- The `Broadcast()` function already sends to ALL clients in the list — watcher is just another client in the list

### Data flow (watch mode):
```
PTY output → readPump() → Broadcast() → owner client.send channel → owner xterm
                                       → watch client.send channel → watch xterm
```
Both see identical output. No garbling because it's the same data going to both.

Input flow:
```
Owner types → owner WS → binary msg → WriteInput() → PTY     ✓
Watcher types → watch WS → binary msg → handler drops it      ✗ (ignored)
```

## Takeover Flow

1. New browser sends `{"type":"takeover"}` on tab WS
2. Server sends `{"type":"disconnected","reason":"takeover"}` to old owner's tab WS
3. Server closes old owner's tab WS connection
4. Old owner's session WSes get closed (WS close event)
5. Old owner's app.js shows "Session taken over in another window"
6. New browser becomes owner, connects session WSes normally

## New Sessions Flow

1. New browser sends `{"type":"new"}` on tab WS
2. Server creates fresh sessions for each leaf in the layout tree
3. Server updates the layout with new session IDs
4. Server kills old sessions (if no other tab references them)
5. New browser receives `{"type":"connected","layout":{...}}` with new session IDs
6. New browser connects session WSes to the new sessions

## Files to Modify

| File | Change |
|------|--------|
| `session.go` | Add `mode` field to `wsClient`, handler reads `?mode=watch` to set it |
| `manager.go` | Add `tabClients` map, `IsTabActive`, `DisconnectTabClient`, `RegisterTabClient`, `UnregisterTabClient`, `SplitPane`, `ClosePane` |
| `handler.go` | Add `/ws/tab/{tabID}` handler, change `/ws/{sessionID}` to `/ws/session/{sessionID}`, remove `POST /api/sessions`, add watch mode |
| `app.js` | Tab WS as primary connection, conflict modal, watch mode, remove REST-based split/layout sync |
| `app.css` | Modal styles, watch mode indicator |
| `tab.html` | Add conflict modal HTML |

## Verification Steps

### 1. Normal single-window flow
```
open http://localhost:17380
click New Tab → tab opens with single pane
Cmd+D → splits vertically, new session created
type in both panes → output shows correctly
close browser tab → reopen same tab URL → panes reconnect (SIGWINCH redraw)
```

### 2. Watch mode
```
Window A: open tab, type some commands, start a long-running process (e.g., `top`)
Window B: open same tab URL
  → conflict modal appears: Watch / Take Over / New Sessions
  → click Watch
  → Window B shows same output as Window A (read-only)
  → type in Window B → nothing happens (input ignored)
  → Window A types → both windows show the output
  → Window A splits (Cmd+D) → Window B also shows the new pane
  → close Window B → Window A unaffected
```

### 3. Takeover mode
```
Window A: open tab, type commands
Window B: open same tab URL → conflict modal → click Take Over
  → Window A shows "Session taken over" message, terminal disconnects
  → Window B now has full control, can type normally
  → Window A can reload → gets conflict modal again (Window B is now owner)
```

### 4. New Sessions mode
```
Window A: open tab with 2 panes, run `pwd` in each
Window B: open same tab URL → conflict modal → click New Sessions
  → Window B gets same 2-pane layout but fresh shells (new PWD = HOME)
  → Window A still has its sessions running (A is still connected)
  → Both windows work independently with different sessions
```

### 5. Tab close + agent restart
```
open tab, type commands
kill agent (Ctrl+C) → restart agent
open same tab URL → dead sessions auto-revive with scrollback
  → no conflict modal (no active tab WS)
```

### 6. No garbled output in watch mode
```
Window A: run `claude` (TUI app with full-screen redraws)
Window B: watch the tab
  → TUI renders correctly in both windows
  → No duplicate characters or garbled escape sequences
  → Window A interacts with Claude Code → both windows show responses
```
