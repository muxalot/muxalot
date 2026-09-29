#!/bin/bash
# Build <outdir>/muxalot-desktop_<version>_amd64.deb from an already built binary.
# usage: build-deb.sh VERSION BINARY OUTDIR   (needs dpkg-dev and fakeroot)
set -euo pipefail
[ $# -eq 3 ] || { echo "usage: $0 VERSION BINARY OUTDIR" >&2; exit 2; }
# 0.3.0-rc1 -> 0.3.0~rc1: a hyphen is not allowed without a Debian revision, and ~ sorts before the release
ver=${1//-/\~}
bin=$(realpath "$2")
out=$(realpath "$3")
repo=$(cd "$(dirname "$0")/../.." && pwd)
[ "$(dpkg --print-architecture)" = amd64 ] || { echo "the package is amd64 only" >&2; exit 1; }
[ -f "$repo/desktop/frontend/vendor/LICENSE-xterm.txt" ] || { echo "run make desktop-assets first (licence texts)" >&2; exit 1; }

umask 022
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
pkg=$tmp/pkg

install -Dm0755 "$bin" "$pkg/usr/bin/muxalot-desktop"
install -Dm0644 "$repo/desktop/packaging/muxalot-desktop.desktop" "$pkg/usr/share/applications/muxalot-desktop.desktop"
install -Dm0644 "$repo/assets/logo.svg" "$pkg/usr/share/icons/hicolor/scalable/apps/muxalot.svg"
install -d "$pkg/usr/share/doc/muxalot-desktop"
{
	cat "$repo/LICENSE"
	printf '\n----\nBundled xterm.js\n\n'
	cat "$repo/desktop/frontend/vendor/LICENSE-xterm.txt"
	printf '\n----\nBundled JetBrainsMono Nerd Font\n\n'
	cat "$repo/desktop/frontend/vendor/LICENSE-nerdfonts.txt"
	# ponytail: names only; add go-licenses output if a compliance review asks for full texts
	printf '\n----\nGo modules linked into the binary: Wails v3 (MIT), zalando/go-keyring (MIT), gorilla/websocket (BSD-2-Clause), golang.org/x/crypto (BSD-3-Clause).\n'
} > "$pkg/usr/share/doc/muxalot-desktop/copyright"
chmod 0644 "$pkg/usr/share/doc/muxalot-desktop/copyright"
find "$pkg" -type d -exec chmod 0755 {} +

# Depends come from the binary's real shared-library needs, built on the oldest supported distro,
# so they stay correct across the t64 renames (the renamed packages provide the old names).
mkdir -p "$tmp/debian"
printf 'Source: x\nPackage: muxalot-desktop\nArchitecture: amd64\n' > "$tmp/debian/control"
deps=$(cd "$tmp" && dpkg-shlibdeps -O -e"$pkg/usr/bin/muxalot-desktop" 2>/dev/null | sed -n 's/^shlibs:Depends=//p')
[ -n "$deps" ] || { echo "dpkg-shlibdeps produced no dependencies" >&2; exit 1; }

mkdir -p "$pkg/DEBIAN"
cat > "$pkg/DEBIAN/control" <<EOF
Package: muxalot-desktop
Version: $ver
Architecture: amd64
Maintainer: muxalot <noreply@users.noreply.github.com>
Installed-Size: $(du -sk "$pkg/usr" | cut -f1)
Depends: $deps
Recommends: gnome-keyring | kwalletd5 | kwalletd6
Section: net
Priority: optional
Homepage: https://github.com/muxalot/muxalot
Description: Desktop client for muxalot remote tmux sessions
 Connects to a muxalot agent over HTTPS with a signed device key and shows
 its tmux sessions in tabs. Uses the system WebKitGTK to render the terminal.
EOF

fakeroot dpkg-deb --build --root-owner-group "$pkg" "$out/muxalot-desktop_${ver}_amd64.deb"
