#!/usr/bin/env bash
# Install scout binary.
# Usage: curl -fsSL https://raw.githubusercontent.com/klarlabs-studio/scout/main/install.sh | bash
set -euo pipefail

REPO="klarlabs-studio/scout"
BINARY="scout"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"

# Detect OS and arch
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
    x86_64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

# Get latest version
VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"v([^"]+)".*/\1/')
if [ -z "$VERSION" ]; then
    echo "Failed to determine latest version"
    exit 1
fi

echo "Installing ${BINARY} v${VERSION} (${OS}/${ARCH})..."

# Download and extract
URL="https://github.com/${REPO}/releases/download/v${VERSION}/${BINARY}_${VERSION}_${OS}_${ARCH}.tar.gz"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

curl -fsSL "$URL" | tar xz -C "$TMP"

# Install
if [ -w "$INSTALL_DIR" ]; then
    mv "$TMP/$BINARY" "$INSTALL_DIR/$BINARY"
else
    sudo mv "$TMP/$BINARY" "$INSTALL_DIR/$BINARY"
fi

chmod +x "$INSTALL_DIR/$BINARY"
echo "Installed $BINARY to $INSTALL_DIR/$BINARY"
echo ""
echo "Configure in your MCP client:"
echo ""
echo "  Claude Code:   claude mcp add browse -- $INSTALL_DIR/$BINARY"
echo "  Claude Desktop: Add to claude_desktop_config.json:"
echo "    {\"mcpServers\": {\"browse\": {\"command\": \"$INSTALL_DIR/$BINARY\"}}}"
echo ""
