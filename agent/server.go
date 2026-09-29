package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

var sessionRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

type ctxKey struct{}

// revokeInterval is how often an open terminal re-checks that its device is still paired.
var revokeInterval = 5 * time.Second

// pairBodyTimeout bounds how long an unauthenticated client may take to send the /pair body.
var pairBodyTimeout = 10 * time.Second

type Server struct {
	st        *Store
	filesRoot string // symlink-resolved absolute path
	maxUpload int64
	lim       *limiter
	nonces    *nonceCache
}

func NewServer(st *Store, filesRoot string, maxUploadMB int64) (*Server, error) {
	root, err := filepath.EvalSymlinks(filesRoot)
	if err != nil {
		return nil, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	return &Server{st: st, filesRoot: root, maxUpload: maxUploadMB << 20, lim: newLimiter(10, 15*time.Minute), nonces: &nonceCache{seen: map[string]time.Time{}}}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /pair", s.handlePair)
	mux.HandleFunc("GET /ws", s.auth(s.handleWS))
	mux.HandleFunc("GET /sessions", s.auth(s.handleSessions))
	mux.HandleFunc("DELETE /sessions/{name}", s.auth(s.handleKill))
	mux.HandleFunc("GET /ls", s.auth(s.handleLs))
	mux.HandleFunc("GET /files", s.auth(s.handleDownload))
	mux.HandleFunc("PUT /files", s.auth(s.handleUpload))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	return mux
}

// ---------- client IP / rate limiting ----------

// clientIP trusts X-Forwarded-For only when the peer is loopback (Caddy on
// the same host). The rightmost entry is the one appended by our proxy.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	return host
}

type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	m      map[string]*failRec
	swept  time.Time
}
type failRec struct {
	n     int
	start time.Time
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, m: map[string]*failRec{}}
}

// limitKey buckets IPv6 clients by /64, since one subscriber usually controls the whole prefix.
func limitKey(ip string) string {
	if a := net.ParseIP(ip); a != nil && a.To4() == nil {
		return a.Mask(net.CIDRMask(64, 128)).String()
	}
	return ip
}

func (l *limiter) blocked(ip string) bool {
	ip = limitKey(ip)
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.m[ip]
	if r == nil {
		return false
	}
	if time.Since(r.start) > l.window {
		delete(l.m, ip)
		return false
	}
	return r.n >= l.max
}

func (l *limiter) fail(ip string) {
	ip = limitKey(ip)
	l.mu.Lock()
	defer l.mu.Unlock()
	// Sweep at most every window/10, so many distinct sources can't make each failure O(n).
	if time.Since(l.swept) > l.window/10 {
		l.swept = time.Now()
		for k, v := range l.m {
			if time.Since(v.start) > l.window {
				delete(l.m, k)
			}
		}
	}
	r := l.m[ip]
	if r == nil {
		r = &failRec{start: time.Now()}
		l.m[ip] = r
	}
	r.n++
}

// ---------- auth ----------
//
// Requests are authenticated by an ECDSA P-256 signature made with the
// device's private key (which never leaves the phone's Keystore):
//
//   Authorization: Sig id="<device>", ts="<unix secs>", nonce="<b64url>", sig="<b64 std, ASN.1 DER>"
//
// signed message (SHA-256 then ECDSA):
//   "muxalot-v1\n" + METHOD + "\n" + HOST + "\n" + REQUEST_URI + "\n" + ts + "\n" + nonce
//
// Timestamps must be within maxSkew of server time and each nonce is
// accepted once, so a captured header cannot be replayed.

const maxSkew = 60 * time.Second

var sigFieldRe = regexp.MustCompile(`(\w+)="([^"]*)"`)

type nonceCache struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// use records the nonce and reports whether it was fresh.
func (c *nonceCache) use(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, exp := range c.seen {
		if now.After(exp) {
			delete(c.seen, k)
		}
	}
	if _, dup := c.seen[key]; dup {
		return false
	}
	c.seen[key] = now.Add(2*maxSkew + time.Second)
	return true
}

func signedMessage(method, host, uri, ts, nonce string) []byte {
	return []byte("muxalot-v1\n" + method + "\n" + host + "\n" + uri + "\n" + ts + "\n" + nonce)
}

