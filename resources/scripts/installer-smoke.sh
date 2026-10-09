#!/bin/bash
# Installer smoke test for CI. Runs install.sh against the locally built
# binary and checks what a user would see: the terminal server answers, the
# pairing is saved, the tunnel agent runs without the token on its command
# line, and a re-run leaves the terminal server alone.
#
# Run build/build.sh first. It installs into the real ~/.local/bin and starts
# real processes, so it is meant for throwaway CI machines.

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

fail() {
    echo "FAIL: $*" >&2
    exit 1
}

# kind: "rc" for the tunnel agent, "server" for the terminal server.
pid_of() {
    local kind="$1" pid args
    for pid in $(pgrep -x -u "$(id -u)" termulaa 2>/dev/null || true); do
        args=$(ps -p "$pid" -o args= 2>/dev/null || true)
        case " $args " in
            *" -rc"*) if [ "$kind" = "rc" ]; then echo "$pid"; return 0; fi ;;
            *)        if [ "$kind" = "server" ]; then echo "$pid"; return 0; fi ;;
        esac
    done
    return 0
}

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

server_answers() { curl -fsS -o /dev/null -m 3 http://127.0.0.1:17380/; }
agent_runs() { [ -n "$(pid_of rc)" ]; }

echo "── first run: install, pair, start ──"
bash "$root/install.sh" --service --rc-server http://127.0.0.1:9 --rc-token v1.smoke-one --rc-label ci-smoke

wait_for "the terminal server" server_answers
wait_for "the tunnel agent" agent_runs
grep -q 'smoke-one' "$HOME/.termulaa/rc.json" || fail "token not saved to rc.json"
grep -q 'ci-smoke' "$HOME/.termulaa/rc.json" || fail "label not saved to rc.json"

agent=$(pid_of rc)
if ps -p "$agent" -o args= | grep -q 'smoke-one'; then
    fail "the token is on the tunnel agent's command line"
fi
server_before=$(pid_of server)
[ -n "$server_before" ] || fail "no terminal server process"

echo "── second run: new token, same binary ──"
bash "$root/install.sh" --service --rc-token v1.smoke-two

wait_for "the terminal server" server_answers
wait_for "the tunnel agent" agent_runs
grep -q 'smoke-two' "$HOME/.termulaa/rc.json" || fail "new token not saved to rc.json"
grep -q 'ci-smoke' "$HOME/.termulaa/rc.json" || fail "label lost on re-pair"

server_after=$(pid_of server)
if [ "$server_before" != "$server_after" ]; then
    fail "the terminal server was restarted ($server_before → $server_after); its terminals would be gone"
fi

echo "OK: installer smoke test passed (server PID $server_after kept, agent PID $(pid_of rc))"
