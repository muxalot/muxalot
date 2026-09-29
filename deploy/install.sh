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

# every tool the installer and the agent need, checked up front so nothing fails halfway
missing=""
for cmd in curl sha256sum grep mktemp systemctl useradd tmux; do
  command -v "$cmd" >/dev/null 2>&1 || missing="$missing $cmd"
done
if [ -n "$missing" ]; then
  echo "missing required commands:$missing" >&2
  if command -v apt-get >/dev/null 2>&1; then hint="apt-get install curl coreutils grep passwd tmux systemd"
  elif command -v dnf >/dev/null 2>&1; then hint="dnf install curl coreutils grep shadow-utils tmux systemd"
  elif command -v pacman >/dev/null 2>&1; then hint="pacman -S curl coreutils grep shadow tmux systemd"
  elif command -v apk >/dev/null 2>&1; then hint="apk add curl coreutils grep shadow tmux (Alpine has no systemd; muxalot-agent needs it)"
  else hint="install the missing commands with your package manager"; fi
  echo "try: $hint" >&2
  exit 1
fi
[ -d /run/systemd/system ] || { echo "systemd is not running on this machine; muxalot-agent's installer needs it (run the agent yourself instead: muxalot-agent serve)" >&2; exit 1; }

name="muxalot-agent-linux-$arch"
base="https://github.com/muxalot/muxalot/releases/latest/download"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

curl -fsSL -o "$tmp/$name" "$base/$name"
curl -fsSL -o "$tmp/SHA256SUMS" "$base/SHA256SUMS"
(cd "$tmp" && grep " $name\$" SHA256SUMS | sha256sum -c -)
chmod +x "$tmp/$name"
"$tmp/$name" install "$@"
