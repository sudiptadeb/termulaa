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
# Re-running upgrades in place: the binary is replaced and anything the
# service manager runs (termulaa / termulaa-rc) is restarted onto the new
# binary. Processes started by hand are detected and reported, never killed.
#
# Environment overrides:
#   INSTALL_DIR   target directory (default: ~/.local/bin)
#   VERSION       release tag to install (default: latest)

set -euo pipefail

REPO="sudiptadeb/termulaa"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${VERSION:-latest}"
SERVICE=0
RESTART=1

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
  --no-restart  Stage the binary without restarting anything. Whatever is
                running keeps executing the previous version until you
                restart it yourself. Cannot be combined with --service.
  -h, --help    Show this help.

Environment:
  INSTALL_DIR   Target directory (default: ~/.local/bin)
  VERSION       Release tag to install (default: latest)

Re-running is safe and is how you upgrade: the binary and any installed
service files are replaced in place, and services that were running are
restarted onto the new binary (server first, then the tunnel agent).
Services you disabled stay disabled; processes you started by hand are
reported but never touched.
EOF
}

while [ $# -gt 0 ]; do
    case "$1" in
        --service)    SERVICE=1 ;;
        --no-service) SERVICE=0 ;;
        --no-restart) RESTART=0 ;;
        -h|--help)    usage; exit 0 ;;
        *) echo "Unknown option: $1 (try --help)" >&2; exit 1 ;;
    esac
    shift
done

if [ "$SERVICE" = 1 ] && [ "$RESTART" = 0 ]; then
    echo "--no-restart cannot be combined with --service (service setup starts services)." >&2
    exit 1
fi

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
# Deliberately does NOT use api.github.com: unauthenticated it allows 60
# requests/hour per IP, and its 403 carries no body, so a plain `curl -fs`
# under `set -e` exits with no output at all. github.com's redirect needs no
# token, is not rate limited, and gives us the tag directly.
resolve_latest_tag() {
    curl -sSI -m 30 "https://github.com/$REPO/releases/latest" 2>/dev/null \
        | tr -d '\r' \
        | awk 'tolower($1) == "location:" { print $2 }' \
        | sed -n 's#.*/releases/tag/##p' \
        | tail -1
}

echo "→ Resolving release ($VERSION)..."
if [ "$VERSION" = "latest" ]; then
    tag=$(resolve_latest_tag) || true
    if [ -z "${tag:-}" ]; then
        echo "Could not resolve the latest release of $REPO." >&2
        echo "  Check https://github.com/$REPO/releases and retry, or pin one:" >&2
        echo "    VERSION=v0.2.0 $0" >&2
        echo "  If you are offline or behind a proxy, download the binary for your" >&2
        echo "  platform from that page and copy it to $INSTALL_DIR/termulaa." >&2
        exit 1
    fi
else
    tag="$VERSION"
fi

dest="$INSTALL_DIR/termulaa"

# ── Installed version ───────────────────────────────────────────────────────
# `-version` exists from v0.2.2 on; older binaries exit 2 with an empty
# stdout, which reads as "unknown" and forces a fresh download.
had_binary=0
old_ver=""
if [ -x "$dest" ]; then
    had_binary=1
    old_ver=$("$dest" -version 2>/dev/null || true)
fi

updated=1
if [ -n "$old_ver" ] && [ "$old_ver" = "$tag" ]; then
    updated=0
    echo "✓ Already installed: $dest ($tag)"
fi

