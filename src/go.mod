module github.com/sudiptadeb/termulaa/src

go 1.25

require (
	github.com/creack/pty v1.1.24
	github.com/gorilla/websocket v1.5.3
	// smux multiplexes every -rc viewer connection over a few long-lived
	// outbound tunnels; hand-rolling stream framing would be riskier code.
	github.com/xtaci/smux v1.5.34
)

require github.com/sudiptadeb/amarit v0.5.3
