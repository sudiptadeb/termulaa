#!/bin/bash
# Build script for terminal-agent
#
# Usage:
#   ./build.sh                      # build terminal-agent (default)
#   ./build.sh terminal-agent       # same as above
#   ./build.sh 2.0.0                # build with version
#   ./build.sh terminal-agent 2.0.0 # build with version

FAILED_BUILDS=()

PROJECT_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIST_DIR="$PROJECT_ROOT/dist"
README_FILE="$PROJECT_ROOT/resources/dist_readme.md"

usage() {
    echo "Usage: $(basename "$0") [component] [version]"
    echo ""
    echo "Components:"
    echo "  terminal-agent   Local terminal WebSocket agent (default)"
    echo ""
    echo "Version defaults to 0.1.0"
    echo ""
    echo "Output: dist/<os>/<binary>-<arch>-v<version>"
    exit 0
}

if [[ "$1" == "-h" || "$1" == "--help" ]]; then
    usage
fi

# Parse args: first arg is component (or version if numeric), second is version
COMPONENT="terminal-agent"
VERSION="0.1.0"

if [ -n "$1" ]; then
    if [[ "$1" =~ ^[0-9]+\.[0-9]+ ]]; then
        VERSION="$1"
    else
        COMPONENT="$1"
    fi
fi
if [ -n "$2" ]; then
    VERSION="$2"
fi

echo "=== termulaa Build ==="
echo "Component: $COMPONENT"
echo "Version:   $VERSION"
echo "Output:    $DIST_DIR"
echo ""

build_one() {
    local CMD_NAME=$1
    local CMD_PATH=$2
    local GOOS=$3
    local GOARCH=$4
    local OUTPUT_NAME="$CMD_NAME-$GOARCH-v$VERSION"

    [ "$GOOS" = "windows" ] && OUTPUT_NAME="$CMD_NAME-$GOARCH-v$VERSION.exe"

    local TARGET_DIR="$DIST_DIR/$GOOS"
    mkdir -p "$TARGET_DIR"

    echo "→ Building $CMD_NAME $GOOS/$GOARCH..."
    cd "$PROJECT_ROOT/src"

    local BUILD_TIME
    BUILD_TIME=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
    local GIT_COMMIT
    GIT_COMMIT=$(git -C "$PROJECT_ROOT" rev-parse --short HEAD 2>/dev/null || echo "unknown")
    local GIT_TAG
    GIT_TAG=$(git -C "$PROJECT_ROOT" describe --tags --always --dirty 2>/dev/null || echo "$VERSION")

    if CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" go build \
        -ldflags "-X main.Version=$GIT_TAG -X main.BuildTime=$BUILD_TIME -X main.GitCommit=$GIT_COMMIT -s -w" \
        -o "$TARGET_DIR/$OUTPUT_NAME" \
        "$CMD_PATH"; then
        [ "$GOOS" != "windows" ] && chmod +x "$TARGET_DIR/$OUTPUT_NAME"
        echo "  ✓ $CMD_NAME $GOOS/$GOARCH"
    else
        echo "  ✗ $CMD_NAME $GOOS/$GOARCH FAILED"
        FAILED_BUILDS+=("$CMD_NAME-$GOOS-$GOARCH")
    fi
}

build_all_platforms() {
    local CMD_NAME=$1
    local CMD_PATH=$2
    build_one "$CMD_NAME" "$CMD_PATH" "linux" "amd64"
    build_one "$CMD_NAME" "$CMD_PATH" "linux" "arm64"
    build_one "$CMD_NAME" "$CMD_PATH" "darwin" "amd64"
    build_one "$CMD_NAME" "$CMD_PATH" "darwin" "arm64"
}

rm -rf "$DIST_DIR"
mkdir -p "$DIST_DIR"

if [ -f "$README_FILE" ]; then
    cp "$README_FILE" "$DIST_DIR/README.md"
fi

case "$COMPONENT" in
    terminal-agent)
        build_all_platforms "terminal-agent" "./cmd/terminal-agent"
        ;;
    *)
        echo "Unknown component: $COMPONENT"
        usage
        ;;
esac

echo ""
if [ ${#FAILED_BUILDS[@]} -gt 0 ]; then
    echo "⚠️  Build completed with errors:"
    for fb in "${FAILED_BUILDS[@]}"; do
        echo "   - $fb"
    done
    echo ""
    echo "Successful builds available in: $DIST_DIR"
    exit 1
else
    echo "✅ Build complete! Output: $DIST_DIR"
fi