# ── Download + install ──────────────────────────────────────────────────────
if [ "$updated" = 1 ]; then
    # Asset names are fully determined by the tag, so no API lookup is needed.
    asset="termulaa-${os}-${arch}-${tag}"
    url="https://github.com/$REPO/releases/download/$tag/$asset"

    code=$(curl -sSL -o /dev/null -w '%{http_code}' -m 60 "$url" 2>/dev/null || echo 000)
    if [ "$code" != "200" ]; then
        echo "No binary for $os/$arch in $tag (HTTP $code)." >&2
        echo "  Looked for: $asset" >&2
        echo "  Available assets: https://github.com/$REPO/releases/tag/$tag" >&2
        exit 1
    fi

    mkdir -p "$INSTALL_DIR"

    echo "→ Downloading $url"
    tmp=$(mktemp)
    trap 'rm -f "$tmp"' EXIT
    if ! curl -sSL --fail-with-body -o "$tmp" -m 300 "$url"; then
        echo "Download failed: $url" >&2
        exit 1
    fi
    chmod +x "$tmp"
    mv "$tmp" "$dest"
    trap - EXIT

    if [ "$had_binary" = 1 ] && [ -n "$old_ver" ]; then
        echo "✓ Installed: $dest ($old_ver → $tag)"
    elif [ "$had_binary" = 1 ]; then
        echo "✓ Installed: $dest (→ $tag, previous version unknown)"
    else
        echo "✓ Installed: $dest ($tag)"
    fi
fi

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

# ── Restart / detection helpers ─────────────────────────────────────────────
restart_failed=""     # space-separated names of units/agents that failed to restart
restarted_any=0
running_current=0     # something managed is already running the installed version
unmanaged_list=""     # one "PID <pid>  <args>" line per manual process
unmanaged_count=0

systemd_user_ok() {
    command -v systemctl >/dev/null 2>&1 && systemctl --user show-environment >/dev/null 2>&1
}

# stale = the process still executes a binary that has since been replaced
# (Linux: /proc/<pid>/exe points at a deleted inode). macOS has no /proc,
# so it reports unknown; unknown is treated as stale only right after a
# fresh download, when everything started earlier is old by definition.
proc_state() {
    local exe
    exe=$(readlink "/proc/$1/exe" 2>/dev/null || true)
    if [ -z "$exe" ]; then
        echo unknown
        return 0
    fi
    case "$exe" in
        *' (deleted)') echo stale ;;
        *)             echo current ;;
    esac
}

# Restart onto the new binary — terminal server first, then the tunnel
# agent (the agent splices to the server). Only units that are currently
# active are touched: a disabled or stopped unit stays that way, and the
# rc agent is never started unpaired. A failed restart is reported and the
# loop continues so a server failure never blocks handling the agent.
restart_services_linux() {
    systemd_user_ok || return 0
    local unit pid state
    for unit in termulaa.service termulaa-rc.service; do
        if ! systemctl --user is-active --quiet "$unit" 2>/dev/null; then
            continue
        fi
        if [ "$updated" = 0 ]; then
            pid=$(systemctl --user show -p MainPID --value "$unit" 2>/dev/null || true)
            state=unknown
            if [ -n "$pid" ] && [ "$pid" != "0" ]; then
                state=$(proc_state "$pid")
            fi
            if [ "$state" = "current" ]; then
                running_current=1
                continue
            fi
        fi
        if systemctl --user restart "$unit" 2>/dev/null; then
            echo "✓ Restarted $unit (systemd user unit)"
            restarted_any=1
        else
            echo "✗ Could not restart $unit — it may be down. Check:" >&2
            echo "    systemctl --user status $unit" >&2
            restart_failed="$restart_failed $unit"
        fi
    done
    return 0
}

# bootout + bootstrap, tolerating the window where launchd still holds the
# label after a bootout ("Input/output error"): retry briefly instead of
# failing on the first attempt.
relaunch_agent() {
    local dom="$1" label="$2" plist="$HOME/Library/LaunchAgents/$2.plist" i
    if [ ! -f "$plist" ]; then
        return 1
    fi
    launchctl bootout "$dom/$label" 2>/dev/null || true
    i=0
    while [ "$i" -lt 10 ]; do
        if launchctl bootstrap "$dom" "$plist" 2>/dev/null; then
            return 0
        fi
        i=$((i + 1))
        sleep 0.5
    done
    return 1
}

