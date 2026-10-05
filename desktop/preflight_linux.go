//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// showFatal is the Linux half of fatal: a terminal user already has the
// message; otherwise route it to a desktop dialog if there is a display.
func showFatal(title, text string) {
	if fi, err := os.Stderr.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		os.Exit(1) // a terminal user already has the message
	}
	if os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "" {
		for _, cmd := range [][]string{
			{"zenity", "--error", "--no-markup", "--title", "muxalot", "--text", text},
			{"kdialog", "--error", text},
			{"notify-send", "muxalot", text},
		} {
			if p, err := exec.LookPath(cmd[0]); err == nil {
				c := exec.Command(p, cmd[1:]...)
				done := make(chan struct{})
				go func() { c.Run(); close(done) }()
				select {
				case <-done:
				case <-time.After(60 * time.Second):
					c.Process.Kill()
				}
				break
			}
		}
	}
	os.Exit(1)
}

// Linux requirements: a graphical session and a D-Bus session bus, both needed
// by GTK/WebKitGTK before a window can appear.

func problemsFor(e environ) []string {
	var problems []string

	// graphical session: a Wayland socket or an X display that is really there
	wl, x := e.get("WAYLAND_DISPLAY"), e.get("DISPLAY")
	wlSock := wl
	if wl != "" && !filepath.IsAbs(wl) {
		wlSock = filepath.Join(e.get("XDG_RUNTIME_DIR"), wl)
	}
	waylandOK := wl != "" && e.exists(wlSock)
	xOK := x != "" && (!strings.HasPrefix(x, ":") || e.exists("/tmp/.X11-unix/X"+strings.SplitN(strings.TrimPrefix(x, ":"), ".", 2)[0]))
	if !waylandOK && !xOK {
		switch {
		case wl == "" && x == "":
			problems = append(problems, "no graphical session: neither DISPLAY nor WAYLAND_DISPLAY is set (SSH or a text console?). Start it from your desktop session")
		default:
			var tried []string
			if wl != "" {
				tried = append(tried, fmt.Sprintf("WAYLAND_DISPLAY=%s (no socket at %s)", wl, wlSock))
			}
			if x != "" {
				tried = append(tried, fmt.Sprintf("DISPLAY=%s (not running)", x))
			}
			problems = append(problems, "cannot reach the display: "+strings.Join(tried, ", ")+". Start it from your desktop session")
		}
	}

	// D-Bus session bus: GTK and the file dialogs need it; without it the window never appears
	if !sessionBus(e) {
		problems = append(problems, "no D-Bus session bus: DBUS_SESSION_BUS_ADDRESS is unset and /run/user/"+fmt.Sprint(e.uid)+"/bus does not exist. Start it from your desktop session, or with `dbus-run-session -- muxalot-desktop`")
	}
	return problems
}

func sessionBus(e environ) bool {
	addr := e.get("DBUS_SESSION_BUS_ADDRESS")
	if addr == "" {
		return e.exists(fmt.Sprintf("/run/user/%d/bus", e.uid))
	}
	for _, a := range strings.Split(addr, ";") {
		switch {
		case strings.HasPrefix(a, "unix:path="):
			if e.exists(strings.SplitN(strings.TrimPrefix(a, "unix:path="), ",", 2)[0]) {
				return true
			}
		default:
			return true // abstract sockets, tcp, launchd: can't check cheaply, let it try
		}
	}
	return false
}
