package main

import (
	"bytes"
	"encoding/xml"
	"os"
	"path/filepath"
)

// System .woff/.woff2 fonts (Debian's fonts-opendyslexic installs them under
// /usr/share/fonts/woff) get matched by fontconfig for every family, and
// WebKitGTK's page thread then spins at 100% CPU: the window never loads.
// Native apps never use web-font files as system fonts, so hide them from this
// process only, keeping the user's own fontconfig setup in force.
func hideWebFontsFromFontconfig(dir string) error {
	base := os.Getenv("FONTCONFIG_FILE")
	if base == "" {
		base = "/etc/fonts/fonts.conf"
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "fontconfig.conf")
	if err := os.WriteFile(path, []byte(fontconfigOverride(base)), 0o600); err != nil {
		return err
	}
	return os.Setenv("FONTCONFIG_FILE", path)
}

// The globs must start with "/": fontconfig resolves a relative glob against the
// config file's own directory, so a bare "*.woff" matches nothing.
func fontconfigOverride(base string) string {
	var esc bytes.Buffer
	_ = xml.EscapeText(&esc, []byte(base))
	return `<?xml version="1.0"?>
<!DOCTYPE fontconfig SYSTEM "fonts.dtd">
<fontconfig>
  <include ignore_missing="yes">` + esc.String() + `</include>
  <selectfont><rejectfont><glob>/*.woff</glob><glob>/*.woff2</glob></rejectfont></selectfont>
</fontconfig>
`
}