# Same contract as restart_services_linux, for launchd. `kickstart -k`
# restarts a loaded agent in place without ever unloading the label (so
# the bootout/bootstrap race cannot happen); the reload path is only a
# fallback for launchds without a working kickstart.
restart_services_darwin() {
    if ! command -v launchctl >/dev/null 2>&1; then
        return 0
    fi
    local dom label
    dom="gui/$(id -u)"
    for label in com.termulaa.server com.termulaa.rc; do
        if ! launchctl print "$dom/$label" >/dev/null 2>&1; then
            continue
        fi
        if [ "$updated" = 0 ]; then
            running_current=1
            continue
        fi
        if launchctl kickstart -k "$dom/$label" 2>/dev/null; then
            echo "✓ Restarted $label (launchd)"
            restarted_any=1
        elif relaunch_agent "$dom" "$label"; then
            echo "✓ Restarted $label (launchd, reloaded)"
            restarted_any=1
        else
            echo "✗ Could not restart $label — it may be down. Check:" >&2
            echo "    launchctl print $dom/$label" >&2
            restart_failed="$restart_failed $label"
        fi
    done
    return 0
}

managed_pids() {
    local unit label pid dom
    if [ "$os" = "linux" ]; then
        systemd_user_ok || return 0
        for unit in termulaa.service termulaa-rc.service; do
            pid=$(systemctl --user show -p MainPID --value "$unit" 2>/dev/null || true)
            if [ -n "$pid" ] && [ "$pid" != "0" ]; then
                printf '%s\n' "$pid"
            fi
        done
    else
        command -v launchctl >/dev/null 2>&1 || return 0
        dom="gui/$(id -u)"
        for label in com.termulaa.server com.termulaa.rc; do
            launchctl print "$dom/$label" 2>/dev/null \
                | awk '/^[[:space:]]*pid = /{print $3; exit}' || true
        done
    fi
    return 0
}

# termulaa processes running OUTSIDE a service manager cannot be restarted
# safely by a script (they are someone's foreground shell command), but
# they silently keep the old binary — so they must be reported, not killed.
find_unmanaged() {
    command -v pgrep >/dev/null 2>&1 || return 0
    local pids managed pid state args
    pids=$(pgrep -x termulaa 2>/dev/null || true)
    if [ -z "$pids" ]; then
        return 0
    fi
    managed=" $(managed_pids | tr '\n' ' ') "
    for pid in $pids; do
        case "$managed" in
            *" $pid "*) continue ;;
        esac
        state=$(proc_state "$pid")
        if [ "$updated" = 0 ] && [ "$state" != "stale" ]; then
            continue
        fi
        args=$(ps -p "$pid" -o args= 2>/dev/null | sed 's/^[[:space:]]*//' || true)
        unmanaged_list="${unmanaged_list}     PID $pid  ${args:-termulaa}"$'\n'
        unmanaged_count=$((unmanaged_count + 1))
    done
    return 0
}

warn_unmanaged() {
    find_unmanaged
    if [ "$unmanaged_count" -gt 0 ]; then
        echo
        echo "⚠️  Still running the OLD binary — started by hand, not by a service"
        echo "   manager, so the installer will not touch it:"
        printf '%s' "$unmanaged_list"
        echo "   Restart these yourself to finish the upgrade: Ctrl-C the foreground"
        echo "   process (or kill <pid>), then run it again."
    fi
}