func (s *Server) verify(r *http.Request) *Device {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Sig ") {
		return nil
	}
	f := map[string]string{}
	for _, m := range sigFieldRe.FindAllStringSubmatch(h[4:], -1) {
		f[m[1]] = m[2]
	}
	id, ts, nonce, sigB64 := f["id"], f["ts"], f["nonce"], f["sig"]
	if id == "" || ts == "" || len(nonce) < 8 || len(nonce) > 64 || sigB64 == "" {
		return nil
	}
	secs, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return nil
	}
	if d := time.Since(time.Unix(secs, 0)); d > maxSkew || d < -maxSkew {
		return nil
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return nil
	}
	dev, pk := s.st.Lookup(id)
	if dev == nil {
		return nil
	}
	sum := sha256.Sum256(signedMessage(r.Method, r.Host, r.URL.RequestURI(), ts, nonce))
	if !ecdsa.VerifyASN1(pk, sum[:], sig) {
		return nil
	}
	// Only consume the nonce after the signature checks out, so garbage
	// can't fill the cache.
	if !s.nonces.use(id + ":" + nonce) {
		return nil
	}
	return dev
}

func (s *Server) auth(next func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// No IP rate limit here: forging a signature is infeasible, and
		// counting failures would let anyone sharing an IP (NAT) or a
		// skewed clock lock out the real device. Only /pair is limited.
		dev := s.verify(r)
		if dev == nil {
			http.NotFound(w, r) // reveal nothing
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, dev.ID)))
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if s.lim.blocked(ip) {
		http.Error(w, "too many attempts", http.StatusTooManyRequests)
		return
	}
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(pairBodyTimeout))
	var req struct {
		Code   string `json:"code"`
		Name   string `json:"name"`
		PubKey string `json:"pubkey"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	id, err := s.st.Redeem(req.Code, req.Name, req.PubKey)
	if err != nil {
		if errors.Is(err, ErrBadCode) {
			s.lim.fail(ip)
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		if errors.Is(err, ErrBadKey) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Printf("pair: %v", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	log.Printf("paired device %s from %s", id, ip)
	writeJSON(w, http.StatusOK, map[string]string{"device_id": id})
}

// ---------- terminal websocket ----------
//
// Binary frames carry raw terminal bytes in both directions.
// Text frames carry JSON control messages:
//   client -> server: {"t":"resize","cols":N,"rows":N}
//                     {"t":"clip_set","text":"..."}   (put text in tmux buffer)
//                     {"t":"clip_get"}                (reply with tmux buffer)
//                     {"t":"ping"}
//   server -> client: {"t":"clip","text":"..."}
//                     {"t":"pong"}
//                     {"t":"exit"}                    (tmux client ended)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  32 << 10,
	WriteBufferSize: 32 << 10,
	// Auth is a bearer header, not a cookie, so cross-site requests can't
	// carry credentials; origin checks add nothing here.
	CheckOrigin: func(*http.Request) bool { return true },
}

type ctrlMsg struct {
	T    string `json:"t"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
	Text string `json:"text,omitempty"`
}

