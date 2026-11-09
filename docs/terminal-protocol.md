# termulaa terminal protocol for non-browser clients, version 1

This document specifies what a client that is not termulaa's own web UI
must speak to drive a terminal on a machine running termulaa: the HTTP
calls that create a tab and the two WebSockets that own it and carry its
PTY. It covers both a client on the same machine (loopback) and a client
reaching the machine through a rendezvous implementing
[rc-protocol.md](rc-protocol.md). The reference client is
`termulaa shell` / `termulaa exec` (`src/internal/termclient`); an app on
another platform implements this document, not that code.

The key words MUST, MUST NOT, SHOULD, and MAY are to be interpreted as
described in RFC 2119.

## 1. Mental model

termulaa keeps **sessions** (a PTY plus a terminal emulator mirroring
what it shows) grouped into **tabs**. A tab has one **owner** at a time:
the client holding its tab WebSocket. A session can be attached by a
client only while that client owns the tab containing it; the attach is
authorised by the **owner token** the tab socket handed out.

```
client ──HTTP──> POST api/tabs                       create a tab (and its session)
client ──WS────> ws/tab/<tab>            ──> {"type":"connected", token, sessions}
client ──WS────> ws/session/<sid>?tab_id&token  ──> PTY bytes both ways
```

Two WebSockets, both kept open for the life of the attachment. The tab
socket is not optional: when it closes, the token dies with it.

A session has two faces a client can attach to:

- the **picture**: a snapshot of the terminal emulator (scrollback, both
  screens, cursor, modes) as an ANSI byte stream, sized for the
  client's terminal. For a client that renders a terminal.
- the **stream**: the raw PTY output bytes, addressable by offset. For a
  client that consumes output, such as a command runner. Resumable.

## 2. Reaching the machine

### 2.1 Loopback

A client on the machine itself uses `http://127.0.0.1:<port>/` with the
paths below. termulaa checks `Host` (loopback only) and, when present,
`Origin`; a client MUST either omit `Origin` or send a loopback origin.

### 2.2 Through a rendezvous

A rendezvous (reference: memd) proxies the machine under a **base URL**
and authenticates the viewer with its own account. For the reference
rendezvous:

1. **Pair** once. A person signed in to the rendezvous dashboard mints a
   one-time pairing code. The client redeems it, with no other
   credential:

   ```
   POST <server>/api/app/redeem
   {"code": "<code>", "label": "<this device's name>"}
   → 200 {"token": "<app token>"}      401 invalid or used code, 429 too many tries
   ```

   The app token is long-lived and revocable from the dashboard. The
   client MUST store it with owner-only permissions and MUST NOT print
   it.

2. **Sign in** at the start of every run (and after any 401):

   ```
   POST <server>/api/app/session
   Authorization: Bearer <app token>
   → 200 + Set-Cookie: <session cookie>      401/403 token revoked: pair again
   ```

   Everything that follows carries the cookie, on HTTP calls and on
   WebSocket upgrades. The bearer token itself authenticates nothing but
   this call.

3. **List machines**:

   ```
   GET <server>/rc/api/agents
   → 200 {"agents":[{"id","label","port","tunnels","connected_at","url"}], "view_host"}
   ```

   Only machines connected right now appear. `url` is the machine's
   base path, `/rc/t/<64 hex>/`, present when the rendezvous serves
   terminals under a path prefix (§6.1 of rc-protocol.md). A rendezvous
   using a dedicated view host gives no `url`; this document does not
   cover that deployment.

4. Use `<server><url>` as the **base URL** for everything in §3 and §4.
   The rendezvous strips the prefix and rewrites `Host`/`Origin`, so the
   machine sees the plain paths.

A client MUST check that an answer it parses as JSON was served as
`application/json`. An offline machine, a sign-in page or a disabled
feature come back as HTML, sometimes with status 200, and parsing them
blind produces confusing failures.

## 3. Tabs

### 3.1 Create

```
POST <base>api/tabs
{"name": "<shown in the browser UI>"}                         interactive shell
{"name": "...", "command": "<one command string>"}            command session
→ 200 {"id": "<tab>", "layout": {"type":"pane","session_id":"<sid>"}, ...}
```

Without `command` the session runs the machine's configured login shell
interactively, exactly like a tab opened from the browser.

With `command` the session runs `<shell> -lc <command>` in a PTY and the
session **ends when the command does**. A command session is never
revived: attaching to it after it ended yields its last output and its
exit code (§5), not a second run. The string is handed to the shell
unmodified; the client is responsible for quoting. It may be large
(tens of kilobytes are fine); the limit is the machine's argument size.

