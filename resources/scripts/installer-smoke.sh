#!/bin/bash
# Installer smoke test for CI. Runs install.sh against the locally built
# binary and checks what a user would see: the terminal server answers, the
# pairing is saved, the tunnel agent runs without the token on its command
# line, both run with --auto-update, and every kind of re-run is idempotent:
# nothing new changes nothing, a new token restarts only the agent, a
# machine set up by the old installer (no --auto-update) is brought over
# without restarting its server, and where amarit is installed system-wide
# the services are handed to amarit once and then left alone.
#
# Run build/build.sh first. It installs into the real ~/.local/bin and starts
# real processes, so it is meant for throwaway CI machines. The amarit part
# runs when `amarit` is on PATH and either AMARIT_HOME names a scratch daemon
# that is already running (local use) or passwordless sudo is available to
# install amarit system-wide (CI); otherwise it is skipped with a note.

set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
ver="v$(tr -d ' \r\n' < "$root/VERSION")"
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$(uname -m)" in
    x86_64|amd64)  arch=amd64 ;;
    arm64|aarch64) arch=arm64 ;;
    *) echo "unsupported arch: $(uname -m)" >&2; exit 1 ;;
esac

bin="$root/dist/$os/termulaa-$arch-$ver"
if [ ! -x "$bin" ]; then
    echo "missing $bin — run build/build.sh first" >&2
    exit 1
fi

# Pin the installer to the version the built binary reports (an untagged
# build says v0.5.2-dev+sha), so it never reaches for a GitHub release and
# the test works offline and before the release of this very version exists.
export INSTALL_DIR="$HOME/.local/bin"
export VERSION
VERSION="$("$bin" -version)"
mkdir -p "$INSTALL_DIR"
cp "$bin" "$INSTALL_DIR/termulaa"
dest="$INSTALL_DIR/termulaa"

fail() {
    echo "FAIL: $*" >&2
    exit 1
}

# Only processes running the installed binary count, so a termulaa from
# elsewhere on the machine never gets mistaken for one of ours.
# kind: "rc" for the tunnel agent, "server" for the terminal server.
pid_of() {
    local kind="$1" pid args
    for pid in $(pgrep -u "$(id -u)" -f "^$dest( |\$)" 2>/dev/null || true); do
        args=$(ps -p "$pid" -o args= 2>/dev/null || true)
        case " $args " in
            *" -rc"*) if [ "$kind" = "rc" ]; then echo "$pid"; return 0; fi ;;
            *)        if [ "$kind" = "server" ]; then echo "$pid"; return 0; fi ;;
        esac
    done
    return 0
}

count_termulaa() { pgrep -u "$(id -u)" -f "^$dest( |\$)" 2>/dev/null | wc -l | tr -d ' '; }

# The server's port: ~/.termulaa/config.json may move it off 17380.
port=17380
if [ -f "$HOME/.termulaa/config.json" ]; then
    port=$(sed -n 's/.*"port"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' "$HOME/.termulaa/config.json" | head -1)
    port=${port:-17380}
fi

has_flag() { ps -p "$1" -o args= 2>/dev/null | grep -q -- '--auto-update'; }

wait_for() {
    local what="$1" i=0
    shift
    until "$@" >/dev/null 2>&1; do
        i=$((i + 1))
        if [ "$i" -ge 30 ]; then
            fail "timed out waiting for $what"
        fi
        sleep 1
    done
}

server_answers() { curl -fsS -o /dev/null -m 3 "http://127.0.0.1:$port/"; }
agent_runs() { [ -n "$(pid_of rc)" ]; }

install() {
    bash "$root/install.sh" "$@" 2>&1 | tee "$root/.tmp/smoke-last.out"
    return "${PIPESTATUS[0]}"
}
mkdir -p "$root/.tmp"

# Which path the installer takes on this machine.
if [ "$os" = linux ] && systemctl --user show-environment >/dev/null 2>&1; then
    path=systemd
elif [ "$os" = darwin ] && launchctl print "gui/$(id -u)" >/dev/null 2>&1; then
    path=launchd
else
    path=detached
fi
echo "service path on this machine: $path"