func atoiDefault(s string, d int) int {
	if n, err := strconv.Atoi(s); err == nil && n > 0 && n < 1000 {
		return n
	}
	return d
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("session")
	if !sessionRe.MatchString(name) {
		http.Error(w, "bad session name", http.StatusBadRequest)
		return
	}
	cols := atoiDefault(r.URL.Query().Get("cols"), 80)
	rows := atoiDefault(r.URL.Query().Get("rows"), 24)

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// OSC 52 needs set-clipboard on; only replace tmux's default ("external")
	// so a user's own setting survives.
	args := []string{"-u", "new-session", "-A", "-s", name}
	if home, err := os.UserHomeDir(); err == nil {
		args = append(args, "-c", home) // start dir for newly created sessions only; ignored on attach
	}
	args = append(args, ";", "if-shell", "-F", "#{==:#{set-clipboard},external}", "set-option -g set-clipboard on")
	cmd := exec.Command("tmux", args...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	if os.Getenv("LANG") == "" {
		cmd.Env = append(cmd.Env, "LANG=C.UTF-8")
	}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		log.Printf("start tmux: %v", err)
		_ = conn.WriteJSON(ctrlMsg{T: "exit"})
		return
	}
	// Closing the pty hangs up the tmux *client*; the tmux session (and its
	// programs) keep running, which is what makes sessions persistent.
	defer func() {
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	var wmu sync.Mutex
	writeMsg := func(typ int, b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
		return conn.WriteMessage(typ, b)
	}
	writeCtrl := func(m ctrlMsg) {
		b, _ := json.Marshal(m)
		_ = writeMsg(websocket.TextMessage, b)
	}

	done := make(chan struct{})

	// pty -> ws
	go func() {
		defer close(done)
		buf := make([]byte, 32<<10)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				if werr := writeMsg(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				writeCtrl(ctrlMsg{T: "exit"})
				return
			}
		}
	}()

	// A live terminal outlives its signature check, so drop it once the device is revoked.
	deviceID, _ := r.Context().Value(ctxKey{}).(string)
	go func() {
		t := time.NewTicker(revokeInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if dev, _ := s.st.Lookup(deviceID); dev == nil {
					_ = conn.Close()
					return
				}
			}
		}
	}()

	// keepalive
	go func() {
		t := time.NewTicker(25 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				wmu.Lock()
				err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
				wmu.Unlock()
				if err != nil {
					return
				}
			}
		}
	}()

	const readTimeout = 70 * time.Second
	_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(readTimeout))
	})
	conn.SetReadLimit(4 << 20)

	// ws -> pty
	for {
		typ, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
		switch typ {
		case websocket.BinaryMessage:
			if _, err := ptmx.Write(data); err != nil {
				return
			}
		case websocket.TextMessage:
			var m ctrlMsg
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			switch m.T {
			case "resize":
				if m.Cols > 0 && m.Rows > 0 && m.Cols < 1000 && m.Rows < 1000 {
					_ = pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(m.Cols), Rows: uint16(m.Rows)})
				}
			case "clip_set":
				if len(m.Text) > 0 && len(m.Text) <= 1<<20 {
					_ = exec.Command("tmux", "set-buffer", "--", m.Text).Run()
				}
			case "clip_get":
				out, _ := exec.Command("tmux", "show-buffer").Output()
				if len(out) > 1<<20 { // matches the clip_set cap
					out = out[:1<<20]
				}
				writeCtrl(ctrlMsg{T: "clip", Text: string(out)})
			case "ping":
				writeCtrl(ctrlMsg{T: "pong"})
			}
		}
	}
}

// ---------- tmux sessions ----------

