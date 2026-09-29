package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func setup(t *testing.T) (*httptest.Server, *Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, _ := NewStore(dir)
	files := t.TempDir()
	srv, err := NewServer(st, files, 1)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st, files
}

type testDev struct {
	id  string
	key *ecdsa.PrivateKey
}

func signHeader(d *testDev, method, host, uri string) string {
	ts := fmt.Sprint(time.Now().Unix())
	nonce := base64.RawURLEncoding.EncodeToString(randomBytes(12))
	sum := sha256.Sum256(signedMessage(method, host, uri, ts, nonce))
	sig, _ := ecdsa.SignASN1(rand.Reader, d.key, sum[:])
	return fmt.Sprintf(`Sig id="%s", ts="%s", nonce="%s", sig="%s"`, d.id, ts, nonce, base64.StdEncoding.EncodeToString(sig))
}

func pairDevice(t *testing.T, ts *httptest.Server, st *Store) *testDev {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	pub := base64.StdEncoding.EncodeToString(der)
	code, _ := st.NewPairCode()
	body, _ := json.Marshal(map[string]string{"code": strings.ToLower(code), "name": "test", "pubkey": pub})
	resp, err := http.Post(ts.URL+"/pair", "application/json", bytes.NewReader(body))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("pair failed: %v %v", err, resp)
	}
	var out map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp2, _ := http.Post(ts.URL+"/pair", "application/json", bytes.NewReader(body))
	if resp2.StatusCode != 401 {
		t.Fatalf("code reuse should fail, got %d", resp2.StatusCode)
	}
	return &testDev{out["device_id"], key}
}

