# Security

## Threat model

termulaa is a **single-user, loopback-only** developer tool. It is not
designed, tested, or intended for multi-user or remote use.

The HTTP server binds to `127.0.0.1` only. It has:

- **No per-request authentication.**
- **Host-header allowlisting** for `127.0.0.1`, `localhost`, and `::1`
  on the configured port.
- **Origin allowlisting** for the same loopback origins on both HTTP and
  WebSocket requests.
- **No wildcard CORS** — the server echoes only allowed loopback origins.

This means: **any web page loaded in any browser on the same machine can
call the API and attach to a live PTY if it uses an allowed Host/Origin**.
This is accepted, known risk for a single-user dev tool running on a
personal machine.

## Do not expose termulaa on a non-loopback interface

Changing the listener bind from `127.0.0.1` to `0.0.0.0` (or any LAN/VPN
address) without also adding the controls below is a security incident. In
`src/cmd/termulaa/main.go` the bind is pinned at `127.0.0.1:<port>`
and commented accordingly.

## What would need to change before exposing on a non-loopback interface

1. **Per-request authentication** — a bearer secret, or an OS-local
   credential (keychain, UDS). Every HTTP and WebSocket request must be
   authenticated.
2. **Strict CORS policy** — explicit allowlist, not `*`.
3. **WebSocket origin check** — `CheckOrigin` must validate the `Origin`
   header against an allowlist.
4. **TLS** — required on any non-loopback interface.
5. **Rate limiting / session quotas** — to contain abuse or bugs.

Until all five are in place, keep the loopback bind.

## Remote access (`-rc`) and the loopback rule

`termulaa -rc` runs a **reverse tunnel agent** (see
[docs/rc-protocol.md](docs/rc-protocol.md)). This does **not** violate
the loopback rule:

- The terminal server's bind is untouched — still `127.0.0.1` only.
- No inbound port is opened anywhere. The agent only dials *out*
  (WebSocket over TLS) to a rendezvous server, and browser traffic
  arrives as multiplexed streams inside those outbound connections.
- The rendezvous rewrites `Host`/`Origin` to loopback values, so the
  local termulaa still enforces its DNS-rebinding guard unchanged.
- The agent is a byte pump with no HTTP parsing and no terminal
  knowledge; `termulaa -rc` never starts the terminal server.

**The token is the capability.** Anyone holding a valid tunnel token can
attach an agent, and anyone the rendezvous admits to the viewer side of
that agent gets your terminal — i.e. arbitrary command execution as your
user. Treat tokens like SSH private keys. They expire by design and can
be revoked at the rendezvous pairing page; expiry is discovered at
connect time (the token is opaque to termulaa and never parsed).

Residual risks, accepted knowingly when you opt in to `-rc`:

- **Rendezvous compromise or malice** — the rendezvous terminates TLS
  and can open streams to your terminal at will. Only tunnel to a
  rendezvous you trust as much as the machine itself (ideally your own).
- **Token leakage** — a leaked token is remote shell access until it
  expires or is revoked.
- **`-rc-insecure`** disables TLS certificate verification on the
  agent's dials. It exists for development against self-signed
  rendezvous certs only; with it set, an on-path attacker can become
  your rendezvous. Never use it in production. It persists in
  `~/.termulaa/rc.json`; remove it with `-rc-insecure=false`.

Not running `-rc` (the default) leaves the historical posture exactly as
described above.

## Reporting a vulnerability

This is a personal project and has no paid security team. Best-effort
response only.

- **Non-sensitive issues** — open a GitHub issue.
- **Sensitive issues** (e.g. a working exploit) — use GitHub's
  [private vulnerability reporting](https://github.com/sudiptadeb/termulaa/security/advisories/new),
  or email <mr.sudiptadeb@gmail.com>.
