# terminal-agent

Local terminal agent — HTTP + WebSocket server that spawns PTYs and serves a browser terminal UI on `127.0.0.1:17380`.

## Run

```
./terminal-agent-<arch>-v<version>
```

Then open `http://127.0.0.1:17380/` in a browser.

Optional flag:

```
./terminal-agent-<arch>-v<version> -port 17380
```

Config persists to `~/.terminal-agent/config.json`.

## Security

Loopback-only (`127.0.0.1`). Never expose on a non-loopback interface without first adding per-request auth, strict CORS, and a WebSocket origin check.
