// muxalot desktop client: a Wails webview that renders xterm.js. All keys,
// signing and network I/O live in Go (service.go); the page only gets the
// narrow, test-pinned Service surface.
package main

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"runtime"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"muxalot-desktop/internal/config"
	"muxalot-desktop/internal/keystore"
)

var version = "dev"

// frontend/vendor is build output (make desktop-assets); index.html etc. are committed.
//
//go:embed all:frontend
var assets embed.FS

// Scripts and everything else come from our own origin only. Styles need
// 'unsafe-inline' because xterm.js sets inline styles; scripts stay strict.
const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'self'; " +
	"img-src 'self' data:; connect-src 'self'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'"

func withCSP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func main() {
	problems := preflight(osEnviron())
	if len(os.Args) > 1 && os.Args[1] == "--check" { // check the requirements without opening a window
		for _, p := range problems {
			fmt.Println("missing:", p)
		}
		if len(problems) > 0 {
			os.Exit(1)
		}
		fmt.Println("ok: graphical session and D-Bus session bus found")
		return
	}
	if len(problems) > 0 {
		fatal("cannot start, missing requirements", problems)
	}

	dir, err := config.Dir()
	if err != nil {
		log.Fatal(err)
	}
	if runtime.GOOS == "linux" {
		if err := hideWebFontsFromFontconfig(dir); err != nil {
			log.Printf("warning: could not set up the font override: %v", err)
		}
	}
	svc := newService(config.New(dir), keystore.New(dir, keystore.System{}))

	ui, err := fs.Sub(assets, "frontend")
	if err != nil {
		log.Fatal(err)
	}
	if _, err := fs.Stat(ui, "vendor/xterm.js"); err != nil {
		log.Fatal("frontend/vendor is missing: build with `make desktop`")
	}

	// Leave Linux.ApplicationID and Linux.ProgramName unset: GTK takes the window's Wayland app_id
	// and X11 WM_CLASS from the executable name, and the packaged launcher
	// (packaging/muxalot-desktop.desktop) matches on "muxalot-desktop". Setting either breaks that.
	app := application.New(application.Options{
		Name:     "muxalot",
		Services: []application.Service{application.NewService(svc)},
		Assets:   application.AssetOptions{Handler: application.AssetFileServerFS(ui), Middleware: withCSP},
	})
	svc.app = app
	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:                       "main",
		Title:                      "muxalot " + version,
		Width:                      1100,
		Height:                     720,
		URL:                        "/",
		DevToolsEnabled:            false,
		DefaultContextMenuDisabled: true,
	})
	ready := make(chan struct{})
	var once sync.Once
	win.OnWindowEvent(events.Common.WindowRuntimeReady, func(*application.WindowEvent) { once.Do(func() { close(ready) }) })
	go watchLoad(ready)
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