A command session's PTY has output post-processing turned off (termios
`OPOST`, hence `ONLCR`) before the command starts, so the stream face
(§4.4) carries the command's exact bytes: `\n` stays `\n` and binary
output is byte-clean. The picture face (§4.3) is rendered as a terminal
with `ONLCR` would render it, so the browser view does not stair-step.
Interactive sessions keep the PTY's default line discipline.

A server that predates `command` ignores the field and creates an
interactive shell. The response echoes `"command"` when it was
honoured; a client that asked for one and does not get it back MUST
delete the tab and report that the machine's termulaa needs updating,
rather than wait on a shell that will never exit.

### 3.2 Own

```
GET <base>ws/tab/<tab>[?resume=<token>]      WebSocket upgrade
```

The first message is one of:

```json
{"type":"connected","layout":{...},"sessions":["<sid>",...],"token":"<owner token>"}
{"type":"conflict","message":"Tab is active in another window"}
```

On `conflict` another client owns the tab. The client MAY answer
`{"type":"takeover"}` and then receives `connected`; the previous owner
is disconnected. (`{"type":"new"}` replaces the tab's sessions with
fresh shells; a terminal client has no use for it.) Anything else closes
the socket.

`resume=<token>` presents the owner token from a previous connection.
If that connection is still registered on the server, the new one
silently replaces it and keeps the same token; otherwise the normal flow
above runs and a new token is issued. A client MUST take the token from
`connected` every time rather than assume it kept the old one.

The client MUST keep reading the tab socket for as long as it holds the
attachment. The server pings it every 30 s and drops it after 90 s
without a pong (WebSocket libraries answer pings automatically while a
read is in progress), and it carries layout events the client may
ignore. When the tab socket closes, the session attach below is no
longer authorised: the client treats it as the end of the attachment.

### 3.3 Delete

```
DELETE <base>api/tabs/<tab>      → 200; 404 if already gone
```

Deletes the tab and kills its sessions (SIGTERM, then SIGKILL after 2 s,
then the PTY is closed). A client that finished with a tab SHOULD delete
it so the machine is not left with dead tabs; a client that merely lost
its connection MUST NOT, since the session is still running and is what
it will resume.

## 4. Session attach

```
GET <base>ws/session/<sid>?tab_id=<tab>&token=<owner token>[&from=<offset>]
```

`403` when the token does not currently own the tab or the session is
not in it; `404` when the session is unknown.

### 4.1 Sizing

The client MUST send, immediately after the upgrade, a resize:

```json
{"type":"resize","cols":<n>,"rows":<n>}
```

The server waits for it before replaying anything, up to 2 seconds, so
that the picture is rendered for the client's size; a client that never
sends one is attached after that grace period at the PTY's current
size, needlessly slow. Resizes MAY be sent at any later time.

### 4.2 Frames

Both directions use the two WebSocket frame types with fixed meanings:

| Direction | Frame | Meaning |
|-----------|-------|---------|
| client → server | **binary** | keystrokes, written to the PTY verbatim |
| client → server | text, JSON | control: `resize` (above), `ping` (answered with `{"type":"pong"}`) |
| server → client | **binary** | PTY output bytes, verbatim |
| server → client | text, JSON | control, below |

A client MUST send input as binary. A text frame that parses as a known
control type is consumed as control and never reaches the PTY, so text
input is not merely unidiomatic, it is lossy. Server control frames:

```json
{"type":"scrollback","snapshot":true,"data":"<base64 ANSI stream>"}   picture attach, once
{"type":"replay","offset":<n>}                                        stream attach, once
{"type":"exit","code":<n>}                                             the process ended
{"type":"pong"}
```

Unknown control types MUST be ignored. Binary frames MAY arrive in any
chunking; message boundaries carry no meaning.

### 4.3 Picture attach (no `from`)

