#!/bin/bash
# termulaa installer — downloads the latest release binary for the host's
# OS and architecture and installs it to ~/.local/bin/termulaa.
#
#   curl -fsSL https://raw.githubusercontent.com/sudiptadeb/termulaa/main/install.sh | bash
#
# With service setup (systemd user units on Linux, LaunchAgents on macOS):
#
#   curl -fsSL https://raw.githubusercontent.com/sudiptadeb/termulaa/main/install.sh | bash -s -- --service
#
# Environment overrides:
#   INSTALL_DIR   target directory (default: ~/.local/bin)
#   VERSION       release tag to install (default: latest)

set -euo pipefail

REPO="sudiptadeb/termulaa"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${VERSION:-latest}"
SERVICE=0

usage() {
    cat <<'EOF'
termulaa installer

Usage:
  install.sh [options]
  curl -fsSL https://raw.githubusercontent.com/sudiptadeb/termulaa/main/install.sh | bash -s -- [options]

Options:
  --service     After installing the binary, install and enable it as a
                per-user service: systemd user units on Linux (no sudo),
                launchd LaunchAgents on macOS. The terminal server starts
                immediately; the remote-access tunnel agent service is
                only started once you have paired (run `termulaa -rc`
                once interactively — the installer prints the exact steps).
  --no-service  Install the binary only (default).
  -h, --help    Show this help.

Environment:
  INSTALL_DIR   Target directory (default: ~/.local/bin)
  VERSION       Release tag to install (default: latest)

Re-running is safe: the binary and any installed service files are
replaced in place.
EOF
}

while [ $# -gt 0 ]; do
    case "$1" in
        --service)    SERVICE=1 ;;
        --no-service) SERVICE=0 ;;
        -h|--help)    usage; exit 0 ;;
        *) echo "Unknown option: $1 (try --help)" >&2; exit 1 ;;
    esac
    shift
done

# ── Detect platform ──────────────────────────────────────────────────────────
os_raw=$(uname -s)
case "$os_raw" in
    Linux)  os="linux"  ;;
    Darwin) os="darwin" ;;
    *) echo "Unsupported OS: $os_raw"; exit 1 ;;
esac

arch_raw=$(uname -m)
case "$arch_raw" in
    x86_64|amd64)    arch="amd64" ;;
    arm64|aarch64)   arch="arm64" ;;
    *) echo "Unsupported arch: $arch_raw"; exit 1 ;;
esac

# ── Resolve release ──────────────────────────────────────────────────────────
api="https://api.github.com/repos/$REPO/releases"
if [ "$VERSION" = "latest" ]; then
    api="$api/latest"
else
    api="$api/tags/$VERSION"
fi

echo "→ Resolving release ($VERSION)..."
release_json=$(curl -fsSL "$api")

# Pull the matching asset URL. Release assets look like
# termulaa-<os>-<arch>-v<version>.
pattern="termulaa-${os}-${arch}-v"
url=$(printf '%s' "$release_json" \
    | grep -o "\"browser_download_url\": *\"[^\"]*\"" \
    | grep "$pattern" \
    | head -1 \
    | sed -E 's/.*"(https:[^"]+)".*/\1/')

if [ -z "$url" ]; then
    echo "No matching binary found for $os/$arch in $VERSION" >&2
    exit 1
fi

# ── Install ─────────────────────────────────────────────────────────────────
mkdir -p "$INSTALL_DIR"
dest="$INSTALL_DIR/termulaa"

echo "→ Downloading $url"
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
curl -fsSL "$url" -o "$tmp"
chmod +x "$tmp"
mv "$tmp" "$dest"
trap - EXIT

echo "✓ Installed: $dest"

# ── Path hint ───────────────────────────────────────────────────────────────
case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *)
        echo
        echo "⚠️  $INSTALL_DIR is not on your PATH."
        echo "   Add to your shell profile:"
        echo "     export PATH=\"\$HOME/.local/bin:\$PATH\""
        ;;
esac

# ── macOS quarantine hint ───────────────────────────────────────────────────
if [ "$os" = "darwin" ]; then
    echo
    echo "   macOS: if Gatekeeper refuses to run the binary, clear the quarantine:"
    echo "     xattr -d com.apple.quarantine $dest"
fi

# ── Service setup (--service) ───────────────────────────────────────────────
# The unit/plist bodies below mirror resources/service/ in the repo, with
# the @TERMULAA_BIN@ / @HOME@ placeholders filled in — this script must
# stay self-contained because it is fetched standalone via curl.

rc_paired() {
    grep -q '"token"[[:space:]]*:[[:space:]]*"[^"]' "$HOME/.termulaa/rc.json" 2>/dev/null
}

print_pair_steps_linux() {
    echo
    echo "   Remote access: the tunnel agent needs a token before it can run"
    echo "   unattended. Pair once interactively, then start its service:"
    echo "     $dest -rc          # prompts for a token, saves it"
    echo "     systemctl --user enable --now termulaa-rc"
}

print_pair_steps_darwin() {
    echo
    echo "   Remote access: the tunnel agent needs a token before it can run"
    echo "   unattended. Pair once interactively, then load its agent:"
    echo "     $dest -rc          # prompts for a token, saves it"
    echo "     launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.termulaa.rc.plist"
}

