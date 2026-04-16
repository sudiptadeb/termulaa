#!/bin/bash
# Start the terminal agent for local development.
# Can be run from anywhere.
# Usage: ./run-terminal-agent.sh [-port 17380]

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

pkill -f "terminal-agent-" 2>/dev/null || true
sleep 0.3

BINARY="$PROJECT_ROOT/dist/darwin/terminal-agent-arm64-v1.0.0"
if [ ! -f "$BINARY" ]; then
    echo "Binary not found, building..."
    bash "$PROJECT_ROOT/build/build.sh" terminal-agent
fi

echo "Starting terminal agent..."
echo "  URL: http://127.0.0.1:17380/"
echo ""
echo "Tip: to background: nohup bash $0 $@ > terminal-agent.log 2>&1 &"
echo ""

exec "$BINARY" "$@"
