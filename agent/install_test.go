package main

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestProxyConfigKeepsPathAndPassesWebSocket(t *testing.T) {
	cases := map[string][]string{
		"caddy":  {"tty.example.com {", "reverse_proxy 127.0.0.1:8787", "flush_interval -1"},
		"nginx":  {"server_name tty.example.com", "proxy_pass http://127.0.0.1:8787;", "Upgrade $http_upgrade", "proxy_buffering off", "proxy_set_header Host $http_host"},
		"apache": {"ServerName tty.example.com", "ws://127.0.0.1:8787/$1", "ProxyPreserveHost On"},
	}
	for kind, wants := range cases {
		got, err := proxyConfig(kind, "tty.example.com", "127.0.0.1:8787")
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		for _, w := range wants {
			if !strings.Contains(got, w) {
				t.Errorf("%s config missing %q:\n%s", kind, w, got)
			}
		}
	}
	// nginx: a URI after the upstream would rewrite the path and break signatures
	got, _ := proxyConfig("nginx", "h", "127.0.0.1:8787")
	if strings.Contains(got, "proxy_pass http://127.0.0.1:8787/") {
		t.Error("nginx proxy_pass must not carry a URI")
	}
}

func TestProxyConfigRejectsUnknownType(t *testing.T) {
	if _, err := proxyConfig("haproxy", "h", "u"); err == nil {
		t.Fatal("want error for unknown proxy type")
	}
}

func TestMissingDeps(t *testing.T) {
	have := map[string]bool{"systemctl": true, "useradd": true}
	look := func(name string) (string, error) {
		if have[name] {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
	got := missingDeps(look)
	if len(got) != 1 || got[0] != "tmux" {
		t.Fatalf("missingDeps = %v, want [tmux]", got)
	}
	have["tmux"] = true
	if got := missingDeps(look); len(got) != 0 {
		t.Fatalf("missingDeps = %v, want none", got)
	}
}

func TestUnitFile(t *testing.T) {
	u := unitFile("svc", "127.0.0.1:8787", "/home/svc")
	for _, w := range []string{"User=svc", "Group=svc", "ExecStart=/usr/local/bin/muxalot-agent serve --listen 127.0.0.1:8787 --files-root /home/svc"} {
		if !strings.Contains(u, w) {
			t.Errorf("unit missing %q:\n%s", w, u)
		}
	}
}

func TestAskUser(t *testing.T) {
	for in, want := range map[string]string{"\n": "def", "alice\n": "alice", "root\nbob\n": "bob", "root\n": "def", "": "def"} {
		if got := askUser("def", strings.NewReader(in), io.Discard); got != want {
			t.Errorf("askUser(%q) = %q, want %q", in, got, want)
		}
	}
}
