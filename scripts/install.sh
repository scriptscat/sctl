#!/bin/sh
# scripts/install.sh — one-line installer for sctl on macOS and Linux.
#
#   curl -fsSL https://raw.githubusercontent.com/scriptscat/sctl/main/scripts/install.sh | sh
#
# Downloads the hyphen-named release archive for this platform, verifies its
# sha256 against checksums.txt from the same release, extracts it, and installs
# the sctl binary atomically (temp file + rename) into SCTL_INSTALL_DIR.
#
# Environment:
#   SCTL_VERSION       version to install (leading "v" accepted); default: latest release
#   SCTL_INSTALL_DIR   install directory; default: $HOME/.local/bin
#   SCTL_LATEST_API    (test seam) latest-release JSON endpoint
#   SCTL_DOWNLOAD_BASE (test seam) base URL for archive + checksums.txt downloads
#
# Requires: POSIX sh, curl, tar, grep, sed, and sha256sum (shasum on macOS).

set -eu

TMP_DIR=$(mktemp -d "${TMPDIR:-/tmp}/sctl-install.XXXXXX")
TMP_INSTALL=
trap 'rm -rf "$TMP_DIR" "$TMP_INSTALL"' 0

LATEST_API=${SCTL_LATEST_API:-https://api.github.com/repos/scriptscat/sctl/releases/latest}
DOWNLOAD_BASE=${SCTL_DOWNLOAD_BASE:-https://github.com/scriptscat/sctl/releases/download}

# 1. Detect platform.
OS=$(uname -s)
case "$OS" in
  Linux) OS=linux ;;
  Darwin) OS=darwin ;;
  *)
    echo "error: unsupported OS '$OS' — install.sh supports macOS and Linux" >&2
    exit 1
    ;;
esac

ARCH=$(uname -m)
case "$ARCH" in
  x86_64) ;;
  aarch64|arm64) ARCH=arm64 ;;
  *)
    echo "error: unsupported architecture '$ARCH' — expected x86_64 or arm64" >&2
    exit 1
    ;;
esac

# 2. Resolve version.
if [ -n "${SCTL_VERSION:-}" ]; then
  VER=${SCTL_VERSION#v}
else
  if ! latest=$(curl -fsSL "$LATEST_API" 2>&1); then
    echo "error: failed to fetch latest release from $LATEST_API" >&2
    exit 1
  fi
  VER=$(printf '%s\n' "$latest" | grep '"tag_name"' | sed -n '1s/.*"tag_name": *"\([^"]*\)".*/\1/p')
  VER=${VER#v}
fi
if [ -z "$VER" ]; then
  echo "error: could not determine sctl version to install" >&2
  exit 1
fi

# 3. Download.
ARCHIVE="sctl-${VER}-${OS}-${ARCH}.tar.gz"
echo "downloading $ARCHIVE"
curl -fsSL -o "$TMP_DIR/$ARCHIVE" "$DOWNLOAD_BASE/v${VER}/$ARCHIVE"
curl -fsSL -o "$TMP_DIR/checksums.txt" "$DOWNLOAD_BASE/v${VER}/checksums.txt"

# 4. Verify sha256 against the matching checksums.txt line.
expected_hash=$(grep -F "  ${ARCHIVE}" "$TMP_DIR/checksums.txt" | sed -n '1s/^\([0-9a-fA-F]\{64\}\) .*/\1/p' || true)
if [ -z "$expected_hash" ]; then
  echo "error: no checksum for $ARCHIVE in checksums.txt" >&2
  exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
  actual_hash=$(sha256sum "$TMP_DIR/$ARCHIVE" | sed 's/^\([0-9a-fA-F]\{64\}\) .*/\1/')
else
  actual_hash=$(shasum -a 256 "$TMP_DIR/$ARCHIVE" | sed 's/^\([0-9a-fA-F]\{64\}\) .*/\1/')
fi
if [ "$actual_hash" != "$expected_hash" ]; then
  echo "error: checksum mismatch for $ARCHIVE" >&2
  echo "  expected: $expected_hash" >&2
  echo "  actual:   $actual_hash" >&2
  exit 1
fi
echo "verified sha256 ($ARCHIVE): $expected_hash"

# 5. Extract; the binary sits in the sctl-<ver>-<os>-<arch>/ directory.
tar -xzf "$TMP_DIR/$ARCHIVE" -C "$TMP_DIR"
BIN="$TMP_DIR/sctl-${VER}-${OS}-${ARCH}/sctl"
if [ ! -f "$BIN" ]; then
  echo "error: sctl executable not found in archive (expected $BIN)" >&2
  exit 1
fi
chmod +x "$BIN"

# 6. Install atomically: temp file in the target dir, then rename over any sctl.
INSTALL_DIR=${SCTL_INSTALL_DIR:-"$HOME/.local/bin"}
mkdir -p "$INSTALL_DIR"
TMP_INSTALL="$INSTALL_DIR/.sctl.install.$$"
cp "$BIN" "$TMP_INSTALL"
chmod 0755 "$TMP_INSTALL"
mv -f "$TMP_INSTALL" "$INSTALL_DIR/sctl"
echo "installed sctl to $INSTALL_DIR/sctl"

# 7. Report: install path, version, and a PATH hint when needed.
if output=$("$INSTALL_DIR/sctl" version 2>&1); then
  printf '%s\n' "$output"
else
  echo "warning: installed, but 'sctl version' failed: $output" >&2
fi

case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *)
    echo
    echo "$INSTALL_DIR is not on your PATH. Add it by putting this line in your"
    echo "shell profile (~/.zshrc, ~/.bashrc, or ~/.profile):"
    echo
    echo "  export PATH=\"$INSTALL_DIR:\$PATH\""
    echo
    echo "sctl will not edit your shell profile for you."
    ;;
esac
