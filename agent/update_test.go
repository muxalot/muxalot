package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		latest, cur string
		want        bool
	}{
		{"v0.2.10", "0.2.9", true},
		{"v1.0.0", "0.9.9", true},
		{"v0.2.9", "0.2.9", false},
		{"v0.2.8", "0.2.9", false},
		{"v0.3.0", "dev", false},
		{"v0.3.0", "0.2.9-dev", false},
		{"junk", "0.2.9", false},
	} {
		if got := newer(c.latest, c.cur); got != c.want {
			t.Errorf("newer(%q,%q)=%v want %v", c.latest, c.cur, got, c.want)
		}
	}
}

func githubStub(t *testing.T, rel release) (*httptest.Server, *int) {
	t.Helper()
	full := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"e1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		full++
		w.Header().Set("ETag", `"e1"`)
		_ = json.NewEncoder(w).Encode(rel)
	}))
	t.Cleanup(ts.Close)
	return ts, &full
}

func TestUpdaterCheckRequest(t *testing.T) {
	dir := t.TempDir()
	ts, full := githubStub(t, release{Tag: "v0.3.0", URL: "u", Body: "notes"})
	u := NewUpdater("0.2.9", dir)
	u.api = ts.URL
	for i := 0; i < 2; i++ { // second round is a 304 and must keep the cache
		if err := u.check(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if *full != 1 {
		t.Fatalf("expected one full fetch, got %d", *full)
	}
	if st := u.Status(); !st.Available || st.Latest != "v0.3.0" || st.Applying {
		t.Fatalf("status %+v", st)
	}
	if tag, err := u.Request(); err != nil || tag != "v0.3.0" {
		t.Fatalf("request: %q %v", tag, err)
	}
	if _, err := u.Request(); err != errApplying {
		t.Fatalf("second request: %v", err)
	}
	if !u.Status().Applying {
		t.Fatal("applying should be true")
	}
}

func TestUpdaterNoUpdate(t *testing.T) {
	for _, rel := range []release{{Tag: "v0.2.9"}, {Tag: "v0.3.0", Prerelease: true}, {Tag: "v0.3.0", Draft: true}} {
		ts, _ := githubStub(t, rel)
		u := NewUpdater("0.2.9", t.TempDir())
		u.api = ts.URL
		_ = u.check(context.Background())
		if _, err := u.Request(); err != errNoUpdate {
			t.Errorf("%+v: got %v", rel, err)
		}
	}
	u := NewUpdater("0.2.9", filepath.Join(t.TempDir(), "missing"))
	u.rel = release{Tag: "v0.3.0"}
	if _, err := u.Request(); err != errNotInstalled {
		t.Errorf("missing dir: got %v", err)
	}
}

func releaseStub(t *testing.T, bin []byte, sum string) *httptest.Server {
	asset := "muxalot-agent-linux-" + runtime.GOARCH
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v0.3.0/SHA256SUMS":
			fmt.Fprintf(w, "%s  %s\n", sum, asset)
		case "/v0.3.0/" + asset:
			_, _ = w.Write(bin)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func sumHex(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func applyCase(t *testing.T, tag string, bin []byte, sum string) (dst string, err error, res updateResult) {
	dir := t.TempDir()
	dst = filepath.Join(t.TempDir(), "muxalot-agent")
	if err := os.WriteFile(dst, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "request"), []byte(tag), 0o600); err != nil {
		t.Fatal(err)
	}
	ts := releaseStub(t, bin, sum)
	err = applyUpdate(ts.Client(), dir, ts.URL, "0.2.9", dst)
	b, _ := os.ReadFile(filepath.Join(dir, "result"))
	_ = json.Unmarshal(b, &res)
	if _, e := os.Stat(filepath.Join(dir, "request")); e == nil {
		t.Error("request file should be consumed")
	}
	return
}

func TestApplyUpdate(t *testing.T) {
	bin := []byte("new-binary")
	dst, err, res := applyCase(t, "v0.3.0", bin, sumHex(bin))
	if got, _ := os.ReadFile(dst); err != nil || string(got) != "new-binary" || !res.OK {
		t.Fatalf("good update: err=%v dst=%q res=%+v", err, got, res)
	}
}

func TestApplyUpdateRejects(t *testing.T) {
	bin := []byte("new-binary")
	for name, c := range map[string]struct{ tag, sum string }{
		"bad checksum": {"v0.3.0", sumHex([]byte("other"))},
		"older tag":    {"v0.2.0", sumHex(bin)},
		"bad tag":      {"../../x", sumHex(bin)},
	} {
		dst, err, res := applyCase(t, c.tag, bin, c.sum)
		if got, _ := os.ReadFile(dst); err == nil || string(got) != "old" || res.OK || res.Error == "" {
			t.Errorf("%s: err=%v dst=%q res=%+v", name, err, got, res)
		}
	}
}

func TestApplyUpdateRefusesSymlinkRequest(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "t")
	_ = os.WriteFile(target, []byte("v0.3.0"), 0o600)
	_ = os.Symlink(target, filepath.Join(dir, "request"))
	if err := applyUpdate(http.DefaultClient, dir, "http://127.0.0.1:1", "0.2.9", filepath.Join(dir, "dst")); err == nil {
		t.Fatal("symlinked request must be refused")
	}
}
