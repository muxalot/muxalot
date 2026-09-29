#!/bin/bash
# Runs inside a container (Ubuntu/Debian): install the .deb from /pkg, check the launcher,
# then launch the app under a virtual display and session bus (no window manager needed).
# usage: docker run --rm -v "$PWD/desktop/packaging/smoke.sh:/smoke.sh:ro" -v "$PWD/dist:/pkg:ro" IMAGE bash /smoke.sh
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
fail() { echo "FAIL: $*" >&2; exit 1; }

echo "image: $(. /etc/os-release; echo "$PRETTY_NAME")"
apt-get update -qq >/dev/null
apt-get install -y -qq --no-install-recommends /pkg/*.deb xvfb xauth dbus dbus-x11 desktop-file-utils x11-utils >/tmp/apt.log 2>&1 \
	|| { tail -15 /tmp/apt.log; fail "apt could not install the package with its dependencies"; }
echo "installed: $(dpkg-query -W -f='${Package} ${Version}' muxalot-desktop)"
[ "$(ldd /usr/bin/muxalot-desktop | grep -c 'not found')" = 0 ] || fail "missing shared libraries"

# launcher: valid, named after the window class GNOME/KDE match on, and everything it points to exists
D=/usr/share/applications/muxalot-desktop.desktop
desktop-file-validate "$D" || fail "desktop-file-validate"
SWC=$(sed -n 's/^StartupWMClass=//p' "$D")
[ "$(basename "$D" .desktop)" = "$SWC" ] || fail "launcher file name and StartupWMClass differ"
command -v "$(sed -n 's/^Exec=//p' "$D" | cut -d' ' -f1)" >/dev/null || fail "Exec is not in PATH"
[ -f "/usr/share/icons/hicolor/scalable/apps/$(sed -n 's/^Icon=//p' "$D").svg" ] || fail "icon file is missing"

export SWC HOME=/tmp/h XDG_CONFIG_HOME=/tmp/h
mkdir -p "$HOME"
cat > /tmp/inner.sh <<'INNER'
fail() { echo "FAIL: $*" >&2; exit 1; }
muxalot-desktop --check || fail "preflight (--check)"
muxalot-desktop > /tmp/app.log 2>&1 &
pid=$!
sleep 10
# any window with the launcher's class (WebKit's own helper windows also appear in the tree)
xwininfo -root -tree | grep -qiE "\(\"$SWC\" \"[^\"]*\"\)" || fail "no window with WM_CLASS instance $SWC"
sleep 16 # the app's load watchdog exits at 20 s, so still running at 26 s means the page loaded
kill -0 "$pid" 2>/dev/null || { cat /tmp/app.log; fail "the app exited (load watchdog or crash)"; }
kill "$pid"
INNER
xvfb-run -a dbus-run-session -- bash /tmp/inner.sh
echo "PASS"
