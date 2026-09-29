#!/bin/sh
# Download the latest muxalot-agent release, verify its checksum, and run its installer.
# Usage: curl -fsSL https://raw.githubusercontent.com/muxalot/muxalot/main/deploy/install.sh | sudo sh
# Extra arguments go to `muxalot-agent install` (e.g. --user, --listen, --files-root).
set -eu

[ "$(uname -s)" = Linux ] || { echo "muxalot-agent supports Linux only" >&2; exit 1; }
[ "$(id -u)" = 0 ] || { echo "run as root (sudo)" >&2; exit 1; }
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
command -v tmux >/dev/null 2>&1 || echo "warning: tmux is not installed; install it before using the app" >&2

name="muxalot-agent-linux-$arch"
base="https://github.com/muxalot/muxalot/releases/latest/download"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

curl -fsSL -o "$tmp/$name" "$base/$name"
curl -fsSL -o "$tmp/SHA256SUMS" "$base/SHA256SUMS"
(cd "$tmp" && grep " $name\$" SHA256SUMS | sha256sum -c -)
chmod +x "$tmp/$name"
"$tmp/$name" install "$@"
