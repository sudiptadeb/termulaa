# Security

## Threat model

termulaa is a **single-user, loopback-only** developer tool. It is not
designed, tested, or intended for multi-user or remote use.

The HTTP server binds to `127.0.0.1` only. It has:

- **No per-request authentication.**
- **Wildcard CORS** (`Access-Control-Allow-Origin: *`).
- **No WebSocket origin check** (the upgrader's `CheckOrigin` returns
  `true`).

This means: **any web page loaded in any browser on the same machine can
call the API and attach to a live PTY**. This is accepted, known risk for a
single-user dev tool running on a personal machine.

## Do not expose termulaa on a non-loopback interface

Changing the listener bind from `127.0.0.1` to `0.0.0.0` (or any LAN/VPN
address) without also adding the controls below is a security incident. In
`src/cmd/terminal-agent/main.go` the bind is pinned at `127.0.0.1:<port>`
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

## Reporting a vulnerability

This is a personal project and has no paid security team. Best-effort
response only.

- **Non-sensitive issues** — open a GitHub issue.
- **Sensitive issues** (e.g. a working exploit) — use GitHub's
  [private vulnerability reporting](https://github.com/sudiptadeb/termulaa/security/advisories/new),
  or email <mr.sudiptadeb@gmail.com>.
