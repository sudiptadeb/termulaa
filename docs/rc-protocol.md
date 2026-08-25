# termulaa remote-access protocol (rc), version 1

This document is the normative specification of the protocol spoken by
`termulaa -rc`. It is vendor-neutral: a third party can implement a
compatible rendezvous server from this document alone. The reference
implementation lives in [memd](https://memd.debkosh.com), but nothing
here depends on it.

The key words MUST, MUST NOT, SHOULD, and MAY are to be interpreted as
described in RFC 2119.

## 1. Mental model

termulaa is a loopback-only terminal server on `127.0.0.1:<port>`. It is
never modified to be remote-aware and never listens on a non-loopback
interface.

Remote access is provided by a separate process, the **agent**
(`termulaa -rc`), and a publicly reachable **server** (the rendezvous).
The agent dials *out* to the server and holds a small pool of long-lived
WebSocket connections — **tunnels**. Each tunnel carries an smux
session. Every browser connection that the server wants to forward
becomes one **stream** multiplexed inside a tunnel. The agent accepts
the stream, dials the local termulaa over TCP, and splices bytes.

```
browser ──HTTPS/WSS──> server (rendezvous)
                          │  OpenStream() on a pooled smux session
                          │  (the session rides INSIDE a WebSocket the agent dialed)
                          ▼
                   termulaa -rc agent ──TCP──> 127.0.0.1:<port> (unmodified termulaa)
```

The tunnel is *reverse* because the TCP/TLS connection direction is
always agent → server. No inbound port is ever opened on the user's
machine; the loopback-only property of termulaa is preserved exactly.

The agent is a dumb byte pump. It MUST NOT parse HTTP inside streams and
holds no knowledge of terminals.

Terminology, used consistently below: a **tunnel** is one live smux
session over one WebSocket. A browser connection is a **stream**
multiplexed inside a tunnel. A stream is not a tunnel.

## 2. Transport: the tunnel endpoint

The server MUST accept WebSocket upgrades at:

```
GET <server>/rc/tunnel
```

over `ws://` (development) or `wss://` (production). The agent derives
the URL from its configured server base URL: `http(s)` maps to
`ws(s)`, and any base path is preserved (`https://example.com/x` →
`wss://example.com/x/rc/tunnel`).

The agent authenticates with:

```
Authorization: Bearer <token>
```

The server MUST validate the token before completing the upgrade and
MUST respond `401` or `403` on failure. The agent treats `401`/`403` as
terminal (it stops and tells the user to re-pair); any other failure is
retried with backoff.

Query parameters sent by the agent:

| Param | Meaning |
|-------|---------|
| `agent` | human-readable label for display (default: the machine's hostname) |
| `session` | 0-based index of this tunnel within the agent's pool |
| `port` | the local TCP port the agent splices streams to — the server MUST use it for header rewriting (section 6) |
| `instance` | the agent's per-process instance id (section 8.1). Generated ONCE per process from a cryptographically random source and sent identically on every tunnel of the pool. It MUST NOT be derived from the token, hostname, or anything else stable across restarts — a restarted process must present a brand-new id. 1–64 characters from `[A-Za-z0-9._-]`; the reference agent sends 32 lowercase hex characters (128 random bits). |
| `takeover` | `1` to displace a live incumbent instance holding the same token (section 8.2). Omitted otherwise; the agent sends it only on explicit operator request (`-rc-takeover`). |

## 3. Framing: WebSocket as a byte pipe

Raw smux bytes ride in WebSocket **binary** messages. Both sides adapt
the WebSocket to an ordered byte pipe (a `net.Conn` in Go) with these
semantics:

- **Read**: if a buffered remainder from a previous message exists,
  drain it first; otherwise read the next message and buffer what does
  not fit. One message MAY be consumed across many reads. Message
  boundaries carry no meaning.
- **Write**: exactly one binary message per write call, serialized by a
  mutex (WebSocket libraries typically permit only one concurrent
  writer).
- **No WebSocket read deadline and no ping/pong liveness scheme.**
  smux's own keepalive (section 4) is the liveness detector. Write
  deadlines MAY map to the WebSocket write deadline.

## 4. Multiplexing: smux roles and configuration

Both sides run [smux](https://github.com/xtaci/smux) v1.5.x over the
byte pipe, with fixed roles that MUST NOT be swapped:

| Side | smux role | Operation |
|------|-----------|-----------|
| agent | `smux.Server` | `AcceptStream()` |
| server | `smux.Client` | `OpenStream()` |

Rationale: the server initiates work (a browser arrived); the agent
serves it.

The smux configuration MUST be identical on both sides. A mismatch does
not fail loudly — it causes silent stalls. The exact configuration:

```go
func muxConfig() *smux.Config {
	c := smux.DefaultConfig()
	c.Version = 2
	c.KeepAliveInterval = 10 * time.Second
	c.KeepAliveTimeout = 30 * time.Second
	c.MaxFrameSize = 32 * 1024
	c.MaxReceiveBuffer = 4 * 1024 * 1024
	c.MaxStreamBuffer = 512 * 1024
	return c
}
```

A non-Go implementation MUST speak smux protocol version 2 with
equivalent settings.

## 5. Stream semantics

One stream = one browser connection (an HTTP request or an upgraded
WebSocket). For every accepted stream the agent:

1. dials `127.0.0.1:<port>` over TCP,
2. copies bytes in both directions,
3. closes both ends when either side closes.

The agent MUST NOT inspect or transform the bytes. The server SHOULD
spread streams across the pool (e.g. least-loaded live tunnel) so
head-of-line blocking stays bounded, and SHOULD skip and reap dead
tunnels.

## 6. REQUIRED server behavior: Host and Origin rewriting

**This is the single most important requirement for an implementer.**

termulaa defends against DNS rebinding with a Host-header allowlist
(`127.0.0.1`, `localhost`, `::1` on its port) and an Origin allowlist.
A request carrying the public hostname is rejected with `421`/`403`.
Because the agent forwards bytes verbatim, the server MUST rewrite each
forwarded request so an *unmodified* termulaa accepts it, using the
`port` value the agent reported at tunnel registration:

```go
target := "localhost:" + agentInfo.Port
req.URL.Scheme = "http"
req.URL.Host   = target
req.Host       = target                             // Host-header allowlist
if req.Header.Get("Origin") != "" {                 // Origin allowlist
    req.Header.Set("Origin", "http://"+target)
}
```

The server SHOULD also strip `X-Forwarded-Host` and `X-Forwarded-Proto`
so the public host does not leak into the local process, and SHOULD
flush proxied responses immediately (unbuffered) so terminal output is
not delayed.

### 6.1 Where to expose the proxied terminal

A server MAY expose the proxied terminal either on a **dedicated
hostname** (every path on that host is proxied verbatim) or **under a
path prefix** on an existing host (e.g. `/rc/t/<agent>/...`).

A dedicated hostname gives the terminal its own browser origin,
isolating it from everything else the server hosts — the more secure
choice when the DNS and TLS work is acceptable.

To serve under a path prefix, the server MUST strip the prefix from the
forwarded request path (termulaa's routes are unchanged: it still sees
`/api/tabs`, `/ws/session/<id>`, …) and MUST send the stripped prefix in
the de-facto-standard `X-Forwarded-Prefix` request header, without a
trailing slash (e.g. `X-Forwarded-Prefix: /rc/t/abc123`). The server
MUST NOT pass through a client-supplied `X-Forwarded-Prefix`; it either
sets its own value or removes the header.

termulaa's side of the contract: its UI is base-path aware. Every HTML
page carries `<base href="/">`, all UI URLs are base-relative, and when
a request for a page carries a **valid** `X-Forwarded-Prefix` the page
is rendered with `<base href="<prefix>/">` instead, so the entire UI —
fetches and WebSockets included — resolves under the prefix.

Because the header is attacker-influenceable input that is rendered into
HTML, termulaa sanitizes it strictly and silently falls back to `/` on
any violation. A prefix is accepted only if it:

- starts with `/` and does not end with `/`,
- is at most 256 bytes,
- consists of `/`-separated segments matching `[A-Za-z0-9._~-]+`
  (so no scheme, host, query, quotes, angle brackets, spaces, or
  percent-escapes),
- contains no empty and no dots-only (`.`, `..`, `...`) segments.

The accepted value is additionally HTML-escaped when emitted. A server
choosing a prefix outside this shape gets root-relative pages, not an
error.

## 7. Tokens

**The token is opaque to termulaa.** The agent stores the string it was
given (in `~/.termulaa/rc.json`) and presents it verbatim in the
`Authorization` header. It MUST NOT parse it, validate it, or assume any
structure or encoding. Token format, signing, storage, revocation, and
expiry are entirely the server's business — one server may issue
HMAC-signed stateless tokens, another random database-backed ones.

Expiry is discovered by being rejected at connect time (`401`/`403`),
never by inspecting the token.

The server SHOULD serve a pairing page at `<server>/rc` where an
authenticated user mints a token; the agent simply prompts the user to
paste the string. Tokens SHOULD expire so that lost or leaked tokens
retire themselves; the agent's re-pair message points users at
`<server>/rc`.

Servers SHOULD never log full tokens (log a hash prefix instead), and
the agent does the same.

## 8. Reconnection, instance identity, and conflicts

The agent maintains its pool of tunnels independently: each tunnel
reconnects on failure with exponential backoff (1s doubling to a 30s
cap, reset after a connection that stayed up), randomized with jitter so
pool members do not reconnect in lockstep after a server restart. The
server MUST tolerate tunnels coming and going.

Replacement of stale tunnels is scoped by **agent instance**, not by
token alone. A bare replace-by-`(token, session)` rule cannot tell an
agent reconnecting from a *different process* using the same token; two
such processes would evict each other's slots forever, each reconnect
displacing the other — a mutual-eviction livelock in which neither pool
ever stabilizes. The rules below exist to make that impossible.

### 8.1 Agent instance identity

Every agent process generates one random, unguessable **instance id**
(see the `instance` parameter, section 2) and presents it on every
tunnel handshake. All tunnels of one process share the id; a restarted
process has a new one. The server keys each token's pool by
`(instance, session)` and evaluates every registration as follows,
**in order**:

1. **Reap first.** The server MUST drop dead tunnels from the pool
   before deciding anything. A registration MUST NOT be refused because
   of tunnels whose connections are already dead — otherwise killing and
   restarting an agent would lock the user out of their own token. A
   server SHOULD detect carrier death promptly (a closed TCP connection
   is immediate; only a true network partition may take until the smux
   keepalive timeout, at most 30s).
2. **Same instance.** The registration is the reconnect path: it MUST
   replace any stale predecessor in the same `session` slot (closing it
   immediately) and joins the pool alongside the instance's other slots.
3. **Different instance, live incumbent, no `takeover`.** The server
   MUST refuse the handshake with HTTP `409` and MUST NOT disturb the
   incumbent in any way. The `409` body is a JSON object describing the
   incumbent so the user can identify their own stray process:

   ```json
   {
     "error": "conflict",
     "label": "handyman",
     "tunnels": 4,
     "connected_at": "2026-08-25T11:02:41Z",
     "connected_secs": 720
   }
   ```

   `label` is the incumbent's `agent` label, `tunnels` its live tunnel
   count, `connected_at`/`connected_secs` when its earliest live tunnel
   registered (the seconds form spares the agent clock-skew math). The
   body SHOULD stay under 1 KB (clients may only surface a handshake
   body prefix). The agent MUST treat `409` as **terminal**: stop every
   tunnel in the pool, do not retry, and tell the user who holds the
   token and what to do (stop that agent, re-run with the takeover
   flag, or mint a separate token at `<server>/rc`).
4. **Different instance, live incumbent, `takeover=1`.** The newcomer
   wins. The server MUST displace the incumbent instance's **entire**
   pool — every tunnel, not just the colliding slot, or the loser keeps
   a partial pool thrashing — and MUST send each displaced tunnel the
   SUPERSEDED close signal (section 8.2) before tearing its carrier
   down.

If a registration lands against a live different-instance pool without
having been refused (two processes racing through the conflict check
simultaneously), the server resolves it as rule 4: the latest
registration takes the pool and the displaced instance is superseded.
Combined with the terminal handling below, any collision converges to
exactly one live instance instead of oscillating.

### 8.2 The SUPERSEDED close signal

Merely closing a displaced tunnel's carrier is indistinguishable from a
network drop (WebSocket close code 1006), which the loser would retry —
recreating the livelock. A displacing server therefore MUST first send
a WebSocket close frame with:

- close code **1008** (policy violation),
- reason exactly the string **`superseded`** (machine-readable; MUST
  NOT change).

An agent receiving this close on any tunnel MUST treat it as terminal
for the **whole process**: stop all tunnels, do not reconnect, exit
non-zero, and tell the user that another agent took over this token and
that a separate token per agent can be minted at `<server>/rc`.

### 8.3 Legacy agents (no `instance`)

An agent predating the `instance` parameter sends none; the server
treats the missing value as the shared **legacy identity** (the empty
string) rather than rejecting it. Two legacy agents therefore share one
identity and keep the historical replace-by-`session`-slot behavior,
including its livelock flaw — indistinguishable by design, since they
offer nothing to distinguish. Between the legacy identity and any real
instance the rules of 8.1 apply unchanged: a legacy newcomer against a
live instance-bearing incumbent is refused with `409` (the old agent
retries with backoff — noisy, but it can never evict the incumbent),
and an instance-bearing agent may take over a live legacy pool with
`takeover=1` (the displaced legacy agent does not understand SUPERSEDED
and will retry into `409` backoff until upgraded or stopped).

**Revocation of a token that is already connected.** Only a handshake
`401`/`403` stops the agent. A server that revokes a live token closes
the tunnels; the agent cannot distinguish that from a network drop, so
it retries — and is then refused at the next handshake, which stops it.
Revocation therefore converges within one backoff interval (at most 30s)
rather than instantly. Servers MUST close revoked tunnels rather than
leaving them open, and MUST re-check the token on every reconnect
instead of trusting a previously accepted one.

## 9. Versioning

This is version 1 of the protocol, identified by the `/rc/tunnel` path.
Any incompatible change — framing, smux roles or configuration, auth
scheme — MUST use a new endpoint path (e.g. `/rc/v2/tunnel`) so old
agents fail cleanly at connect time rather than stalling. Additive,
backward-compatible changes (new optional query parameters) MAY be made
within version 1; servers MUST ignore query parameters they do not
understand.
