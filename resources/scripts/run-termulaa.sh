#!/bin/bash
# Start the terminal agent for local development.
# Can be run from anywhere.
# Usage: ./run-termulaa.sh [-port 17380]

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

pkill -f "termulaa-" 2>/dev/null || true
sleep 0.3

BINARY="$PROJECT_ROOT/dist/darwin/termulaa-arm64-v0.1.0"
if [ ! -f "$BINARY" ]; then
    echo "Binary not found, building..."
    bash "$PROJECT_ROOT/build/build.sh" termulaa
fi

echo "Starting terminal agent..."
echo "  URL: http://127.0.0.1:17380/"
echo ""
echo "Tip: to background: nohup bash $0 $@ > termulaa.log 2>&1 &"
echo ""

exec "$BINARY" "$@"