echo "── 1. fresh install: pair, start, both with --auto-update ──"
install --service --rc-server http://127.0.0.1:9 --rc-token v1.smoke-one --rc-label ci-smoke
wait_for "the terminal server" server_answers
wait_for "the tunnel agent" agent_runs
grep -q 'smoke-one' "$HOME/.termulaa/rc.json" || fail "token not saved to rc.json"
grep -q 'ci-smoke' "$HOME/.termulaa/rc.json" || fail "label not saved to rc.json"
server1=$(pid_of server); agent1=$(pid_of rc)
[ -n "$server1" ] || fail "no terminal server process"
ps -p "$agent1" -o args= | grep -q 'smoke-one' && fail "the token is on the tunnel agent's command line"
has_flag "$server1" || fail "the terminal server runs without --auto-update"
has_flag "$agent1" || fail "the tunnel agent runs without --auto-update"
[ "$(count_termulaa)" = 2 ] || fail "expected one server and one agent, found $(count_termulaa) processes"

echo "── 2. re-run with nothing new, twice: nothing restarts ──"
for n in 1 2; do
    install --service
    wait_for "the terminal server" server_answers
    [ "$(pid_of server)" = "$server1" ] || fail "re-run $n restarted the terminal server ($server1 → $(pid_of server))"
    [ "$(pid_of rc)" = "$agent1" ] || fail "re-run $n restarted the tunnel agent ($agent1 → $(pid_of rc))"
done

echo "── 3. new token: the agent restarts, the server is kept ──"
install --service --rc-token v1.smoke-two
wait_for "the terminal server" server_answers
wait_for "the tunnel agent" agent_runs
grep -q 'smoke-two' "$HOME/.termulaa/rc.json" || fail "new token not saved to rc.json"
grep -q 'ci-smoke' "$HOME/.termulaa/rc.json" || fail "label lost on re-pair"
[ "$(pid_of server)" = "$server1" ] || fail "the terminal server was restarted for a new token; its terminals would be gone"
agent3=$(pid_of rc)
[ "$agent3" != "$agent1" ] || fail "the tunnel agent did not pick up the new token"
has_flag "$agent3" || fail "the restarted tunnel agent runs without --auto-update"

echo "── 4. a machine set up by the old installer (no --auto-update) ──"
# Put the machine in the state the 0.6.0 installer left: the same services,
# without the flag, running.
strip_flag() { sed 's/ --auto-update//; /<string>--auto-update<\/string>/d' "$1" > "$1.tmp" && mv "$1.tmp" "$1"; }
case "$path" in
    systemd)
        for u in termulaa termulaa-rc; do strip_flag "$HOME/.config/systemd/user/$u.service"; done
        systemctl --user daemon-reload
        systemctl --user restart termulaa.service termulaa-rc.service
        ;;
    launchd)
        for l in com.termulaa.server com.termulaa.rc; do
            strip_flag "$HOME/Library/LaunchAgents/$l.plist"
            launchctl bootout "gui/$(id -u)/$l" 2>/dev/null || true
            sleep 1
            launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/$l.plist"
        done
        ;;
    detached)
        pkill -u "$(id -u)" -f "^$dest( |\$)" || true
        sleep 1
        nohup "$dest" >> "$HOME/.termulaa/logs/server.log" 2>&1 < /dev/null &
        nohup "$dest" -rc >> "$HOME/.termulaa/logs/rc.log" 2>&1 < /dev/null &
        echo $! > "$HOME/.termulaa/rc.pid"
        ;;
esac
wait_for "the old-style terminal server" server_answers
wait_for "the old-style tunnel agent" agent_runs
server4=$(pid_of server); agent4=$(pid_of rc)
has_flag "$server4" && fail "precondition: old-style server should run without the flag"
install --service
wait_for "the terminal server" server_answers
wait_for "the tunnel agent" agent_runs
[ "$(pid_of server)" = "$server4" ] || fail "the server was restarted just to add --auto-update ($server4 → $(pid_of server))"
grep -q 'takes effect\|gets it when it next starts' "$root/.tmp/smoke-last.out" || fail "the output does not say when --auto-update takes effect"
case "$path" in
    systemd) grep -q -- '--auto-update' "$HOME/.config/systemd/user/termulaa.service" || fail "unit file not updated" ;;
    launchd) grep -q -- '--auto-update' "$HOME/Library/LaunchAgents/com.termulaa.server.plist" || fail "plist not updated" ;;
