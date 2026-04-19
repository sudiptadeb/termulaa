#!/bin/bash
# termulaa installer — downloads the latest release binary for the host's
# OS and architecture and installs it to ~/.local/bin/termulaa.
#
#   curl -fsSL https://raw.githubusercontent.com/sudiptadeb/termulaa/main/install.sh | bash
#
# Environment overrides:
#   INSTALL_DIR   target directory (default: ~/.local/bin)
#   VERSION       release tag to install (default: latest)

set -euo pipefail

REPO="sudiptadeb/termulaa"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${VERSION:-latest}"

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

echo
echo "Run it:"
echo "  termulaa"
echo "Then open http://127.0.0.1:17380/"
