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
ARCHIVE="${BINARY}_${VERSION}_${OS}_${ARCH}.tar.gz"
CHECKSUMS_URL="https://github.com/${REPO}/releases/download/v${VERSION}/${BINARY}_${VERSION}_checksums.txt"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

curl -fsSL "$URL" -o "$TMP/$ARCHIVE"
curl -fsSL "$CHECKSUMS_URL" -o "$TMP/checksums.txt"

EXPECTED_CHECKSUM=$(awk -v asset="$ARCHIVE" '$2 == asset { print $1; exit }' "$TMP/checksums.txt")
if [ -z "$EXPECTED_CHECKSUM" ]; then
    echo "No checksum found for ${ARCHIVE} in the release checksums" >&2
    exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
    printf '%s  %s\n' "$EXPECTED_CHECKSUM" "$TMP/$ARCHIVE" | sha256sum --check --status -
elif command -v shasum >/dev/null 2>&1; then
    printf '%s  %s\n' "$EXPECTED_CHECKSUM" "$TMP/$ARCHIVE" | shasum -a 256 --check - >/dev/null
else
    echo "Cannot verify release archive: sha256sum or shasum is required" >&2
    exit 1
fi

echo "Verified SHA-256 checksum for ${ARCHIVE}."
tar xz -f "$TMP/$ARCHIVE" -C "$TMP"

# Set permissions before moving into a potentially root-owned install directory.
chmod +x "$TMP/$BINARY"

# Install
if [ -w "$INSTALL_DIR" ]; then
    mv "$TMP/$BINARY" "$INSTALL_DIR/$BINARY"
else
    sudo mv "$TMP/$BINARY" "$INSTALL_DIR/$BINARY"
fi

echo "Installed $BINARY to $INSTALL_DIR/$BINARY"
echo ""
echo "Configure in your MCP client:"
echo ""
echo "  Claude Code:   claude mcp add scout -- $INSTALL_DIR/$BINARY mcp serve --profile=core"
echo "  Claude Desktop: Add to claude_desktop_config.json:"
echo "    {\"mcpServers\": {\"scout\": {\"command\": \"$INSTALL_DIR/$BINARY\", \"args\": [\"mcp\", \"serve\", \"--profile=core\"]}}}"
echo ""