type sessionInfo struct {
	Name     string `json:"name"`
	Windows  int    `json:"windows"`
	Attached int    `json:"attached"`
	Created  int64  `json:"created"`
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	out, err := exec.Command("tmux", "list-sessions", "-F",
		"#{session_name}|#{session_windows}|#{session_attached}|#{session_created}").Output()
	list := []sessionInfo{}
	if err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			// tmux won't emit raw tabs when piped; parse from the right so a
			// '|' inside someone else's session name can't break the numbers.
			f := strings.Split(line, "|")
			if len(f) < 4 {
				continue
			}
			n := len(f)
			if !sessionRe.MatchString(strings.Join(f[:n-3], "|")) {
				continue // can't be attached or killed through the API
			}
			win, _ := strconv.Atoi(f[n-3])
			att, _ := strconv.Atoi(f[n-2])
			cr, _ := strconv.ParseInt(f[n-1], 10, 64)
			list = append(list, sessionInfo{strings.Join(f[:n-3], "|"), win, att, cr})
		}
	} // "no server running" just means no sessions
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleKill(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !sessionRe.MatchString(name) {
		http.Error(w, "bad session name", http.StatusBadRequest)
		return
	}
	// Already-gone session (e.g. shell exited) counts as killed.
	if err := exec.Command("tmux", "kill-session", "-t", "="+name).Run(); err != nil &&
		exec.Command("tmux", "has-session", "-t", "="+name).Run() == nil {
		http.Error(w, "kill failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- files ----------

var errOutside = errors.New("path outside files root")

// resolve maps a client path to an absolute path guaranteed (after symlink
// resolution) to be inside filesRoot. For not-yet-existing targets the
// parent directory is resolved instead.
func (s *Server) resolve(p string) (string, error) {
	if p == "" {
		p = "."
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(s.filesRoot, p)
	}
	p = filepath.Clean(p)
	if !within(s.filesRoot, p) {
		return "", errOutside
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent, err := filepath.EvalSymlinks(filepath.Dir(p))
		if err != nil {
			return "", err
		}
		real = filepath.Join(parent, filepath.Base(p))
	}
	if !within(s.filesRoot, real) {
		return "", errOutside
	}
	return real, nil
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (s *Server) pathErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errOutside):
		http.Error(w, "forbidden", http.StatusForbidden)
	case errors.Is(err, os.ErrNotExist):
		http.Error(w, "not found", http.StatusNotFound)
	default:
		http.Error(w, "error", http.StatusInternalServerError)
	}
}

type fileEntry struct {
	Name  string `json:"name"`
	Dir   bool   `json:"dir"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"`
}

func (s *Server) handleLs(w http.ResponseWriter, r *http.Request) {
	p, err := s.resolve(r.URL.Query().Get("path"))
	if err != nil {
		s.pathErr(w, err)
		return
	}
	ents, err := os.ReadDir(p)
	if err != nil {
		s.pathErr(w, err)
		return
	}
	list := make([]fileEntry, 0, len(ents))
	for _, e := range ents {
		info, err := e.Info()
		if err != nil {
			continue
		}
		list = append(list, fileEntry{e.Name(), e.IsDir(), info.Size(), info.ModTime().Unix()})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Dir != list[j].Dir {
			return list[i].Dir
		}
		return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
	})
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "entries": list})
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	p, err := s.resolve(r.URL.Query().Get("path"))
	if err != nil {
		s.pathErr(w, err)
		return
	}
	// Check before opening: os.Open on a FIFO blocks forever.
	if fi, err := os.Lstat(p); err != nil {
		s.pathErr(w, err)
		return
	} else if !fi.Mode().IsRegular() {
		http.Error(w, "not a file", http.StatusBadRequest)
		return
	}
	f, err := os.Open(p)
	if err != nil {
		s.pathErr(w, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		s.pathErr(w, err)
		return
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(p)}))
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, filepath.Base(p), info.ModTime(), f) // supports Range/resume
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	p, err := s.resolve(r.URL.Query().Get("path"))
	if err != nil {
		s.pathErr(w, err)
		return
	}
	overwrite := r.URL.Query().Get("overwrite") == "1"
	mode := os.FileMode(0o600)
	if fi, err := os.Lstat(p); err == nil {
		if !fi.Mode().IsRegular() {
			http.Error(w, "target is not a regular file", http.StatusBadRequest)
			return
		}
		if !overwrite {
			http.Error(w, "exists", http.StatusConflict)
			return
		}
		mode = fi.Mode().Perm() // overwrite keeps the file's permissions
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".upload-*")
	if err != nil {
		s.pathErr(w, err)
		return
	}
	defer os.Remove(tmp.Name())
	body := http.MaxBytesReader(w, r.Body, s.maxUpload)
	n, err := io.Copy(tmp, body)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), mode)
	}
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "upload too large", http.StatusRequestEntityTooLarge)
		} else {
			log.Printf("upload %s: %v", p, err)
			http.Error(w, "upload failed", http.StatusInternalServerError)
		}
		return
	}
	// Without overwrite, publish with link(2): it fails if p appeared since
	// the check above, where rename would silently replace it.
	// ponytail: needs hard-link support on the files-root filesystem.
	if overwrite {
		err = os.Rename(tmp.Name(), p)
	} else {
		err = os.Link(tmp.Name(), p)
	}
	if errors.Is(err, os.ErrExist) {
		http.Error(w, "exists", http.StatusConflict)
		return
	}
	if err != nil {
		log.Printf("upload %s: %v", p, err)
		http.Error(w, "error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "size": n})
}
