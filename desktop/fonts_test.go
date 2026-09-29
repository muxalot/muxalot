package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFontconfigOverride(t *testing.T) {
	conf := fontconfigOverride(`/etc/fonts/a&b.conf`)
	for _, want := range []string{
		`<include ignore_missing="yes">/etc/fonts/a&amp;b.conf</include>`, // the user's own config stays in force, XML-escaped
		`<glob>/*.woff</glob>`, // anchored with "/": a relative glob would silently match nothing
		`<glob>/*.woff2</glob>`,
		`<rejectfont>`,
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("override lacks %q:\n%s", want, conf)
		}
	}
}

func TestHideWebFontsUsesExistingFontconfigFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FONTCONFIG_FILE", "/home/u/my-fonts.conf")
	if err := hideWebFontsFromFontconfig(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "fontconfig.conf")
	if got := os.Getenv("FONTCONFIG_FILE"); got != path {
		t.Fatalf("FONTCONFIG_FILE = %q, want %q", got, path)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "/home/u/my-fonts.conf") {
		t.Error("the user's FONTCONFIG_FILE must be included, not replaced")
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestHideWebFontsDefaultBase(t *testing.T) {
	t.Setenv("FONTCONFIG_FILE", "")
	dir := t.TempDir()
	if err := hideWebFontsFromFontconfig(dir); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "fontconfig.conf"))
	if !strings.Contains(string(b), "/etc/fonts/fonts.conf") {
		t.Error("default system config must be included")
	}
}
