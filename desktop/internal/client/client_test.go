package client

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func testKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// same field syntax the agent parses (agent/server.go sigFieldRe)
var sigFieldRe = regexp.MustCompile(`(\w+)="([^"]*)"`)

// verify mirrors agent verify(): fields, limits, signature over the signed string.
func verify(t *testing.T, r *http.Request, pub *ecdsa.PublicKey) bool {
	t.Helper()
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Sig ") {
		return false
	}
	f := map[string]string{}
	for _, m := range sigFieldRe.FindAllStringSubmatch(h[4:], -1) {
		f[m[1]] = m[2]
	}
	if f["id"] == "" || len(f["nonce"]) < 8 || len(f["nonce"]) > 64 {
		return false
	}
	secs, err := strconv.ParseInt(f["ts"], 10, 64)
	if err != nil || time.Since(time.Unix(secs, 0)) > 60*time.Second {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(f["sig"])
	if err != nil {
		return false
	}
	sum := sha256.Sum256([]byte("muxalot-v1\n" + r.Method + "\n" + r.Host + "\n" + r.URL.RequestURI() + "\n" + f["ts"] + "\n" + f["nonce"]))
	return ecdsa.VerifyASN1(pub, sum[:], sig)
}

func TestAuthorizationVerifies(t *testing.T) {
	k := testKey(t)
	h, err := Authorization(k, "dev1", "GET", "example.com:8443", "/ls?path=%2Fa+b")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "https://example.com:8443/ls?path=%2Fa+b", nil)
	r.Header.Set("Authorization", h)
	if !verify(t, r, &k.PublicKey) {
		t.Fatalf("agent-style verify rejected %q", h)
	}
	r.Header.Set("Authorization", strings.Replace(h, `dev1`, `dev1`, 1))
	r.URL.RawQuery = "path=other"
	if verify(t, r, &k.PublicKey) {
		t.Fatal("signature must bind the request URI")
	}
}

func TestCanonicalURL(t *testing.T) {
	ok := map[string]string{
		"https://tty.example.com/":        "https://tty.example.com",
		"https://tty.example.com:443":     "https://tty.example.com",
		"https://tty.example.com:8443/x/": "https://tty.example.com:8443/x",
		"http://127.0.0.1:9000":           "http://127.0.0.1:9000",
		"http://localhost:9000":           "http://localhost:9000",
		"  https://tty.example.com  ":     "https://tty.example.com",
	}
	for in, want := range ok {
		got, err := CanonicalURL(in)
		if err != nil || got != want {
			t.Errorf("CanonicalURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"http://tty.example.com", "http://192.168.1.5", "ftp://x", "https://", "https://u:p@x.com", "https://x.com?a=1", "https://x.com#f", "x.com", ""} {
		if got, err := CanonicalURL(in); err == nil {
			t.Errorf("CanonicalURL(%q) = %q, want error", in, got)
		}
	}
}

func newTestClient(t *testing.T, h http.Handler) (*Client, *ecdsa.PrivateKey) {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	k := testKey(t)
	c, err := New(ts.URL, "dev1", k)
	if err != nil {
		t.Fatal(err)
	}
	return c, k
}

func TestSessionsSignedAndDecoded(t *testing.T) {
	var k *ecdsa.PrivateKey
	c, key := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !verify(t, r, &k.PublicKey) {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`[{"name":"main","windows":2,"attached":1,"created":5}]`))
	}))
	k = key
	got, err := c.Sessions()
	if err != nil || len(got) != 1 || got[0].Name != "main" || got[0].Windows != 2 {
		t.Fatalf("Sessions = %+v, %v", got, err)
	}
}

func TestErrorsAndLimits(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sessions":
			http.Redirect(w, r, "http://evil.example/sessions", http.StatusFound)
		case "/ls":
			w.Write([]byte(strings.Repeat("x", maxBody+10)))
		default:
			http.Error(w, "nope", http.StatusUnauthorized)
		}
	}))
	if _, err := c.Sessions(); err == nil {
		t.Error("redirect must not be followed (or succeed)")
	} else if ae, ok := err.(*APIError); !ok || ae.Code != http.StatusFound {
		t.Errorf("redirect error = %v", err)
	}
	if _, err := c.Ls("/"); err == nil {
		t.Error("oversize body must fail")
	}
	if err := c.Kill("../x"); err == nil {
		t.Error("Kill must reject bad session names")
	}
	err := c.Kill("main")
	if ae, ok := err.(*APIError); !ok || ae.Code != 401 || ae.Msg != "nope" {
		t.Errorf("Kill error = %v", err)
	}
}

func TestUploadDownloadRoundTrip(t *testing.T) {
	var stored []byte
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "PUT":
			if r.ContentLength != 5 {
				t.Errorf("upload ContentLength = %d, want 5", r.ContentLength)
			}
			if stored != nil && r.URL.Query().Get("overwrite") != "1" {
				http.Error(w, "exists", http.StatusConflict)
				return
			}
			stored = make([]byte, 5)
			r.Body.Read(stored)
		case "GET":
			w.Write(stored)
		}
	}))
	up := func(ow bool) error {
		return c.Upload(context.Background(), "/a", 5, ow, strings.NewReader("hello"), nil)
	}
	if err := up(false); err != nil {
		t.Fatal(err)
	}
	if ae, ok := up(false).(*APIError); !ok || ae.Code != http.StatusConflict {
		t.Fatalf("second upload = %v, want 409", ae)
	}
	if err := up(true); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	if err := c.Download(context.Background(), "/a", &sb, nil); err != nil || sb.String() != "hello" {
		t.Fatalf("Download = %q, %v", sb.String(), err)
	}
}

