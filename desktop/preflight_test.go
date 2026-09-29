package main

import (
	"strings"
	"testing"
)

func fake(env map[string]string, files ...string) environ {
	have := map[string]bool{}
	for _, f := range files {
		have[f] = true
	}
	return environ{get: func(k string) string { return env[k] }, exists: func(p string) bool { return have[p] }, uid: 1000}
}

func TestPreflight(t *testing.T) {
	cases := []struct {
		name string
		e    environ
		want []string // substrings, one per expected problem; nil = ready
	}{
		{"wayland session", fake(map[string]string{"WAYLAND_DISPLAY": "wayland-0", "XDG_RUNTIME_DIR": "/run/user/1000", "DBUS_SESSION_BUS_ADDRESS": "unix:path=/run/user/1000/bus"},
			"/run/user/1000/wayland-0", "/run/user/1000/bus"), nil},
		{"x11 session, default bus path", fake(map[string]string{"DISPLAY": ":0"}, "/tmp/.X11-unix/X0", "/run/user/1000/bus"), nil},
		{"x11 with screen suffix", fake(map[string]string{"DISPLAY": ":1.0"}, "/tmp/.X11-unix/X1", "/run/user/1000/bus"), nil},
		{"ssh x forwarding", fake(map[string]string{"DISPLAY": "localhost:10.0", "DBUS_SESSION_BUS_ADDRESS": "unix:abstract=/tmp/dbus-x"}), nil},
		{"tty: no display", fake(map[string]string{"DBUS_SESSION_BUS_ADDRESS": "unix:path=/run/user/1000/bus"}, "/run/user/1000/bus"), []string{"no graphical session"}},
		{"stale display", fake(map[string]string{"DISPLAY": ":5"}, "/run/user/1000/bus"), []string{"cannot reach the display"}},
		{"wayland socket missing, x fine", fake(map[string]string{"WAYLAND_DISPLAY": "wayland-0", "XDG_RUNTIME_DIR": "/run/user/1000", "DISPLAY": ":0"}, "/tmp/.X11-unix/X0", "/run/user/1000/bus"), nil},
		{"no bus", fake(map[string]string{"DISPLAY": ":0"}, "/tmp/.X11-unix/X0"), []string{"no D-Bus session bus"}},
		{"bus address points nowhere", fake(map[string]string{"DISPLAY": ":0", "DBUS_SESSION_BUS_ADDRESS": "unix:path=/nope"}, "/tmp/.X11-unix/X0"), []string{"no D-Bus session bus"}},
		{"nothing at all", fake(nil), []string{"no graphical session", "no D-Bus session bus"}},
	}
	for _, c := range cases {
		got := preflight(c.e)
		if len(got) != len(c.want) {
			t.Errorf("%s: problems = %q, want %d", c.name, got, len(c.want))
			continue
		}
		for i, w := range c.want {
			if !strings.Contains(got[i], w) {
				t.Errorf("%s: problem %d = %q, want it to mention %q", c.name, i, got[i], w)
			}
		}
	}
}
