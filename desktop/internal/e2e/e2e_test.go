// Package e2e drives the real muxalot agent (built from ../../../agent) with the
// desktop client, so wire-format drift between the two fails a test.
package e2e

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"muxalot-desktop/internal/client"
	"muxalot-desktop/internal/keystore"
)

type agent struct {
	url, data, root string
	env             []string
	bin             string
}

func startAgent(t *testing.T) *agent {
	t.Helper()
	if testing.Short() {
		t.Skip("-short")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tmp := t.TempDir()
	a := &agent{
		data: filepath.Join(tmp, "data"),
		root: filepath.Join(tmp, "root"),
		bin:  filepath.Join(tmp, "muxalot-agent"),
		env:  append(os.Environ(), "TMUX_TMPDIR="+mkdir(t, filepath.Join(tmp, "tmux")), "HOME="+mkdir(t, filepath.Join(tmp, "home"))),
	}
	mkdir(t, a.root)
	build := exec.Command("go", "build", "-o", a.bin, ".")
	build.Dir = filepath.Join("..", "..", "..", "agent")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build agent: %v\n%s", err, out)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	a.url = "http://" + addr

	srv := exec.Command(a.bin, "serve", "--data", a.data, "--files-root", a.root, "--listen", addr)
	srv.Env = a.env
	var log bytes.Buffer
	srv.Stdout, srv.Stderr = &log, &log
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		srv.Process.Kill()
		srv.Wait()
		ks := exec.Command("tmux", "kill-server") // private TMUX_TMPDIR only: never touch the user's real tmux
		ks.Env = a.env
		ks.Run()
		if t.Failed() {
			t.Logf("agent log:\n%s", log.String())
		}
	})
	for i := 0; ; i++ {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			break
		}
		if i > 100 {
			t.Fatal("agent did not start")
		}
		time.Sleep(50 * time.Millisecond)
	}
	return a
}

func mkdir(t *testing.T, p string) string {
	t.Helper()
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func (a *agent) run(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command(a.bin, append(args, "--data", a.data)...)
	cmd.Env = a.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("agent %v: %v\n%s", args, err, out)
	}
	return string(out)
}

var codeRe = regexp.MustCompile(`Code:\s+(\S+)`)

// pair pairs a fresh key with the agent and returns a client plus the device id.
func (a *agent) pair(t *testing.T) (*client.Client, string) {
	t.Helper()
	m := codeRe.FindStringSubmatch(a.run(t, "pair", "--url", "https://example.invalid"))
	if m == nil {
		t.Fatal("no pairing code in agent output")
	}
	ks := keystore.New(t.TempDir(), nil)
	pub, err := ks.Create("e2e", keystore.ModePassphrase, "pw")
	if err != nil {
		t.Fatal(err)
	}
	dev, err := client.Pair(a.url, m[1], "e2e", pub)
	if err != nil {
		t.Fatalf("pair: %v", err)
	}
	key, err := ks.Load("e2e", keystore.ModePassphrase, "pw")
	if err != nil {
		t.Fatal(err)
	}
	c, err := client.New(a.url, dev, key)
	if err != nil {
		t.Fatal(err)
	}
	return c, dev
}

func waitFor(t *testing.T, what string, d time.Duration, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

type sink struct {
	mu    sync.Mutex
	data  bytes.Buffer
	state client.State
}

func (s *sink) onData(b []byte) { s.mu.Lock(); s.data.Write(b); s.mu.Unlock() }
func (s *sink) onState(st client.State) {
	s.mu.Lock()
	s.state = st
	s.mu.Unlock()
}
func (s *sink) has(sub string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Contains(s.data.String(), sub)
}
func (s *sink) is(st client.State) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state == st
}