report_failures_and_exit() {
    if [ -n "$restart_failed" ]; then
        echo
        echo "✗ Install finished, but these failed to restart:$restart_failed" >&2
        echo "  termulaa may be DOWN until you start it again by hand." >&2
        exit 1
    fi
}

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
    # enable + restart, not `enable --now`: --now is a no-op for an already
    # running unit and would leave it on the replaced binary.
    systemctl --user enable termulaa.service >/dev/null 2>&1 || true
    if systemctl --user restart termulaa.service 2>/dev/null; then
        echo "✓ termulaa.service enabled and started (systemd user unit)"
    else
        echo "✗ termulaa.service failed to start. Check:" >&2
        echo "    systemctl --user status termulaa" >&2
        restart_failed="$restart_failed termulaa.service"
    fi

    if rc_paired; then
        systemctl --user enable termulaa-rc.service >/dev/null 2>&1 || true
        if systemctl --user restart termulaa-rc.service 2>/dev/null; then
            echo "✓ termulaa-rc.service enabled and started (token found in ~/.termulaa/rc.json)"
        else
            echo "✗ termulaa-rc.service failed to start. Check:" >&2
            echo "    systemctl --user status termulaa-rc" >&2
            restart_failed="$restart_failed termulaa-rc.service"
        fi
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
     token is saved to ~/.termulaa/rc.json. Without a saved token the
     agent cannot prompt (no tty here) and exits; launchd retries every
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

    # Reload so re-runs pick up the rewritten plists. relaunch_agent retries
    # the bootstrap: right after a bootout, launchd can still hold the label
    # for a moment and the first attempt fails with an I/O error.
    if relaunch_agent "gui/$uid" com.termulaa.server; then
        echo "✓ com.termulaa.server loaded (LaunchAgent, logs in ~/.termulaa/logs/)"
    else
        echo "✗ com.termulaa.server failed to load. Check:" >&2
        echo "    launchctl print gui/$uid/com.termulaa.server" >&2
        restart_failed="$restart_failed com.termulaa.server"
    fi

    if rc_paired; then
        if relaunch_agent "gui/$uid" com.termulaa.rc; then
            echo "✓ com.termulaa.rc loaded (token found in ~/.termulaa/rc.json)"
        else
            echo "✗ com.termulaa.rc failed to load. Check:" >&2
            echo "    launchctl print gui/$uid/com.termulaa.rc" >&2
            restart_failed="$restart_failed com.termulaa.rc"
        fi
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
    warn_unmanaged
    report_failures_and_exit
    echo
    echo "Open http://127.0.0.1:17380/"
    exit 0
fi

# ── Restart onto the new binary (default path) ──────────────────────────────
if [ "$RESTART" = 0 ]; then
    echo
    echo "→ --no-restart: binary staged, nothing was restarted."
    echo "   Anything already running keeps the previous version until you"
    echo "   restart it:"
    if [ "$os" = "linux" ]; then
        echo "     systemctl --user restart termulaa termulaa-rc   # if run as services"
    else
        echo "     launchctl kickstart -k gui/\$UID/com.termulaa.server   # if run as agents"
        echo "     launchctl kickstart -k gui/\$UID/com.termulaa.rc"
    fi
    echo "     (plus any termulaa you started by hand)"
    exit 0
fi

case "$os" in
    linux)  restart_services_linux ;;
    darwin) restart_services_darwin ;;
esac
warn_unmanaged
report_failures_and_exit

echo
if [ "$restarted_any" = 1 ] && [ "$unmanaged_count" -gt 0 ]; then
    echo "Services are on $tag; restart the processes above to finish."
elif [ "$restarted_any" = 1 ]; then
    echo "Upgrade complete: $tag is live."
    echo "Open http://127.0.0.1:17380/"
elif [ "$unmanaged_count" -gt 0 ]; then
    echo "Binary is on $tag; restart the processes above to finish."
elif [ "$running_current" = 1 ]; then
    echo "Already up to date ($tag)."
    echo "Open http://127.0.0.1:17380/"
elif [ "$had_binary" = 1 ]; then
    echo "Nothing was running. Start it when ready:"
    echo "  termulaa"
    echo "Then open http://127.0.0.1:17380/"
else
    echo "Run it:"
    echo "  termulaa"
    echo "Then open http://127.0.0.1:17380/"
fi