func req(t *testing.T, method, url string, d *testDev, body io.Reader) *http.Response {
	r, _ := http.NewRequest(method, url, body)
	if d != nil {
		r.Header.Set("Authorization", signHeader(d, method, r.URL.Host, r.URL.RequestURI()))
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestAuthAndFiles(t *testing.T) {
	ts, st, files := setup(t)
	dev := pairDevice(t, ts, st)

	if c := req(t, "GET", ts.URL+"/ls", nil, nil).StatusCode; c != 404 {
		t.Fatalf("unauth should 404, got %d", c)
	}
	if c := req(t, "PUT", ts.URL+"/files?path=a.txt", dev, strings.NewReader("hello")).StatusCode; c != 200 {
		t.Fatalf("upload got %d", c)
	}
	if c := req(t, "PUT", ts.URL+"/files?path=a.txt", dev, strings.NewReader("x")).StatusCode; c != 409 {
		t.Fatalf("duplicate upload should 409, got %d", c)
	}
	resp := req(t, "GET", ts.URL+"/files?path=a.txt", dev, nil)
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "hello" {
		t.Fatalf("download got %q", b)
	}
	// traversal + symlink escape
	if c := req(t, "GET", ts.URL+"/files?path=../../etc/passwd", dev, nil).StatusCode; c != 403 {
		t.Fatalf("traversal got %d", c)
	}
	_ = os.Symlink("/etc", files+"/link")
	if c := req(t, "GET", ts.URL+"/files?path=link/passwd", dev, nil).StatusCode; c != 403 {
		t.Fatalf("symlink escape got %d", c)
	}
	// size limit (1 MiB)
	big := bytes.Repeat([]byte("x"), 2<<20)
	if c := req(t, "PUT", ts.URL+"/files?path=big", dev, bytes.NewReader(big)).StatusCode; c != 413 {
		t.Fatalf("oversize got %d", c)
	}
}

func TestUploadPermissionsAndRace(t *testing.T) {
	ts, st, files := setup(t)
	dev := pairDevice(t, ts, st)
	put := func(q, body string) int {
		return req(t, "PUT", ts.URL+"/files?path="+q, dev, strings.NewReader(body)).StatusCode
	}
	if c := put("new", "a"); c != 200 {
		t.Fatalf("upload got %d", c)
	}
	if fi, _ := os.Stat(files + "/new"); fi.Mode().Perm() != 0o600 {
		t.Fatalf("new file mode %v, want 0600", fi.Mode().Perm())
	}
	_ = os.Chmod(files+"/new", 0o755)
	if c := put("new&overwrite=1", "b"); c != 200 {
		t.Fatalf("overwrite got %d", c)
	}
	if fi, _ := os.Stat(files + "/new"); fi.Mode().Perm() != 0o755 {
		t.Fatalf("overwrite changed mode to %v", fi.Mode().Perm())
	}
	// concurrent no-overwrite uploads to one path: exactly one wins
	var wins, conflicts atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch put("race", "x") {
			case 200:
				wins.Add(1)
			case 409:
				conflicts.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 || conflicts.Load() != 7 {
		t.Fatalf("wins=%d conflicts=%d", wins.Load(), conflicts.Load())
	}
}

func TestUploadIOErrorNot413(t *testing.T) {
	ts, st, files := setup(t)
	dev := pairDevice(t, ts, st)
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	_ = os.Mkdir(files+"/ro", 0o500)
	t.Cleanup(func() { _ = os.Chmod(files+"/ro", 0o700) })
	if c := req(t, "PUT", ts.URL+"/files?path=ro/x", dev, strings.NewReader("a")).StatusCode; c != 500 {
		t.Fatalf("unwritable dir got %d, want 500", c)
	}
}

func TestSignatureSecurity(t *testing.T) {
	ts, st, _ := setup(t)
	dev := pairDevice(t, ts, st)
	host := strings.TrimPrefix(ts.URL, "http://")
	do := func(h string) int {
		r, _ := http.NewRequest("GET", ts.URL+"/sessions", nil)
		r.Header.Set("Authorization", h)
		resp, _ := http.DefaultClient.Do(r)
		return resp.StatusCode
	}
	good := signHeader(dev, "GET", host, "/sessions")
	if c := do(good); c != 200 {
		t.Fatalf("valid signature got %d", c)
	}
	if c := do(good); c != 404 {
		t.Fatalf("replayed header should fail, got %d", c)
	}
	// signature for a different path must not authorize this one
	if c := do(signHeader(dev, "GET", host, "/ls")); c != 404 {
		t.Fatalf("wrong-path signature got %d", c)
	}
	// other device's key can't impersonate
	other := &testDev{dev.id, func() *ecdsa.PrivateKey { k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader); return k }()}
	if c := do(signHeader(other, "GET", host, "/sessions")); c != 404 {
		t.Fatalf("forged key got %d", c)
	}
	// stale timestamp
	old := strings.Replace(signHeader(dev, "GET", host, "/sessions"), fmt.Sprint(time.Now().Unix()), fmt.Sprint(time.Now().Unix()-3600), 1)
	if c := do(old); c != 404 {
		t.Fatalf("stale ts got %d", c)
	}
	// revoked device
	_, _ = st.Revoke(dev.id)
	if c := do(signHeader(dev, "GET", host, "/sessions")); c != 404 {
		t.Fatalf("revoked device got %d", c)
	}
}

func badDev() *testDev {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	return &testDev{"nope", k}
}

func TestBadSignaturesDontLockOutDevice(t *testing.T) {
	ts, st, _ := setup(t)
	dev := pairDevice(t, ts, st)
	for i := 0; i < 15; i++ {
		if c := req(t, "GET", ts.URL+"/sessions", badDev(), nil).StatusCode; c != 404 {
			t.Fatalf("bad signature got %d", c)
		}
	}
	if c := req(t, "GET", ts.URL+"/sessions", dev, nil).StatusCode; c != 200 {
		t.Fatalf("valid device locked out, got %d", c)
	}
}

func TestPairRateLimit(t *testing.T) {
	ts, _, _ := setup(t)
	var last int
	for i := 0; i < 12; i++ {
		body := `{"code":"AAAA-AAAA","name":"x","pubkey":"` + testPub() + `"}`
		resp, _ := http.Post(ts.URL+"/pair", "application/json", strings.NewReader(body))
		last = resp.StatusCode
	}
	if last != 429 {
		t.Fatalf("expected lockout 429, got %d", last)
	}
}

func testPub() string {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	return base64.StdEncoding.EncodeToString(der)
}

func TestDownloadFIFORejected(t *testing.T) {
	ts, st, files := setup(t)
	dev := pairDevice(t, ts, st)
	if err := syscall.Mkfifo(files+"/pipe", 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() { done <- req(t, "GET", ts.URL+"/files?path=pipe", dev, nil).StatusCode }()
	select {
	case c := <-done:
		if c != 400 {
			t.Fatalf("fifo download got %d", c)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("fifo download hung")
	}
}

func TestDownloadFilenameHeader(t *testing.T) {
	ts, st, files := setup(t)
	dev := pairDevice(t, ts, st)
	const name = "résumé \"q\".txt"
	_ = os.WriteFile(files+"/"+name, []byte("x"), 0o600)
	resp := req(t, "GET", ts.URL+"/files?path="+url.QueryEscape(name), dev, nil)
	_, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition"))
	if err != nil || params["filename"] != name {
		t.Fatalf("header %q -> %v %v", resp.Header.Get("Content-Disposition"), params, err)
	}
}

func TestTerminalPersistence(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	ts, st, _ := setup(t)
	dev := pairDevice(t, ts, st)
	name := "ttytest" + strings.ReplaceAll(t.Name(), "/", "")
	defer exec.Command("tmux", "kill-session", "-t", name).Run()

	dial := func() *websocket.Conn {
		uri := "/ws?session=" + name + "&cols=100&rows=30"
		h := http.Header{"Authorization": {signHeader(dev, "GET", strings.TrimPrefix(ts.URL, "http://"), uri)}}
		u := "ws" + strings.TrimPrefix(ts.URL, "http") + uri
		c, _, err := websocket.DefaultDialer.Dial(u, h)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	readUntil := func(c *websocket.Conn, want string) bool {
		var acc strings.Builder
		_ = c.SetReadDeadline(time.Now().Add(8 * time.Second))
		for {
			typ, data, err := c.ReadMessage()
			if err != nil {
				return false
			}
			if typ == websocket.BinaryMessage {
				acc.Write(data)
				if strings.Contains(acc.String(), want) {
					return true
				}
			}
		}
	}

	c1 := dial()
	time.Sleep(700 * time.Millisecond)
	_ = c1.WriteMessage(websocket.BinaryMessage, []byte("echo MARK$((20+22))\r"))
	if !readUntil(c1, "MARK42") {
		t.Fatal("no shell output")
	}
	_ = c1.WriteMessage(websocket.TextMessage, []byte(`{"t":"resize","cols":120,"rows":40}`))
	c1.Close() // simulate dropped connection

	time.Sleep(500 * time.Millisecond)
	resp := req(t, "GET", ts.URL+"/sessions", dev, nil)
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), name) {
		t.Fatalf("session should survive disconnect: %s", b)
	}

	c2 := dial() // reattach; earlier output must still be on screen
	defer c2.Close()
	if !readUntil(c2, "MARK42") {
		t.Fatal("reattach did not restore screen")
	}

	// clipboard round trip through tmux buffer
	_ = c2.WriteMessage(websocket.TextMessage, []byte(`{"t":"clip_set","text":"clipdata"}`))
	time.Sleep(300 * time.Millisecond)
	_ = c2.WriteMessage(websocket.TextMessage, []byte(`{"t":"clip_get"}`))
	_ = c2.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		typ, data, err := c2.ReadMessage()
		if err != nil {
			t.Fatal("no clip reply")
		}
		if typ == websocket.TextMessage && strings.Contains(string(data), "clipdata") {
			break
		}
	}
}

// isolatedTmux points the agent's tmux at a private server so tests never
// touch the user's sessions or options.
func isolatedTmux(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-server").Run() })
}

func TestAttachKeepsUserClipboardSetting(t *testing.T) {
	isolatedTmux(t)
	ts, st, _ := setup(t)
	dev := pairDevice(t, ts, st)
	if err := exec.Command("tmux", "new-session", "-d", "-s", "keep").Run(); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command("tmux", "set-option", "-g", "set-clipboard", "off").Run()
	uri := "/ws?session=keep"
	h := http.Header{"Authorization": {signHeader(dev, "GET", strings.TrimPrefix(ts.URL, "http://"), uri)}}
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+uri, h)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	time.Sleep(700 * time.Millisecond)
	out, _ := exec.Command("tmux", "show-options", "-gv", "set-clipboard").Output()
	if strings.TrimSpace(string(out)) != "off" {
		t.Fatalf("set-clipboard overridden: %q", out)
	}
}

func TestSessionsListOnlyManageable(t *testing.T) {
	isolatedTmux(t)
	ts, st, _ := setup(t)
	dev := pairDevice(t, ts, st)
	for _, n := range []string{"good", "has space"} {
		if err := exec.Command("tmux", "new-session", "-d", "-s", n).Run(); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := io.ReadAll(req(t, "GET", ts.URL+"/sessions", dev, nil).Body)
	if !strings.Contains(string(b), `"good"`) || strings.Contains(string(b), "has space") {
		t.Fatalf("sessions = %s", b)
	}
}

func TestLimiterPrunesExpired(t *testing.T) {
	l := newLimiter(3, 20*time.Millisecond)
	l.fail("1.1.1.1")
	time.Sleep(40 * time.Millisecond)
	l.fail("2.2.2.2")
	if _, ok := l.m["1.1.1.1"]; ok {
		t.Fatal("expired entry not pruned")
	}
}

func TestPairBadKeyIs400(t *testing.T) {
	ts, st, _ := setup(t)
	code, _ := st.NewPairCode()
	body := `{"code":"` + code + `","name":"x","pubkey":"!!!"}`
	resp, _ := http.Post(ts.URL+"/pair", "application/json", strings.NewReader(body))
	if resp.StatusCode != 400 {
		t.Fatalf("bad key got %d", resp.StatusCode)
	}
}

func TestKillAlreadyGoneIsNoContent(t *testing.T) {
	isolatedTmux(t)
	ts, st, _ := setup(t)
	dev := pairDevice(t, ts, st)
	if err := exec.Command("tmux", "new-session", "-d", "-s", "keep").Run(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // second call: session already gone
		if r := req(t, "DELETE", ts.URL+"/sessions/gone", dev, nil); r.StatusCode != 204 {
			t.Fatalf("missing session: got %d, want 204", r.StatusCode)
		}
	}
	if r := req(t, "DELETE", ts.URL+"/sessions/keep", dev, nil); r.StatusCode != 204 {
		t.Fatalf("kill existing: got %d", r.StatusCode)
	}
	if exec.Command("tmux", "has-session", "-t", "=keep").Run() == nil {
		t.Fatal("session not killed")
	}
}

func TestNewSessionStartsInHome(t *testing.T) {
	isolatedTmux(t)
	home, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("HOME", home)
	ts, st, _ := setup(t)
	dev := pairDevice(t, ts, st)
	uri := "/ws?session=homey"
	h := http.Header{"Authorization": {signHeader(dev, "GET", strings.TrimPrefix(ts.URL, "http://"), uri)}}
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+uri, h)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	time.Sleep(700 * time.Millisecond)
	out, _ := exec.Command("tmux", "list-panes", "-t", "=homey", "-F", "#{pane_current_path}").Output()
	if got := strings.TrimSpace(string(out)); got != home {
		t.Fatalf("start dir = %q, want %q", got, home)
	}
}

func TestRevokeClosesLiveTerminal(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	isolatedTmux(t)
	old := revokeInterval
	revokeInterval = 50 * time.Millisecond
	defer func() { revokeInterval = old }()
	ts, st, _ := setup(t)
	dev := pairDevice(t, ts, st)

	uri := "/ws?session=revoketest&cols=80&rows=24"
	h := http.Header{"Authorization": {signHeader(dev, "GET", strings.TrimPrefix(ts.URL, "http://"), uri)}}
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+uri, h)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if ok, err := st.Revoke(dev.id); !ok || err != nil {
		t.Fatalf("revoke: %v %v", ok, err)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		if _, _, err := c.ReadMessage(); err != nil {
			if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
				t.Fatal("connection still open after revoke")
			}
			return
		}
	}
}

