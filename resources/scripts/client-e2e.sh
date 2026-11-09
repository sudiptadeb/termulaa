#!/bin/bash
# End-to-end test of the terminal client (termulaa pair|machines|shell|exec)
# through the real path: client -> rendezvous (memd) -> agent -> termulaa
# server, every piece built from this checkout and run in isolation from any
# termulaa or memd on this machine. Run it before a release that touches the
# client, the session protocol or the agent.
#
#   MEMD_SRC=~/src/memd resources/scripts/client-e2e.sh
#
# Environment:
#   MEMD_SRC          a memd checkout; its server is built into .tmp (required
#                     unless MEMD_BIN is set)
#   MEMD_BIN          an already built memd binary to use instead
#   TERMULAA_OLD_BIN  a released termulaa from before command sessions; when
#                     set, it is also run as the server and agent to prove
#                     exec refuses it cleanly and shell still works
#   E2E_MEMD_PORT, E2E_TERMULAA_PORT   scratch ports (17978, 17981)
#   LIVE_PORT         a termulaa that must stay untouched (17380); its tab
#                     list is read before and after and compared, nothing else
#
# Isolation: HOME is a scratch directory for every process; the agent gets an
# explicit -rc-target and the run aborts unless its log confirms it; nothing
# contacts a host other than 127.0.0.1. Needs: go, python3, curl, shasum.
set -u
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
T="$ROOT/.tmp/client-e2e"
H="$T/home"
MP=${E2E_MEMD_PORT:-17978}; TP=${E2E_TERMULAA_PORT:-17981}; LIVE_PORT=${LIVE_PORT:-17380}
SRV="http://127.0.0.1:$MP"
PIDS=()
pass=0; fail=0
check() { if [ "$2" = "$3" ]; then echo "PASS $1"; pass=$((pass+1)); else echo "FAIL $1: want [$3] got [$2]"; fail=$((fail+1)); fi; }
die() { echo "ABORT: $*" >&2; exit 1; }
cleanup() {
  for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null; done
  pkill -f "sleep 30; echo never" 2>/dev/null
  sleep 0.5
  for p in "${PIDS[@]}"; do kill -9 "$p" 2>/dev/null; done
}
trap cleanup EXIT

[ "$TP" != "$LIVE_PORT" ] && [ "$MP" != "$LIVE_PORT" ] || die "scratch ports must differ from LIVE_PORT"
rm -rf "$T"; mkdir -p "$H" "$T/bin" "$T/logs"

echo "== build"
bash "$ROOT/build/build.sh" >"$T/logs/build.log" 2>&1 || { tail -20 "$T/logs/build.log"; die "build failed"; }
OS=$(uname -s | tr '[:upper:]' '[:lower:]'); ARCH=$(uname -m); [ "$ARCH" = x86_64 ] && ARCH=amd64
BIN="$ROOT/dist/$OS/termulaa-$ARCH-v$(tr -d ' \r\n' < "$ROOT/VERSION")"
[ -x "$BIN" ] || die "no binary at $BIN"
if [ -n "${MEMD_BIN:-}" ]; then
  MEMD="$MEMD_BIN"
else
  [ -n "${MEMD_SRC:-}" ] || die "set MEMD_SRC to a memd checkout (or MEMD_BIN)"
  (cd "$MEMD_SRC/server" && go build -o "$T/bin/memd" ./cmd/memd) || die "memd build failed"
  MEMD="$T/bin/memd"
fi
# go run leaves no binary behind (binaries come only from build/build.sh).
harness() { (cd "$ROOT/src" && go run ./cmd/ptyharness "$@"); }
harness -h >/dev/null 2>&1 || die "harness does not build"

export HOME="$H"
export MEMD_SESSION_SECRET
MEMD_SESSION_SECRET=$(head -c 24 /dev/urandom | base64)
ADMIN_PASS=$(head -c 18 /dev/urandom | base64)

live_tabs() { curl -s --max-time 2 "http://127.0.0.1:$LIVE_PORT/api/tabs" 2>/dev/null | python3 -c 'import json,sys; print(sorted(t["id"] for t in json.load(sys.stdin)))' 2>/dev/null; }
live_before=$(live_tabs)

