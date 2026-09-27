#!/bin/sh
set -e

# ticfac installer (tick 4yb)
# Usage: curl -fsSL https://raw.githubusercontent.com/pengelbrecht/ticfac/main/install.sh | sh
#
# That URL is the stable install URL: it serves this script from main's tip,
# and the script resolves the latest release itself, so one command installs
# the same thing on every machine, and a machine that has never built ticfac
# gets a working one.
#
# The release archives carry BOTH binaries — ticfac and ticfac-exec-subprocess,
# the local executor that supervises every attempt — because a run refuses to
# start unless the executor sits beside the ticfac that dispatches it
# (internal/reconcile's supervisorArgv looks there first). They are installed
# side by side.

REPO="pengelbrecht/ticfac"
# Mirrors and internal/release's end-to-end test override where releases are
# resolved from; the default is GitHub, which is where the tagged releases
# (.github/workflows/release.yml + goreleaser) land.
BASE_URL="${TICFAC_RELEASE_BASE_URL:-https://github.com}"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

# Detect OS
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
    linux*) OS="linux" ;;
    darwin*) OS="darwin" ;;
    *) echo "Unsupported OS: $OS — releases are cut for linux and darwin"; exit 1 ;;
esac

# Detect architecture
ARCH=$(uname -m)
case "$ARCH" in
    x86_64|amd64) ARCH="amd64" ;;
    arm64|aarch64) ARCH="arm64" ;;
    *) echo "Unsupported architecture: $ARCH — releases are cut for amd64 and arm64"; exit 1 ;;
esac

# Get the latest version by resolving the /releases/latest redirect, which
# avoids GitHub API rate limits in shared-IP environments — the same trick
# ticks' installer uses.
LATEST_URL=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$BASE_URL/$REPO/releases/latest") || {
    echo "Failed to resolve the latest release from $BASE_URL/$REPO"
    exit 1
}
VERSION=${LATEST_URL##*/v}
if [ -z "$VERSION" ] || [ "$VERSION" = "$LATEST_URL" ]; then
    echo "Failed to get latest version from: $LATEST_URL"
    exit 1
fi

echo "Installing ticfac v$VERSION for $OS/$ARCH..."

# The download URL — the archive name goreleaser's name_template cuts
# ({{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}, tar.gz).
URL="$BASE_URL/$REPO/releases/download/v$VERSION/ticfac_${VERSION}_${OS}_${ARCH}.tar.gz"

# Create the install directory
mkdir -p "$INSTALL_DIR"

# Download and extract
TMP=$(mktemp -d)
# Single quotes: $TMP is expanded when the trap FIRES, not when it is set,
# and the inner double quotes keep that expansion one word — mktemp's answer
# can carry a space (GNU mktemp honors TMPDIR, and a TMPDIR with a space in
# it is a legal one), and an unquoted expansion here silently removes two
# wrong paths instead of the temp dir.
trap 'rm -rf "$TMP"' EXIT

curl -fsSL "$URL" | tar -xz -C "$TMP"

# Install both binaries side by side
for BINARY in ticfac ticfac-exec-subprocess; do
    if [ ! -f "$TMP/$BINARY" ]; then
        echo "The release archive did not carry $BINARY"
        exit 1
    fi
    mv "$TMP/$BINARY" "$INSTALL_DIR/$BINARY"
    chmod +x "$INSTALL_DIR/$BINARY"
done

echo "Installed ticfac v$VERSION and ticfac-exec-subprocess to $INSTALL_DIR"

# Check if in PATH
case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *)
        echo ""
        echo "Add to your PATH:"
        echo "  export PATH=\"\$PATH:$INSTALL_DIR\""
        ;;
esac

echo ""
echo "Run 'ticfac doctor' to check what a run needs on this machine"
echo "Run 'ticfac skills install ticfac' for the execution skill"