After the resize the client receives one `scrollback` frame whose data,
written into a **freshly reset** terminal of the stated size,
reproduces the session's scrollback, screen, cursor and modes. The
client MUST reset its terminal before writing it (the web UI calls
`term.reset()`; a real terminal needs `ESC c`, and `ESC [3J` as well if
it is to show the session's scrollback rather than its own). Binary
frames follow and continue exactly where the snapshot ends: the snapshot
and the join to the live stream happen under one lock on the server.

This is the attach the web UI uses and what an interactive client
wants: a reconnect is a repaint, not a replay of history.

### 4.4 Stream attach (`from=<offset>`)

Every session counts the PTY output bytes it has produced since it
started; the **offset** of a byte is its index in that stream. The
server keeps the most recent bytes in a ring (the machine's scrollback
limit, 1 MB by default).

`from=<n>` asks for the raw stream from offset n. The client receives
first

```json
{"type":"replay","offset":<m>}
```

where `m` is the offset the bytes that follow start at: `m == n` when
the ring still holds byte n, `m > n` when the oldest `m - n` bytes were
already overwritten (the client lost them and SHOULD say so), and `m`
never exceeds the stream length. Then come binary frames with bytes
`[m, …)`: the replayed part and the live stream, indistinguishable, with
no gap or repeat at the seam. A client resumes by counting the bytes it
has received and presenting that count as `from` on its next attach;
`from=0` on a first attach yields everything from the start of the
session.

Offsets are stable across the machine's own restarts: the ring and its
end offset are saved together with the session's state.

### 4.5 Exit

`{"type":"exit","code":<n>}` follows the last output when the process
ends. `code` is the exit status, or 128 plus the signal number when the
process was killed by a signal (130 for SIGINT, 137 for SIGKILL, 143 for
SIGTERM). The server flushes queued output before it and then closes
the socket.

For an **interactive** session this is the shell exiting; attaching
again revives the session with a fresh shell in the same cwd, and the
picture carries the old output above the new prompt. For a **command**
session it is the result; attaching again (either face) replays the
output or picture and sends `exit` again with the same code, then
closes. The command is never run twice.

## 5. The command runner, normatively

A client that runs one command and wants its exit code:

1. `POST api/tabs` with `command`; remember `id` and `session_id`.
2. Own the tab (§3.2), attach the session with `from=0` (§4.4), send
   the resize.
3. Write binary frames to the output as they come, counting bytes.
   Deliver an interrupt as the byte `0x03` in a binary frame: the PTY's
   line discipline turns it into SIGINT for the foreground process
   group, so the command ends the way it would under Ctrl-C and reports
   130.
4. On `exit`, remember the code, `DELETE api/tabs/<id>`, done.
5. On any socket error before `exit`: the command is still running (or
   finished unobserved). Reconnect with backoff: sign in again if
   through a rendezvous, re-own the tab with `resume=<token>`, attach
   with `from=<bytes received so far>`, continue at step 3. The exit
   frame is delivered even if the command ended while disconnected.
6. To abandon a run, `DELETE api/tabs/<id>`: this kills the command.

What a command runner must document to its users, because the PTY
decides it and not the client: stdout and stderr arrive merged in the
order they were written, otherwise byte for byte as the command wrote
them; a command that reads stdin blocks, since nothing is typed. Output
that scrolled out of the machine's ring while the client was
disconnected is lost and reported through the replay offset.

## 6. The interactive client, normatively

1. `POST api/tabs` without `command`.
2. Own the tab, attach the session without `from`, send the local
   terminal's size.
3. On `scrollback`: reset the local terminal, write the data. On binary:
   write it. Forward every keystroke as binary; forward every local
   resize as `resize`.
4. On `exit`: the shell ended; `DELETE api/tabs/<id>` and report the
   code.
5. On a socket error: the shell is still running. Reconnect as in §5.5
   but attach without `from`; the snapshot repaints the screen. Input
   typed while disconnected cannot be delivered and SHOULD be dropped,
   not queued.

## 7. What the server guarantees, and what it does not

- A session's PTY keeps running with no client attached. Nothing is
  killed by a disconnect.
- Output is never dropped for a slow client; a client more than 8 MiB
  behind is disconnected instead and must reattach (§4.4 makes that
  lossless when the ring still holds the bytes).
- A single WebSocket message from a client is limited to 1 MiB.
- If the termulaa server process itself stops (not an in-place update,
  which hands the PTYs over), its sessions are killed with SIGTERM and
  their exit (143) and final output are saved; a client that reattaches
  afterwards gets them. Output produced in the last moments before a
  crash that did not save state is lost.
- The server makes no attempt to keep a command's stdout and stderr
  apart or to deliver its stdin.

## 8. Versioning

This is version 1, the protocol the web UI in `src/cmd/termulaa/ui`
speaks plus the `command` field on tab creation and the `from`
parameter on session attach, both of which a server predating them
ignores (the tab becomes an interactive shell; the attach replays the
picture). A client detects an old server by the missing `command` echo
in the tab response (§3.1) and by the absence of a `replay` frame after
asking for `from`. Incompatible changes to framing or authorisation
MUST use new paths.