start_memd() {
  "$MEMD" serve --port "$MP" --init-db "$@" >>"$T/logs/memd.log" 2>&1 &
  MEMD_PID=$!; PIDS+=($MEMD_PID)
  for i in $(seq 40); do curl -fs -o /dev/null "$SRV/api/session" && return 0; sleep 0.25; done
  tail -20 "$T/logs/memd.log"; die "memd did not start"
}
start_server() {
  "${SERVER_BIN:-$BIN}" -port "$TP" >>"$T/logs/term.log" 2>&1 &
  SERVER_PID=$!; PIDS+=($SERVER_PID)
  for i in $(seq 40); do curl -fs -o /dev/null "http://127.0.0.1:$TP/health" && return 0; sleep 0.25; done
  tail -20 "$T/logs/term.log"; die "termulaa did not start"
}
start_agent() {
  : > "$T/logs/agent-current.log"
  "${AGENT_BIN:-$BIN}" -rc -rc-server "$SRV" -rc-token "$RCTOK" -rc-target "127.0.0.1:$TP" -rc-label scratch -rc-tunnels 2 >>"$T/logs/agent-current.log" 2>&1 &
  AGENT_PID=$!; PIDS+=($AGENT_PID)
  for i in $(seq 40); do grep -q "tunnel 0 connected" "$T/logs/agent-current.log" 2>/dev/null && break; sleep 0.25; done
  cat "$T/logs/agent-current.log" >> "$T/logs/agent.log"
  grep -q "target=127.0.0.1:$TP" "$T/logs/agent-current.log" || die "agent is not targeting the scratch server; refusing to continue"
}