esac
agent4b=$(pid_of rc)
[ "$agent4b" != "$agent4" ] || fail "the old-style tunnel agent was not restarted with the flag"
has_flag "$agent4b" || fail "the restarted tunnel agent runs without --auto-update"
[ "$(count_termulaa)" = 2 ] || fail "duplicate processes after the old-installer re-run: $(count_termulaa)"
install --service
[ "$(pid_of server)" = "$server4" ] && [ "$(pid_of rc)" = "$agent4b" ] || fail "a further re-run restarted something"

echo "── 5. amarit installed system-wide: hand over once, then leave alone ──"
amarit_ready=0
if ! command -v amarit >/dev/null 2>&1; then
    echo "SKIP: amarit is not on PATH"
elif [ -n "${AMARIT_HOME:-}" ]; then
    amarit ls | head -1 | grep -q 'pid' && amarit_ready=1 || echo "SKIP: no scratch daemon at AMARIT_HOME=$AMARIT_HOME"
elif sudo -n true 2>/dev/null; then
    sudo "$(command -v amarit)" install --system
    for i in $(seq 20); do amarit ls | head -1 | grep -q 'pid' && break; sleep 0.5; done
    amarit ls | head -1 | grep -q 'pid' || fail "amarit daemon did not come up after install --system"
    amarit_ready=1
else
    echo "SKIP: no passwordless sudo to install amarit system-wide"
fi
if [ "$amarit_ready" = 1 ]; then
    if [ "$path" = detached ]; then
        # The installer never kills a server it did not start as a service;
        # a user would stop a hand-started one, as its message says.
        kill "$server4"; sleep 1
    fi
    install --service
    sleep 1
    [ "$(amarit ls | awk '$1=="termulaa"{print $2}')" = running ] || fail "amarit unit termulaa not running"
    [ "$(amarit ls | awk '$1=="termulaa-rc"{print $2}')" = running ] || fail "amarit unit termulaa-rc not running"
    grep -q "\"$dest\"" "${AMARIT_HOME:-$HOME/.amarit}/units/termulaa.json" || fail "amarit unit does not run the installed binary"
    case "$path" in
        systemd) [ ! -f "$HOME/.config/systemd/user/termulaa.service" ] || fail "old systemd unit left behind"
                 systemctl --user is-active --quiet termulaa.service && fail "old systemd unit still active" ;;
        launchd) [ ! -f "$HOME/Library/LaunchAgents/com.termulaa.server.plist" ] || fail "old plist left behind"
                 launchctl print "gui/$(id -u)/com.termulaa.server" >/dev/null 2>&1 && fail "old LaunchAgent still loaded" ;;
    esac
    grep -q 'moves from the service manager to amarit' "$root/.tmp/smoke-last.out" || fail "the output does not say the switch closes terminals"
    wait_for "the terminal server under amarit" server_answers
    [ "$(count_termulaa)" = 2 ] || fail "duplicate processes after the amarit switch: $(count_termulaa)"
    u5=$(amarit ls | awk '$1=="termulaa"{print $3}'); r5=$(amarit ls | awk '$1=="termulaa-rc"{print $3}')
    for n in 1 2; do
        install --service
        [ "$(amarit ls | awk '$1=="termulaa"{print $3}')" = "$u5" ] || fail "amarit re-run $n restarted the server unit"
        [ "$(amarit ls | awk '$1=="termulaa-rc"{print $3}')" = "$r5" ] || fail "amarit re-run $n restarted the agent unit"
        [ "$(grep -c 'left alone' "$root/.tmp/smoke-last.out")" = 2 ] || fail "amarit re-run $n did not report both units left alone"
    done
    echo "amarit: switched once, then left alone (server unit pid $u5, agent unit pid $r5)"
fi

echo "OK: installer smoke test passed ($path path; server PID $(pid_of server), agent PID $(pid_of rc))"
