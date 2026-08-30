module github.com/sudiptadeb/termulaa/src

go 1.24.0

require (
	github.com/creack/pty v1.1.24
	github.com/gorilla/websocket v1.5.3
	// smux multiplexes every -rc viewer connection over a few long-lived
	// outbound tunnels; hand-rolling stream framing would be riskier code.
	github.com/xtaci/smux v2.0.1+incompatible
)

require github.com/pkg/errors v0.9.1 // indirect
