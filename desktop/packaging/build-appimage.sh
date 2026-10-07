#!/bin/bash
# Build <outdir>/muxalot-desktop_<version>_amd64.AppImage from an already built binary.
# usage: build-appimage.sh VERSION BINARY OUTDIR   (needs patchelf and curl; downloads
# linuxdeploy and linuxdeploy-plugin-gtk from their GitHub releases, which the
# attestation in the release workflow does not cover — the AppImage itself is attested)
set -euo pipefail
[ $# -eq 3 ] || { echo "usage: $0 VERSION BINARY OUTDIR" >&2; exit 2; }
ver=$1
bin=$(realpath "$2")
out=$(realpath "$3")
repo=$(cd "$(dirname "$0")/../.." && pwd)
[ -f "$repo/desktop/frontend/vendor/LICENSE-xterm.txt" ] || { echo "run make desktop-assets first (licence texts)" >&2; exit 1; }
arch=x86_64 # the AppImage ecosystem's name for amd64
# linuxdeploy-plugin-gtk publishes no releases: this is the pinned script itself.
PLUGIN_GTK_SHA=7a3fbc31a9e5075073ff8790f26effbac5f84453

umask 022
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
appdir=$tmp/AppDir

fetch() {
  curl -fsSL -o "$2" "$1" || { echo "download failed: $1" >&2; exit 1; }
}

# The AppDir mirrors the .deb layout for usr/, so the launcher file is identical.
install -Dm0755 "$bin" "$appdir/usr/bin/muxalot-desktop"
install -Dm0644 "$repo/desktop/packaging/muxalot-desktop.desktop" "$appdir/usr/share/applications/muxalot-desktop.desktop"
install -Dm0644 "$repo/assets/logo.svg" "$appdir/usr/share/icons/hicolor/scalable/apps/muxalot.svg"

cd "$tmp"
fetch https://github.com/linuxdeploy/linuxdeploy/releases/latest/download/linuxdeploy-$arch.AppImage linuxdeploy.AppImage
fetch https://raw.githubusercontent.com/linuxdeploy/linuxdeploy-plugin-gtk/$PLUGIN_GTK_SHA/linuxdeploy-plugin-gtk.sh linuxdeploy-plugin-gtk
chmod +x linuxdeploy.AppImage linuxdeploy-plugin-gtk
./linuxdeploy.AppImage --appimage-extract >/dev/null && mv squashfs-root ld   # no fuse in build containers; extract and run AppRun

# AppRun hooks are sourced by linuxdeploy's AppRun; the hook list is snapshotted
# when linuxdeploy runs, so this file must exist BEFORE the first invocation.
# $APPDIR is anchored here: the type-2 runtime presets APPDIR differently per
# launch mode (fuse mount vs --appimage-extract-and-run), and the helper spawn
# path is AppDir-relative (the lib patch below depends on the cwd).
mkdir -p "$appdir/apprun-hooks"
cat > "$appdir/apprun-hooks/webkit.sh" <<'HOOK'
export APPDIR="$(dirname "$(realpath "$0")")"
export WEBKIT_INJECTED_BUNDLE_PATH="$APPDIR/usr/lib/webkit2gtk-4.1/injected-bundle"
cd "$APPDIR"
HOOK

# linuxdeploy's baked-in excludelist skips libraries it assumes every desktop
# host carries. Mostly true: any host with a graphical session has X11, xcb,
# wayland-client and the GL/mesa stack (so don't re-add those — bundling
# jammy-era copies also breaks EGL init on newer distros, e.g. 26.04). The
# exception that bites: WebKitGTK renders TEXT, and the text stack
# (freetype, harfbuzz, fontconfig + their deps) is needed here; explicit
# --library calls bypass the excludelist and pull their closures.
libs="libfreetype.so.6 libharfbuzz.so.0 libfontconfig.so.1 libfribidi.so.0 \
libexpat.so.1 libz.so.1 libgmp.so.10 libgpg-error.so.0 libcom_err.so.2"
args=""
libexec_cache=$(ldconfig -p 2>/dev/null) # read once: awk's early exit after a pipe is SIGPIPE (141) under pipefail
for l in $libs; do
  p=$(printf '%s\n' "$libexec_cache" | awk -v l="$l" '$1==l {print $NF; exit}')
  [ -n "$p" ] || { echo "cannot resolve $l on this host" >&2; exit 1; }
  args="$args --library $p"
done

# Stage 1: the binary's library closure (ldd), then plugin-gtk adds GLib schemas,
# GIO/pixbuf modules and the GTK share tree the AppImage must carry itself.
DEPLOY_GTK_VERSION=3 PATH="$tmp:$PATH" ./ld/AppRun --appdir "$appdir" \
  --executable "$appdir/usr/bin/muxalot-desktop" \
  --desktop-file "$appdir/usr/share/applications/muxalot-desktop.desktop" \
  --icon-file "$appdir/usr/share/icons/hicolor/scalable/apps/muxalot.svg" \
  --plugin gtk $args

# WebKitGTK spawns its helper processes from a webkit2gtk-4.1 directory next to
# the loaded library, which ldd does not see. Copy them (and the injected bundle)
# from the build host and pull in their own library closures.
wk=$appdir/usr/lib/webkit2gtk-4.1
mkdir -p "$wk"
cp -r /usr/lib/x86_64-linux-gnu/webkit2gtk-4.1/. "$wk/"
for exe in "$wk"/WebKit*Process; do
  ./ld/AppRun --appdir "$appdir" --deploy-deps-only "$exe"
done

# libwebkit carries the build host's libexec dir as a baked-in absolute string and
# launches the helpers from there ("Failed to spawn child process /usr/lib/x86_64-linux-gnu/...">
# on every host that has no Debian-style webkit2gtk-4.1 package). Patch it to an
# AppDir-relative path; plugin-gtk's AppRun hook below cds into $APPDIR first.
# Replacement must fit in the original string's bytes; the remainder is NUL-padded.
lib=$appdir/usr/lib/libwebkit2gtk-4.1.so.0
old=${LIBEXEC:-/usr/lib/x86_64-linux-gnu/webkit2gtk-4.1}
new=./usr/lib/webkit2gtk-4.1
[ "$(grep -ao -F "$old" "$lib" | wc -l)" -ge 1 ] || { echo "$lib has no baked libexec path to patch" >&2; exit 1; }
offs=$(grep -abo -F "$old" "$lib" | cut -d: -f1)
printf '%s\0' "$new" > "$tmp/patch.bin"
pad=$((${#old} + 1 - $(stat -c%s "$tmp/patch.bin")))
[ $pad -ge 0 ] || { echo "patch longer than baked path" >&2; exit 1; }
head -c $pad /dev/zero >> "$tmp/patch.bin"
for o in $offs; do
  dd if="$tmp/patch.bin" of="$lib" bs=1 seek="$o" conv=notrunc status=none
done

# plugin-gtk's AppRun execs a symlink named AppRun.wrapped, which becomes the
# process's name and with it the GTK WM_CLASS / Wayland app_id; the packaged
# launcher matches on "muxalot-desktop" (see main.go), so exec the real path.
sed -i 's|AppRun.wrapped|usr/bin/muxalot-desktop|' "$appdir/AppRun"
grep -q 'usr/bin/muxalot-desktop "$@"' "$appdir/AppRun" || { echo "AppRun exec line patch failed" >&2; cat "$appdir/AppRun" >&2; exit 1; }

# Package directly with appimagetool (bundled inside linuxdeploy): the --output
# appimage path re-runs deploy passes that regenerate AppRun behind our patch.
# No fuse needed: it only writes squashfs. Runtime comes from AppImageKit's
# continuous release.
tool=$tmp/ld/plugins/linuxdeploy-plugin-appimage/usr/bin/appimagetool
[ -x "$tool" ] || { echo "appimagetool not found in linuxdeploy" >&2; exit 1; }
"$tool" "$appdir" "$out/muxalot-desktop_${ver}_amd64.AppImage"
[ -f "$out/muxalot-desktop_${ver}_amd64.AppImage" ] || { echo "appimagetool produced no AppImage" >&2; exit 1; }
ls -lh "$out/muxalot-desktop_${ver}_amd64.AppImage"