install_service_linux() {
    if ! command -v systemctl >/dev/null 2>&1; then
        echo "✗ --service needs systemd, but systemctl was not found." >&2
        echo "  The binary is installed; start it manually with: termulaa" >&2
        exit 1
    fi
    if ! systemctl --user show-environment >/dev/null 2>&1; then
        echo "✗ systemctl --user cannot reach a user session manager." >&2
        echo "  This usually means no systemd user session (e.g. a container," >&2
        echo "  or a login without DBUS_SESSION_BUS_ADDRESS/XDG_RUNTIME_DIR)." >&2
        echo "  The binary is installed; start it manually with: termulaa" >&2
        exit 1
    fi

    unit_dir="$HOME/.config/systemd/user"
    mkdir -p "$unit_dir"

    cat > "$unit_dir/termulaa.service" <<EOF
[Unit]
Description=termulaa terminal server (loopback-only)
Documentation=https://github.com/$REPO

[Service]
ExecStart=$dest
Restart=on-failure
RestartSec=2

[Install]
WantedBy=default.target
EOF

    cat > "$unit_dir/termulaa-rc.service" <<EOF
# Pair before enabling: run \`termulaa -rc\` once in a terminal so the
# token is saved to ~/.termulaa/rc.json. Without it the agent cannot
# prompt (no tty here) and exits; the start limit stops the loop.
[Unit]
Description=termulaa reverse tunnel agent
Documentation=https://github.com/$REPO/blob/main/docs/rc-protocol.md
After=termulaa.service
Wants=termulaa.service
StartLimitIntervalSec=120
StartLimitBurst=5

[Service]
ExecStart=$dest -rc
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
EOF

    systemctl --user daemon-reload
    systemctl --user enable --now termulaa.service
    echo "✓ termulaa.service enabled and started (systemd user unit)"

    if rc_paired; then
        systemctl --user enable --now termulaa-rc.service
        echo "✓ termulaa-rc.service enabled and started (token found in ~/.termulaa/rc.json)"
    else
        echo "✓ termulaa-rc.service installed (not enabled — no token yet)"
        print_pair_steps_linux
    fi

    user_name=$(id -un)
    if loginctl enable-linger "$user_name" >/dev/null 2>&1; then
        echo "✓ Lingering enabled: services survive logout and start at boot"
    else
        echo
        echo "⚠️  Could not enable lingering. Without it, user services stop at"
        echo "   logout and don't start at boot. Enable it with:"
        echo "     loginctl enable-linger $user_name"
    fi
}

install_service_darwin() {
    if ! command -v launchctl >/dev/null 2>&1; then
        echo "✗ --service needs launchd, but launchctl was not found." >&2
        echo "  The binary is installed; start it manually with: termulaa" >&2
        exit 1
    fi

    agents_dir="$HOME/Library/LaunchAgents"
    mkdir -p "$agents_dir" "$HOME/.termulaa/logs"
    uid=$(id -u)

    cat > "$agents_dir/com.termulaa.server.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.termulaa.server</string>
	<key>ProgramArguments</key>
	<array>
		<string>$dest</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>$HOME/.termulaa/logs/server.log</string>
	<key>StandardErrorPath</key>
	<string>$HOME/.termulaa/logs/server.log</string>
</dict>
</plist>
EOF

    cat > "$agents_dir/com.termulaa.rc.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!-- Pair before loading: run \`termulaa -rc\` once in a terminal so the
     token is saved to ~/.termulaa/rc.json. Without it the agent cannot
     prompt (no tty here) and exits; launchd retries every
     ThrottleInterval seconds, logging to rc.log, until you pair. -->
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.termulaa.rc</string>
	<key>ProgramArguments</key>
	<array>
		<string>$dest</string>
		<string>-rc</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>30</integer>
	<key>StandardOutPath</key>
	<string>$HOME/.termulaa/logs/rc.log</string>
	<key>StandardErrorPath</key>
	<string>$HOME/.termulaa/logs/rc.log</string>
</dict>
</plist>
EOF

    # bootout first so re-runs pick up the rewritten plist
    launchctl bootout "gui/$uid/com.termulaa.server" 2>/dev/null || true
    launchctl bootstrap "gui/$uid" "$agents_dir/com.termulaa.server.plist"
    echo "✓ com.termulaa.server loaded (LaunchAgent, logs in ~/.termulaa/logs/)"

    if rc_paired; then
        launchctl bootout "gui/$uid/com.termulaa.rc" 2>/dev/null || true
        launchctl bootstrap "gui/$uid" "$agents_dir/com.termulaa.rc.plist"
        echo "✓ com.termulaa.rc loaded (token found in ~/.termulaa/rc.json)"
    else
        launchctl bootout "gui/$uid/com.termulaa.rc" 2>/dev/null || true
        echo "✓ com.termulaa.rc.plist installed (not loaded — no token yet)"
        print_pair_steps_darwin
    fi
}

if [ "$SERVICE" = 1 ]; then
    echo
    case "$os" in
        linux)  install_service_linux ;;
        darwin) install_service_darwin ;;
    esac
    echo
    echo "Open http://127.0.0.1:17380/"
    exit 0
fi

echo
echo "Run it:"
echo "  termulaa"
echo "Then open http://127.0.0.1:17380/"
