// Requirements the app can check for itself before opening a window. A missing
// shared library (libwebkit2gtk-4.1, GTK3, the WebView2 runtime) can't always be
// reported from here: the dynamic loader aborts the process before main() runs
// and prints its own error. The per-OS checks live in preflight_linux.go and
// preflight_windows.go.
package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"
)

// Shown on Windows wherever a diagnosis needs the download; the same link the
// Microsoft Edge installer keeps current.
const webview2Download = "https://developer.microsoft.com/en-us/microsoft-edge/webview2/"

type environ struct {
	get    func(string) string
	exists func(string) bool
	uid    int
}

func osEnviron() environ {
	return environ{
		get:    os.Getenv,
		exists: func(p string) bool { _, err := os.Stat(p); return err == nil },
		uid:    os.Getuid(),
	}
}

// preflight returns one line per missing requirement; empty means ready.
// problemsFor is the per-OS check, defined in preflight_linux.go / _windows.go.
func preflight(e environ) []string {
	return problemsFor(e)
}

// fatal reports a startup failure where the user will see it and exits. The
// message also goes to stderr for whoever can see it; showFatal (defined per
// OS) puts it in a window that a launcher-started app would otherwise lack.
func fatal(title string, lines []string) {
	msg := title + ":\n  - " + strings.Join(lines, "\n  - ") + "\n"
	fmt.Fprint(os.Stderr, "muxalot-desktop: "+msg)
	showFatal(title, msg)
}

const loadTimeout = 20 * time.Second

// watchLoad exits with a diagnosis if the window's page never comes up: the
// silent failure a broken GPU/compositor, WebKitGTK or a missing WebView2 produces.
func watchLoad(ready <-chan struct{}) {
	select {
	case <-ready:
	case <-time.After(loadTimeout):
		if runtime.GOOS == "windows" {
			fatal("the window did not finish loading", []string{
				"the webview is stuck: reinstall the Microsoft WebView2 runtime from " + webview2Download,
			})
			return
		}
		fatal("the window did not finish loading", []string{
			"the web view is stuck (check `top`: a WebKitWebProcess at 100% CPU means a hang). Known causes: system .woff fonts (this app already hides them, so look for other odd fonts: `fc-list | grep -iE 'woff|^[^:]*: *$'`), and on some GPUs the renderer; for the latter try WEBKIT_DISABLE_DMABUF_RENDERER=1 WEBKIT_DISABLE_COMPOSITING_MODE=1",
			"otherwise check that libwebkit2gtk-4.1 is current, and run from a terminal to see WebKit's own messages",
		})
	}
}