func TestTerminalSessionsAndFiles(t *testing.T) {
	a := startAgent(t)
	c, _ := a.pair(t)

	// unpaired requests are indistinguishable from 404
	other, _ := client.New(a.url, "nosuchdev", mustKey(t))
	if _, err := other.Sessions(); !isCode(err, http.StatusNotFound) {
		t.Fatalf("unknown device err = %v, want 404", err)
	}

	if got, err := c.Sessions(); err != nil || len(got) != 0 {
		t.Fatalf("Sessions = %v, %v; want none", got, err)
	}

	s := &sink{}
	cn, err := c.Dial("e2e", 100, 30, s.onData, s.onState)
	if err != nil {
		t.Fatal(err)
	}
	defer cn.Close()
	waitFor(t, "connected", 10*time.Second, func() bool { return s.is(client.Connected) })
	// the echoed command line holds "$((40+2))", so only the shell's output holds "muxalot-42"
	waitFor(t, "shell prompt", 10*time.Second, func() bool { return s.data.Len() > 0 })
	if err := cn.Send([]byte("echo muxalot-$((40+2))\r")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "command output", 10*time.Second, func() bool { return s.has("muxalot-42") })
	cn.Resize(120, 40)
	if err := cn.Send([]byte("stty size\r")); err != nil {
		t.Fatal(err)
	}
	// tmux's status line takes one row, so a 40-row client shows a 39-row pane
	waitFor(t, "resized pty", 10*time.Second, func() bool { return s.has("39 120") })

	waitFor(t, "session listed", 5*time.Second, func() bool {
		l, _ := c.Sessions()
		return len(l) == 1 && l[0].Name == "e2e"
	})

	// files: upload, conflict, overwrite, list, download
	up := func(ow bool) error {
		return c.Upload(context.Background(), "hello.txt", 5, ow, strings.NewReader("hello"), nil)
	}
	if err := up(false); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if err := up(false); !isCode(err, http.StatusConflict) {
		t.Fatalf("second upload err = %v, want 409", err)
	}
	if err := up(true); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	ls, err := c.Ls("")
	if err != nil || len(ls.Entries) != 1 || ls.Entries[0].Name != "hello.txt" || ls.Entries[0].Size != 5 {
		t.Fatalf("Ls = %+v, %v", ls, err)
	}
	var buf bytes.Buffer
	if err := c.Download(context.Background(), "hello.txt", &buf, nil); err != nil || buf.String() != "hello" {
		t.Fatalf("Download = %q, %v", buf.String(), err)
	}
	if _, err := c.Ls("../.."); err == nil {
		t.Fatal("Ls outside the files root must fail")
	}

	// kill; the attached connection sees the session end
	if err := c.Kill("e2e"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "exit after kill", 10*time.Second, func() bool { return s.is(client.Exited) })
	if err := c.Kill("e2e"); err != nil {
		t.Fatalf("killing a gone session must succeed (agent answers 204): %v", err)
	}
}

func TestRevokeClosesLiveTerminal(t *testing.T) {
	a := startAgent(t)
	c, dev := a.pair(t)
	s := &sink{}
	cn, err := c.Dial("rev", 80, 24, s.onData, s.onState)
	if err != nil {
		t.Fatal(err)
	}
	defer cn.Close()
	waitFor(t, "connected", 10*time.Second, func() bool { return s.is(client.Connected) })

	a.run(t, "revoke", dev)
	// agent re-checks every 5 s; after the drop, reconnects are refused
	waitFor(t, "disconnect after revoke", 15*time.Second, func() bool { return !s.is(client.Connected) })
	if _, err := c.Sessions(); !isCode(err, http.StatusNotFound) {
		t.Fatalf("revoked device err = %v, want 404", err)
	}
}

func isCode(err error, code int) bool {
	var ae *client.APIError
	return errors.As(err, &ae) && ae.Code == code
}

func mustKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	ks := keystore.New(t.TempDir(), nil)
	if _, err := ks.Create("x", keystore.ModePassphrase, "pw"); err != nil {
		t.Fatal(err)
	}
	k, err := ks.Load("x", keystore.ModePassphrase, "pw")
	if err != nil {
		t.Fatal(err)
	}
	return k
}
