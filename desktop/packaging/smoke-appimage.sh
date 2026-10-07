#!/bin/bash
# Runs inside a container (Ubuntu/Debian): launch the AppImage from /pkg with NO GTK or
# WebKitGTK on the host, under a virtual display and session bus (no window manager
# needed). This is the point of the AppImage: the container is the assertion.
# usage: docker run --rm -v "$PWD/desktop/packaging/smoke-appimage.sh:/smoke.sh:ro" -v "$PWD/dist:/pkg:ro" IMAGE bash /smoke.sh
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
fail() { echo "FAIL: $*" >&2; exit 1; }

echo "image: $(. /etc/os-release; echo "$PRETTY_NAME")"
apt-get update -qq >/dev/null
# The AppImage carries WebKitGTK and the text stack, but assumes the host has a
# desktop's GL/wayland/X11 runtime (as every desktop distro does) and a
# fontconfig with fonts — the lib list is exactly that assumption. No
# gtk/webkit packages at all: their absence is the test.
apt-get install -y -qq --no-install-recommends fontconfig fonts-dejavu-core \
	libegl1 libgl1 libgbm1 libgles2 xvfb xauth dbus \
	dbus-x11 desktop-file-utils x11-utils >/tmp/apt.log 2>&1 \
	|| { tail -15 /tmp/apt.log; fail "apt could not install the smoke tooling"; }

app=$(ls /pkg/muxalot-desktop_*_amd64.AppImage) || fail "no AppImage in /pkg"
app=$(realpath "$app")
echo "appimage: $(basename "$app") ($(du -h "$app" | cut -f1))"

export HOME=/tmp/h XDG_CONFIG_HOME=/tmp/h
mkdir -p "$HOME"
# --appimage-extract-and-run also covers fuse-less systems, so the smoke exercises it
# and the README documents it; containers cannot fuse-mount anyway.
cat > /tmp/inner.sh <<'INNER'
fail() { echo "FAIL: $*" >&2; exit 1; }
"$1" --appimage-extract >/dev/null
x=squashfs-root
[ -x "$x/AppRun" ] || fail "extract failed"
cd "$x" # exercise the same state the AppRun hook relies on: cwd = $APPDIR
./AppRun --check || fail "preflight (--check)"

# every NEEDED of every bundled ELF must resolve to inside the bundle or a host
# library (bundle rpath = $ORIGIN, then ldconfig) — catches excludelist gaps
# (linuxdeploy assumes X11/GL/fontconfig on the host) before the window test does
lc=$(ldconfig -p)
miss=""
for f in "$x"/usr/bin/* "$x"/usr/lib/*.so* "$x"/usr/lib/webkit2gtk-4.1/*Process; do
	case "$(wc -c <"$f" 2>/dev/null)" in 0|"") continue;; esac
	# shellcheck disable=SC2012
	while read -r n; do
		[ -z "$n" ] && continue
		[ -e "$x/usr/lib/$n" ] && continue
		echo "$lc" | grep -qF " $n " || miss="$miss $n"
	done < <(readelf -d "$f" | sed -n 's/.*Shared library: \[\([^]]*\)\]/\1/p')
done
[ -z "$miss" ] || { echo "FAIL: unresolved shared libraries in bundle:$miss"; cat /tmp/app.log >/dev/null; exit 1; }

./AppRun > /tmp/app.log 2>&1 &
pid=$!
sleep 10
xwininfo -root -tree | grep -qiE "\(\"muxalot-desktop\" \"[^\"]*\"\)" || { cat /tmp/app.log; fail "no window with WM_CLASS instance muxalot-desktop"; }
sleep 16 # the app's load watchdog exits at 20 s, so still running at 26 s means the page loaded
kill -0 "$pid" 2>/dev/null || { cat /tmp/app.log; fail "the app exited (load watchdog or crash)"; }
kill "$pid"
INNER
xvfb-run -a dbus-run-session -- bash /tmp/inner.sh "$app"
echo "PASS — AppImage is self-contained apart from host fontconfig and the desktop GL stack"