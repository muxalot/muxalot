#!/bin/bash
# Runs inside ubuntu:22.04 with the repo mounted at /src: build the .deb there so the binary's
# glibc floor is 2.35 (runs on Ubuntu 22.04+ and Debian 12+), whatever the host is.
# usage: container-build.sh VERSION UID:GID   (the owner gets dist/ and desktop/frontend/vendor back)
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
# librsvg2-dev / GIRepository dev / gtk dev tools: what linuxdeploy-plugin-gtk requires.
# GIRepository's dev package has different names across Debian/Ubuntu releases (gi 2.0 transition).
apt-get install -y -qq --no-install-recommends build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev \
	libgtk-3-bin librsvg2-dev libgdk-pixbuf2.0-dev file dpkg-dev fakeroot patchelf make curl ca-certificates >/dev/null
gir_ok=""
for gir in libgirepository-2.0-dev libgirepository1.0-dev libgirepository-1.0-dev; do
	apt-get install -y -qq --no-install-recommends "$gir" >/dev/null 2>&1 || continue
	gir_ok=1
	break
done
[ -n "$gir_ok" ] || { echo "no libgirepository dev package found" >&2; exit 1; }
# the Go version desktop/go.mod asks for; ubuntu's own golang is too old to read it
gov=$(awk '/^go /{print $2}' /src/desktop/go.mod)
curl -fsSL "https://go.dev/dl/go${gov}.linux-amd64.tar.gz" | tar -C /usr/local -xz
export PATH=$PATH:/usr/local/go/bin GOTOOLCHAIN=local
cd /src
make deb appimage VERSION="$1"
chown -R "$2" /src/dist /src/desktop/frontend/vendor