func TestLookupUnknownIDSkipsStateLock(t *testing.T) {
	st, _ := NewStore(t.TempDir())
	id, err := st.AddKey("a", testPub())
	if err != nil {
		t.Fatal(err)
	}
	if dev, _ := st.Lookup(id); dev == nil {
		t.Fatal("known device not found")
	}
	unlock, err := st.lockState() // simulate another process mid-write
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	done := make(chan *Device, 1)
	go func() { d, _ := st.Lookup("deadbeef"); done <- d }()
	select {
	case d := <-done:
		if d != nil {
			t.Fatal("unknown id found")
		}
	case <-time.After(time.Second):
		t.Fatal("Lookup of an unknown id waited for the state lock")
	}
}

func TestLookupSeesRevokeAndAdd(t *testing.T) {
	st, _ := NewStore(t.TempDir())
	id, _ := st.AddKey("a", testPub())
	if d, _ := st.Lookup(id); d == nil {
		t.Fatal("not found")
	}
	if ok, _ := st.Revoke(id); !ok {
		t.Fatal("revoke failed")
	}
	if d, _ := st.Lookup(id); d != nil {
		t.Fatal("revoked device still found")
	}
	id2, _ := st.AddKey("b", testPub())
	if d, _ := st.Lookup(id2); d == nil {
		t.Fatal("new device not found")
	}
}