echo "== scratch rendezvous, server and agent"
MEMD_CREATE_SUPER_ADMIN_PASSWORD="$ADMIN_PASS" start_memd --create-super-admin admin
J="$T/jar"
curl -fs -c "$J" -H 'Content-Type: application/json' -d "{\"username\":\"admin\",\"password\":\"$ADMIN_PASS\"}" "$SRV/api/auth/login" >/dev/null || { tail -20 "$T/logs/memd.log"; die "memd login failed"; }
RCTOK=$(curl -fs -b "$J" -H 'Content-Type: application/json' -d '{"label":"scratch","ttl":1}' "$SRV/rc/api/tokens" | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')
[ -n "$RCTOK" ] || die "could not mint an agent token"
CODE=$(curl -fs -b "$J" -X POST "$SRV/api/app/pair" | python3 -c 'import json,sys;print(json.load(sys.stdin)["code"])')
start_server
start_agent
check "agent splices to the scratch server" "$(grep -o 'target=127.0.0.1:[0-9]*' "$T/logs/agent.log" | head -1)" "target=127.0.0.1:$TP"
check "scratch server starts with no tabs" "$(curl -s "http://127.0.0.1:$TP/api/tabs")" "[]"

echo "== pair"
"$BIN" pair -server "$SRV" "$CODE"; check "pair exit" $? 0
check "credentials mode 0600" "$(stat -f %Lp "$H/.termulaa/client.json" 2>/dev/null || stat -c %a "$H/.termulaa/client.json")" 600
check "token never printed" "$(grep -rc mat_ "$T/logs" | grep -vc ':0$')" 0
"$BIN" pair -server "$SRV" "$CODE" >/dev/null 2>&1; check "reused code refused (255)" $? 255

echo "== machines"
"$BIN" machines | tee "$T/logs/machines.txt"
check "machines lists scratch" "$(cut -f1 "$T/logs/machines.txt")" scratch
check "machines --json has url" "$("$BIN" machines --json | python3 -c 'import json,sys; m=json.load(sys.stdin)[0]; print(m["label"], m["url"].startswith("/rc/t/"))')" "scratch True"

echo "== exec: output and exit codes"
out=$("$BIN" exec scratch -- echo hello); c=$?
check "exec stdout, no CR" "$out" hello; check "exec exit 0" $c 0
"$BIN" exec scratch -- false >/dev/null; check "exit code 1 from false" $? 1
"$BIN" exec scratch -- 'exit 7' >/dev/null; check "exit code 7 from exit 7" $? 7
"$BIN" exec scratch -- 'kill -9 $$' >/dev/null 2>&1; check "signal death is 137" $? 137
"$BIN" exec scratch -- 'sh -c "kill -TERM \$\$"' >/dev/null 2>&1; check "child killed by TERM gives 143" $? 143
out=$("$BIN" exec scratch -- 'echo out; echo err >&2'); check "stderr merged in order" "$out" "out
err"
out=$("$BIN" exec scratch -- 'printf "a\tb\n"' | od -c | head -1 | tr -s ' ' | sed 's/ *$//'); check "exact bytes: tab kept, newline is LF" "$out" "0000000 a \t b \n"
out=$("$BIN" exec scratch -- 'shopt -q login_shell && echo login' | tail -1); check "runs under a login shell" "$out" login

echo "== exec: quoting"
out=$("$BIN" exec scratch -- "printf '%s' \"it's \\\$HOME\""); check "quotes and dollar" "$out" "it's \$HOME"
out=$("$BIN" exec scratch -- 'printf "[%s]" "a  b" "c d"'); check "spaces inside quotes" "$out" "[a  b][c d]"
out=$("$BIN" exec scratch -- echo 'x*y' '$(echo nope)'); check "words joined then interpreted remotely" "$out" "x*y nope"

echo "== exec: large command line (64 KiB+)"
LONG=$(python3 -c 'print("x"*70000)')
out=$("$BIN" exec scratch -- "echo $LONG" | wc -c | tr -d ' '); check "70000-char command line" "$out" 70001

echo "== exec: large output"
BINFILE="$T/e2e.bin"
REMOTE_SHA=$("$BIN" exec scratch -- "head -c 3000000 /dev/urandom > $BINFILE; base64 < $BINFILE | shasum | cut -d' ' -f1")
LOCAL_SHA=$("$BIN" exec scratch -- "base64 < $BINFILE" | shasum | cut -d' ' -f1)
check "4 MB text output intact" "$LOCAL_SHA" "$REMOTE_SHA"
BIN_SHA=$("$BIN" exec scratch -- "shasum $BINFILE | cut -d' ' -f1")
RAW_SHA=$("$BIN" exec scratch -- "cat $BINFILE" | shasum | cut -d' ' -f1)
check "3 MB binary byte-clean through the PTY" "$RAW_SHA" "$BIN_SHA"

echo "== exec: unknown machine"
"$BIN" exec nosuch -- true 2>"$T/logs/nosuch.err"; check "unknown machine is 255" $? 255
check "offline error names the connected list" "$(grep -c 'connected: scratch' "$T/logs/nosuch.err")" 1

echo "== exec: interrupt"
"$BIN" exec scratch -- 'sleep 30; echo never' >"$T/logs/int.out" 2>"$T/logs/int.err" & P=$!
sleep 2; kill -INT $P; wait $P; check "interrupt gives 130" $? 130
sleep 1
check "interrupted process is gone" "$(pgrep -f 'sleep 30; echo never' | wc -l | tr -d ' ')" 0
check "no tab left after interrupt" "$(curl -s "http://127.0.0.1:$TP/api/tabs")" "[]"

echo "== exec: survives an agent restart"
"$BIN" exec scratch -- 'echo start; sleep 6; echo done; exit 3' >"$T/logs/agent-restart.out" 2>"$T/logs/agent-restart.err" & P=$!
sleep 2; kill $AGENT_PID; wait $AGENT_PID 2>/dev/null; sleep 1; start_agent
wait $P; check "exit code after agent restart" $? 3
check "output after agent restart" "$(tr '\n' ' ' < "$T/logs/agent-restart.out")" "start done "
echo "INFO client reported: $(grep -m1 termulaa "$T/logs/agent-restart.err")"

echo "== exec: survives a rendezvous restart"
"$BIN" exec scratch -- 'echo start; sleep 8; echo done; exit 4' >"$T/logs/memd-restart.out" 2>"$T/logs/memd-restart.err" & P=$!
sleep 2; kill $MEMD_PID; wait $MEMD_PID 2>/dev/null; sleep 1; start_memd
wait $P; check "exit code after rendezvous restart" $? 4
check "output after rendezvous restart" "$(tr '\n' ' ' < "$T/logs/memd-restart.out")" "start done "
echo "INFO client reported: $(grep -m1 termulaa "$T/logs/memd-restart.err")"

echo "== exec: output produced while disconnected is replayed once"
"$BIN" exec scratch -- 'for i in 1 2 3 4 5 6; do echo line-$i; sleep 1; done' >"$T/logs/gap.out" 2>"$T/logs/gap.err" & P=$!
sleep 2; kill $AGENT_PID; wait $AGENT_PID 2>/dev/null; sleep 3; start_agent
wait $P; check "exit after gap" $? 0
check "all six lines, none twice" "$(tr '\n' ' ' < "$T/logs/gap.out")" "line-1 line-2 line-3 line-4 line-5 line-6 "

echo "== server restart: a command session is not re-run"
MARK="$T/ran.txt"
"$BIN" exec scratch -- "echo ran >> $MARK; sleep 30; echo never" >"$T/logs/srv-restart.out" 2>"$T/logs/srv-restart.err" & P=$!
sleep 2; kill -TERM $SERVER_PID; wait $SERVER_PID 2>/dev/null; sleep 1; start_server
wait $P; check "killed command reports 143 after server restart" $? 143
sleep 1
check "command ran exactly once" "$(grep -c ran "$MARK")" 1
check "no tab left after server restart" "$(curl -s "http://127.0.0.1:$TP/api/tabs")" "[]"

echo "== server restart while the client is disconnected: saved exit code and offset"
MARK2="$T/ran2.txt"
"$BIN" exec scratch -- "echo ran >> $MARK2; echo partial; sleep 30; echo never" >"$T/logs/srv-restart2.out" 2>"$T/logs/srv-restart2.err" & P=$!
sleep 2; kill $AGENT_PID; wait $AGENT_PID 2>/dev/null
sleep 1; kill -TERM $SERVER_PID; wait $SERVER_PID 2>/dev/null; sleep 1; start_server
start_agent
wait $P; check "disconnected client learns the saved exit code 143" $? 143
check "disconnected client gets no duplicate output" "$(tr '\n' ' ' < "$T/logs/srv-restart2.out")" "partial "
check "command ran exactly once (restart while disconnected)" "$(grep -c ran "$MARK2")" 1
check "no tab left (restart while disconnected)" "$(curl -s "http://127.0.0.1:$TP/api/tabs")" "[]"

echo "== shell under a real PTY (with an agent restart mid-session)"
harness -hook "kill $AGENT_PID; sleep 1; HOME=$H $BIN -rc -rc-server $SRV -rc-token $RCTOK -rc-target 127.0.0.1:$TP -rc-label scratch -rc-tunnels 2 >>$T/logs/agent.log 2>&1 & echo \$! > $T/agent2.pid" "$BIN" shell scratch
hc=$?
[ -f "$T/agent2.pid" ] && { AGENT_PID=$(cat "$T/agent2.pid"); PIDS+=($AGENT_PID); }
check "pty harness" $hc 0
sleep 1
check "no tab left after shell exit" "$(curl -s "http://127.0.0.1:$TP/api/tabs")" "[]"
"$BIN" shell scratch </dev/null >/dev/null 2>&1; check "shell without a tty is 255" $? 255

if [ -n "${TERMULAA_OLD_BIN:-}" ]; then
  echo "== old machine: $TERMULAA_OLD_BIN ($("$TERMULAA_OLD_BIN" -version)) as server and agent"
  kill $AGENT_PID $SERVER_PID; wait $AGENT_PID $SERVER_PID 2>/dev/null; sleep 1
  SERVER_BIN="$TERMULAA_OLD_BIN" start_server
  AGENT_BIN="$TERMULAA_OLD_BIN" start_agent
  "$BIN" exec scratch -- 'echo should-not-run' >"$T/logs/old.out" 2>"$T/logs/old.err"; check "exec against an old machine is 255" $? 255
  check "error tells them to update that machine" "$(grep -c 'termulaa update.*scratch' "$T/logs/old.err")" 1
  check "nothing printed as output" "$(wc -c < "$T/logs/old.out" | tr -d ' ')" 0
  sleep 1; check "no tab left on the old machine" "$(curl -s "http://127.0.0.1:$TP/api/tabs")" "[]"
  harness "$BIN" shell scratch; check "shell works against an old machine" $? 0
fi

echo "== live server untouched"
check "live termulaa tab list unchanged" "$(live_tabs)" "$live_before"

echo; echo "pass=$pass fail=$fail"
[ $fail -eq 0 ]