func TestSafeName(t *testing.T) {
	ok := map[string]string{"a.txt": "a.txt", "/etc/passwd": "passwd", `C:\x\y.txt`: "y.txt", "../../up.txt": "up.txt", "ünï.txt": "ünï.txt"}
	for in, want := range ok {
		if got, err := SafeName(in); err != nil || got != want {
			t.Errorf("SafeName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", ".", "..", "a/", "x\x00y", "CON", "nul.txt", "com1.log", "a.", "a ", strings.Repeat("a", 300)} {
		if got, err := SafeName(in); err == nil {
			t.Errorf("SafeName(%q) = %q, want error", in, got)
		}
	}
}

func TestCreateExclusive(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	f, err := CreateExclusive(p)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	if _, err := CreateExclusive(p); err == nil {
		t.Error("existing file must be refused")
	}
	link := filepath.Join(dir, "l")
	os.Symlink(filepath.Join(dir, "missing"), link)
	if _, err := CreateExclusive(link); err == nil {
		t.Error("symlink must be refused")
	}
	if _, err := os.Stat(filepath.Join(dir, "missing")); err == nil {
		t.Error("symlink target must not be created")
	}
}

// ---- Conn ----

var up = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

func TestConnReconnectsAndStops(t *testing.T) {
	backoffUnit = time.Millisecond
	t.Cleanup(func() { backoffUnit = 500 * time.Millisecond })

	var conns atomic.Int32
	var k *ecdsa.PrivateKey
	got := make(chan string, 8)
	c, key := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !verify(t, r, &k.PublicKey) {
			http.NotFound(w, r)
			return
		}
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		n := conns.Add(1)
		if r.URL.Query().Get("session") != "main" || r.URL.Query().Get("cols") != "100" || r.URL.Query().Get("rows") != "30" {
			t.Errorf("query = %v", r.URL.RawQuery)
		}
		if n == 1 {
			return // drop the first connection: client must come back
		}
		ws.WriteMessage(websocket.BinaryMessage, []byte("hello"))
		for {
			typ, msg, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if typ == websocket.TextMessage && strings.Contains(string(msg), "exit-now") {
				ws.WriteMessage(websocket.TextMessage, []byte(`{"t":"exit"}`))
			}
			got <- string(msg)
		}
	}))
	k = key

	states := make(chan State, 32)
	data := make(chan string, 8)
	cn, err := c.Dial("main", 100, 30, func(b []byte) { data <- string(b) }, func(s State) { states <- s })
	if err != nil {
		t.Fatal(err)
	}
	defer cn.Close()
	if s := <-data; s != "hello" {
		t.Fatalf("data = %q", s)
	}
	if conns.Load() < 2 {
		t.Fatalf("expected a reconnect, conns = %d", conns.Load())
	}
	if err := cn.Send([]byte("ls\r")); err != nil {
		t.Fatal(err)
	}
	if m := <-got; m != "ls\r" {
		t.Fatalf("server got %q", m)
	}
	cn.Resize(120, 40)
	if m := <-got; !strings.Contains(m, `"cols":120`) {
		t.Fatalf("resize frame = %q", m)
	}
	// server ends the session: no more reconnects
	cn.write(websocket.TextMessage, []byte(`{"t":"exit-now"}`))
	deadline := time.After(3 * time.Second)
	for {
		select {
		case s := <-states:
			if s == Exited {
				n := conns.Load()
				time.Sleep(50 * time.Millisecond)
				if conns.Load() != n {
					t.Fatal("reconnected after exit")
				}
				return
			}
		case <-deadline:
			t.Fatal("no Exited state")
		}
	}
}

func TestConnOversizeFrameAndClose(t *testing.T) {
	backoffUnit = time.Millisecond
	t.Cleanup(func() { backoffUnit = 500 * time.Millisecond })

	var conns atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		conns.Add(1)
		ws.WriteMessage(websocket.BinaryMessage, make([]byte, readLimit+1))
		time.Sleep(200 * time.Millisecond)
	}))
	var delivered atomic.Int32
	cn, _ := c.Dial("main", 0, 0, func([]byte) { delivered.Add(1) }, func(State) {})
	time.Sleep(100 * time.Millisecond)
	if delivered.Load() != 0 {
		t.Error("oversize frame must not be delivered")
	}
	if conns.Load() < 2 {
		t.Errorf("oversize frame should drop the socket and reconnect, conns = %d", conns.Load())
	}
	cn.Close()
	time.Sleep(50 * time.Millisecond)
	n := conns.Load()
	time.Sleep(100 * time.Millisecond)
	if conns.Load() != n {
		t.Error("Close must stop reconnecting")
	}
	if _, err := c.Dial("../x", 1, 1, nil, nil); err == nil {
		t.Error("Dial must reject bad session names")
	}
}
