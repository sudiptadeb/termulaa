#!/bin/bash
# Terminal Performance Benchmark
#
# Run this script in BOTH terminals to compare:
#   1. Open iTerm2 → run this script
#   2. Open Ulaa Terminal (browser) → run this script
#   3. Compare the numbers
#
# Usage:
#   bash resources/scripts/terminal-bench.sh          # run all tests
#   bash resources/scripts/terminal-bench.sh quick     # throughput only
#   bash resources/scripts/terminal-bench.sh <test>    # run specific test

set -e

# ── Helpers ──────────────────────────────────────────────────

RED='\033[31m'
GREEN='\033[32m'
YELLOW='\033[33m'
CYAN='\033[36m'
DIM='\033[2m'
BOLD='\033[1m'
RESET='\033[0m'

COLS=$(tput cols 2>/dev/null || echo 80)
ROWS=$(tput lines 2>/dev/null || echo 24)

# Accumulate results to print summary at end
RESULTS=""

header() {
    echo ""
    echo -e "${BOLD}${CYAN}━━━ $1 ━━━${RESET}"
    echo -e "${DIM}$2${RESET}"
    echo ""
}

bench() {
    # Output MUST flow through the terminal — that's what we're measuring.
    # Clear screen after each test so the flood doesn't obscure results.
    local label=$1
    shift
    local start end elapsed
    start=$(python3 -c "import time; print(time.time())")
    "$@"
    end=$(python3 -c "import time; print(time.time())")
    elapsed=$(python3 -c "print(f'{$end - $start:.3f}')")
    printf '\033[2J\033[H'
    printf "  ${GREEN}%-30s${RESET} %s sec\n" "$label" "$elapsed"
    RESULTS="${RESULTS}$(printf "  %-30s %s sec\n" "$label" "$elapsed")\n"
}

# ── Tests ────────────────────────────────────────────────────

test_throughput() {
    header "THROUGHPUT" "How fast the terminal can consume output (lower = faster)"

    bench "100K lines (seq)" seq 1 100000
    bench "50K dense lines" bash -c "yes 'The quick brown fox jumps over the lazy dog' | head -50000"
    bench "5MB random base64" bash -c "dd if=/dev/urandom bs=1024 count=5120 2>/dev/null | base64"
    bench "200K short lines" bash -c "yes 'ok' | head -200000"
}

test_ansi_parsing() {
    header "ANSI PARSING" "Escape sequence processing speed (lower = faster)"

    bench "256-color palette x100" bash -c '
        for rep in $(seq 1 100); do
            for i in $(seq 0 255); do
                printf "\033[38;5;%dm#" $i
            done
        done
    '

    bench "True color gradient 10K" bash -c '
        for i in $(seq 0 10000); do
            r=$((i % 256))
            g=$(((i * 3) % 256))
            b=$(((i * 7) % 256))
            printf "\033[38;2;%d;%d;%dm█" $r $g $b
        done
    '

    bench "Style cycling 50K" bash -c '
        for i in $(seq 1 50000); do
            printf "\033[1mbold\033[0m \033[3mitalic\033[0m \033[4munderline\033[0m "
        done
    '
}

test_cursor() {
    header "CURSOR MOVEMENT" "Repositioning and screen manipulation (lower = faster)"

    bench "Cursor jumps 5K" bash -c "
        for i in \$(seq 1 5000); do
            r=\$((RANDOM % $ROWS + 1))
            c=\$((RANDOM % $COLS + 1))
            printf '\033[%d;%dHx' \$r \$c
        done
    "

    bench "Clear+redraw 500x" bash -c '
        for i in $(seq 1 500); do
            printf "\033[2J\033[HFrame %d" $i
            for j in $(seq 1 10); do
                printf "\033[%d;1HLine %d of frame %d" $((j+1)) $j $i
            done
        done
    '

    bench "Scroll region 2K" bash -c '
        printf "\033[1;20r"
        for i in $(seq 1 2000); do
            printf "\033[20;1H\nScroll line %d" $i
        done
        printf "\033[r"
    '
}

test_unicode() {
    header "UNICODE" "Wide character and emoji rendering (lower = faster)"

    bench "CJK 20K chars" python3 -c "
import sys
sys.stdout.write('你好世界天地人和' * 2500)
"

    bench "Emoji 5K chars" python3 -c "
import sys
sys.stdout.write('😀😁😂🤣😃😄😅😆😉😊😋😎😍😘🥰😗😙🥲😚☺😌😛😝😜🤪🤨🧐🤓😎🥸🤩🥳😏😒😞😔😟😕' * 125)
"

    bench "Mixed ASCII+CJK 50K" python3 -c "
import sys
sys.stdout.write(('Hello 你好 World 世界 Test テスト\n') * 2000)
"
}

test_stress() {
    header "STRESS" "Sustained heavy load (lower = faster)"

    bench "1M lines" seq 1 1000000

    bench "100K colored lines" bash -c '
        for i in $(seq 1 100000); do
            printf "\033[%dm[%05d] The quick brown fox jumps over the lazy dog\033[0m\n" $((31 + i % 7)) $i
        done
    '
}

# ── Main ─────────────────────────────────────────────────────

echo -e "${BOLD}Terminal Performance Benchmark${RESET}"
echo -e "${DIM}Terminal: ${COLS}x${ROWS}  Shell: $SHELL  Date: $(date '+%Y-%m-%d %H:%M')${RESET}"
echo -e "${DIM}TERM=$TERM${RESET}"

TEST=${1:-all}

case "$TEST" in
    quick|throughput)  test_throughput ;;
    ansi|color)        test_ansi_parsing ;;
    cursor)            test_cursor ;;
    unicode)           test_unicode ;;
    stress)            test_stress ;;
    all)
        test_throughput
        test_ansi_parsing
        test_cursor
        test_unicode
        test_stress
        ;;
    *)
        echo "Usage: $0 [quick|throughput|ansi|cursor|unicode|stress|all]"
        exit 1
        ;;
esac

# Print summary of all results
echo ""
echo -e "${BOLD}━━━ SUMMARY ━━━${RESET}"
echo -e "$RESULTS"
echo -e "${BOLD}Done.${RESET} Run in both terminals and compare the numbers."
