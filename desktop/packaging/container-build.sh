#!/bin/bash
# Runs inside ubuntu:22.04 with the repo mounted at /src: build the .deb there so the binary's
# glibc floor is 2.35 (runs on Ubuntu 22.04+ and Debian 12+), whatever the host is.
# usage: container-build.sh VERSION UID:GID   (the owner gets dist/ and desktop/frontend/vendor back)
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq --no-install-recommends build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev \
	dpkg-dev fakeroot make curl ca-certificates >/dev/null
# the Go version desktop/go.mod asks for; ubuntu's own golang is too old to read it
gov=$(awk '/^go /{print $2}' /src/desktop/go.mod)
curl -fsSL "https://go.dev/dl/go${gov}.linux-amd64.tar.gz" | tar -C /usr/local -xz
export PATH=$PATH:/usr/local/go/bin GOTOOLCHAIN=local
cd /src
make deb VERSION="$1"
chown -R "$2" /src/dist /src/desktop/frontend/vendor
