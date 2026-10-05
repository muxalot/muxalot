//go:build windows

package main

import (
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

// The registry lookup copies wails' own webviewloader: the evergreen WebView2
// runtime registers itself under EdgeUpdate\ClientState with an EBWebView value
// giving the folder of the loaded webview. HKLM covers machine installs, HKCU
// covers per-user ones; reads go through WOW64_32KEY.
func webview2Installed() bool {
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		k, err := registry.OpenKey(root,
			`Software\Microsoft\EdgeUpdate\ClientState\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`,
			registry.READ|registry.WOW64_32KEY)
		if err != nil {
			continue
		}
		_, _, err = k.GetStringValue("EBWebView")
		k.Close()
		if err == nil {
			return true
		}
	}
	return false
}

func problemsFor(e environ) []string {
	// ponytail: no version floor yet (the loader demands >= 86.0.616.0 itself and fails loudly);
	if !webview2Installed() {
		return []string{"the Microsoft WebView2 runtime is missing: install it from " + webview2Download + " and start muxalot again"}
	}
	return nil
}

// showFatal shows the startup failure in a message box: an exe started from
// the launcher or Explorer has no terminal, and stderr goes nowhere.
func showFatal(title, text string) {
	user32 := syscall.NewLazyDLL("user32.dll")
	box := user32.NewProc("MessageBoxW")
	pTitle, _ := syscall.UTF16PtrFromString(title)
	pText, _ := syscall.UTF16PtrFromString(text)
	// MB_ICONERROR; the parent hwnd stays 0 — nothing to borrow focus from.
	box.Call(0,
		uintptr(unsafe.Pointer(pText)),
		uintptr(unsafe.Pointer(pTitle)),
		0x10)
	os.Exit(1)
}