func TestPairBodyReadDeadline(t *testing.T) {
	old := pairBodyTimeout
	pairBodyTimeout = 200 * time.Millisecond
	defer func() { pairBodyTimeout = old }()
	ts, _, _ := setup(t)
	c, err := net.Dial("tcp", strings.TrimPrefix(ts.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprint(c, "POST /pair HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\n{") // then stall
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadAll(c); err != nil {
		t.Fatalf("server held the stalled /pair body open: %v", err)
	}
}

func TestLimiterKeysIPv6ByPrefix(t *testing.T) {
	l := newLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		l.fail(fmt.Sprintf("2001:db8:1:2::%x", i+1)) // three hosts in one /64
	}
	if !l.blocked("2001:db8:1:2:ffff::9") {
		t.Fatal("another address in the same /64 is not blocked")
	}
	if l.blocked("2001:db8:1:3::1") {
		t.Fatal("a different /64 is blocked")
	}
	l.fail("10.0.0.1")
	if l.blocked("10.0.0.2") {
		t.Fatal("IPv4 addresses must stay separate")
	}
}

func TestLimiterSweepIsThrottled(t *testing.T) {
	l := newLimiter(3, time.Hour) // sweeps at most every window/10
	l.fail("1.1.1.1")
	l.m["1.1.1.1"].start = time.Now().Add(-2 * time.Hour) // expired
	l.fail("2.2.2.2")
	if _, ok := l.m["1.1.1.1"]; !ok {
		t.Fatal("swept again straight after the last sweep")
	}
}
