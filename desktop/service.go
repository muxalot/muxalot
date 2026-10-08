package main

import (
	"context"
	"crypto/ecdsa"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"muxalot-desktop/internal/client"
	"muxalot-desktop/internal/config"
	"muxalot-desktop/internal/keystore"
)

// Service is the only app-defined surface the webview can call. Every exported
// method is reachable by any script in the page, so the set is fixed by a test
// (TestBoundSurface): nothing here signs, returns key material, or takes a local
// filesystem path from JS. Add a method only after asking who can call it.
//
// Not covered by that test: Wails' built-in runtime calls (open URL, clipboard,
// native dialogs, window control) are also reachable by any script in the page
// and are not restricted; the CSP is what keeps foreign scripts out.
type Service struct {
	app *application.App
	cfg *config.Store
	ks  *keystore.Store

	mu      sync.Mutex
	keys    map[string]*ecdsa.PrivateKey // unlocked keys by server id
	clients map[string]*client.Client
	conns   map[string]*termConn // "serverID/session"
}

type termConn struct {
	conn *client.Conn
	feed *feed
}

type ServerView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	KeyMode  string `json:"keyMode"`
	Unlocked bool   `json:"unlocked"`
}

type termData struct {
	Server  string `json:"server"`
	Session string `json:"session"`
	B64     string `json:"b64"`
}

type termState struct {
	Server  string       `json:"server"`
	Session string       `json:"session"`
	State   client.State `json:"state"`
}

type xferProgress struct {
	Name  string `json:"name"`
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
}

const maxBytes = 1 << 20 // input, paste and clipboard cap

func newService(cfg *config.Store, ks *keystore.Store) *Service {
	return &Service{cfg: cfg, ks: ks, keys: map[string]*ecdsa.PrivateKey{}, clients: map[string]*client.Client{}, conns: map[string]*termConn{}}
}

func (s *Service) find(id string) (config.Server, error) {
	for _, x := range s.cfg.Load() {
		if x.ID == id {
			return x, nil
		}
	}
	return config.Server{}, errors.New("unknown server")
}

// api returns the authenticated client for a server, loading its key on first use.
func (s *Service) api(id string) (*client.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.clients[id]; c != nil {
		return c, nil
	}
	srv, err := s.find(id)
	if err != nil {
		return nil, err
	}
	key := s.keys[id]
	if key == nil {
		if keystore.Mode(srv.KeyMode) == keystore.ModePassphrase {
			return nil, errors.New("locked: enter the passphrase")
		}
		if key, err = s.ks.Load(id, keystore.Mode(srv.KeyMode), ""); err != nil {
			return nil, err
		}
		s.keys[id] = key
	}
	c, err := client.New(srv.URL, srv.DeviceID, key)
	if err != nil {
		return nil, err
	}
	s.clients[id] = c
	return c, nil
}

func (s *Service) Servers() []ServerView {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []ServerView{}
	for _, x := range s.cfg.Load() {
		out = append(out, ServerView{ID: x.ID, Name: x.Name, URL: x.URL, KeyMode: x.KeyMode,
			Unlocked: keystore.Mode(x.KeyMode) == keystore.ModeKeyring || s.keys[x.ID] != nil})
	}
	return out
}

// Pair generates a device key, registers it with the server using the one-time
// code, and saves the server. deviceName is how the agent lists this device.
func (s *Service) Pair(rawURL, code, deviceName, mode, passphrase string) (ServerView, error) {
	base, err := client.CanonicalURL(rawURL)
	if err != nil {
		return ServerView{}, err
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return ServerView{}, errors.New("pairing code required")
	}
	if deviceName = strings.TrimSpace(deviceName); deviceName == "" {
		deviceName, _ = os.Hostname()
	}
	km := keystore.Mode(mode)
	id := config.NewID()
	pub, err := s.ks.Create(id, km, passphrase)
	if err != nil {
		return ServerView{}, err
	}
	dev, err := client.Pair(base, code, deviceName, pub)
	if err != nil {
		s.ks.Delete(id, km)
		return ServerView{}, err
	}
	u, _ := url.Parse(base)
	srv := config.Server{ID: id, Name: u.Host, URL: base, DeviceID: dev, KeyMode: mode}
	if err := s.cfg.Add(srv); err != nil {
		s.ks.Delete(id, km)
		return ServerView{}, err
	}
	if km == keystore.ModePassphrase {
		_ = s.Unlock(id, passphrase)
	}
	return ServerView{ID: id, Name: srv.Name, URL: base, KeyMode: mode, Unlocked: true}, nil
}

func (s *Service) Forget(id string) error {
	srv, err := s.find(id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	for k, tc := range s.conns {
		if strings.HasPrefix(k, id+"/") {
			tc.conn.Close()
			delete(s.conns, k)
		}
	}
	delete(s.clients, id)
	delete(s.keys, id)
	s.mu.Unlock()
	s.ks.Delete(id, keystore.Mode(srv.KeyMode))
	return s.cfg.Remove(id)
}

func (s *Service) Unlock(id, passphrase string) error {
	srv, err := s.find(id)
	if err != nil {
		return err
	}
	key, err := s.ks.Load(id, keystore.Mode(srv.KeyMode), passphrase)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.keys[id] = key
	delete(s.clients, id)
	s.mu.Unlock()
	return nil
}

func (s *Service) Sessions(id string) ([]string, error) {
	c, err := s.api(id)
	if err != nil {
		return nil, err
	}
	list, err := c.Sessions()
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, x := range list {
		if client.ValidSession(x.Name) {
			names = append(names, x.Name)
		}
	}
	return names, nil
}

// Attach opens (or reuses) the terminal stream for a session. Output arrives as
// "term:data" events, connection changes as "term:state".
func (s *Service) Attach(id, session string, cols, rows int) error {
	c, err := s.api(id)
	if err != nil {
		return err
	}
	key := id + "/" + session
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conns[key] != nil {
		return nil
	}
	f := &feed{emit: func(b []byte) {
		s.app.Event.Emit("term:data", termData{id, session, base64.StdEncoding.EncodeToString(b)})
	}}
	conn, err := c.Dial(session, cols, rows, f.add, func(st client.State) {
		s.app.Event.Emit("term:state", termState{id, session, st})
	})
	if err != nil {
		return err
	}
	s.conns[key] = &termConn{conn, f}
	return nil
}

// Detach closes the stream; the tmux session keeps running unless kill is set.
func (s *Service) Detach(id, session string, kill bool) error {
	s.mu.Lock()
	tc := s.conns[id+"/"+session]
	delete(s.conns, id+"/"+session)
	s.mu.Unlock()
	if tc != nil {
		tc.conn.Close()
	}
	if !kill {
		return nil
	}
	c, err := s.api(id)
	if err != nil {
		return err
	}
	return c.Kill(session)
}

// Rename renames the tmux session on the server. The frontend detaches and
// re-opens any tab it held under the old name.
func (s *Service) Rename(id, from, to string) error {
	c, err := s.api(id)
	if err != nil {
		return err
	}
	return c.Rename(from, to)
}

// TabColors lists the locally chosen chip colors for one server: session -> color.
func (s *Service) TabColors(id string) map[string]string {
	return s.cfg.TabColors()[id]
}

// SetTabColor records the client-side chip color for a tab; an empty color clears it.
func (s *Service) SetTabColor(id, session, color string) error {
	return s.cfg.SetTabColor(id, session, color)
}

func (s *Service) conn(id, session string) (*client.Conn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tc := s.conns[id+"/"+session]
	if tc == nil {
		return nil, errors.New("not attached")
	}
	return tc.conn, nil
}

// Send types base64-encoded bytes (from xterm.js onData/onBinary) into the terminal.
func (s *Service) Send(id, session, b64 string) error {
	if len(b64) > maxBytes*4/3+4 {
		return errors.New("input too large")
	}
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return err
	}
	cn, err := s.conn(id, session)
	if err != nil {
		return err
	}
	return cn.Send(b)
}

// Paste types clipboard text. bracketed is the terminal's bracketed-paste mode as reported by the page.
func (s *Service) Paste(id, session, text string, bracketed bool) error {
	if len(text) > maxBytes {
		return errors.New("paste too large")
	}
	cn, err := s.conn(id, session)
	if err != nil {
		return err
	}
	return cn.Send(pasteBytes(text, bracketed))
}

func (s *Service) Resize(id, session string, cols, rows int) error {
	cn, err := s.conn(id, session)
	if err != nil {
		return err
	}
	cn.Resize(cols, rows)
	return nil
}

func (s *Service) SetClipboard(text string) error {
	if len(text) > maxBytes {
		return errors.New("clipboard text too large")
	}
	if !s.app.Clipboard.SetText(text) {
		return errors.New("could not set the clipboard")
	}
	return nil
}

func (s *Service) Ls(id, remotePath string) (client.LsResult, error) {
	c, err := s.api(id)
	if err != nil {
		return client.LsResult{}, err
	}
	return c.Ls(remotePath)
}

// Download asks where to save (native dialog), then streams the remote file there.
// The suggested name is sanitised; the user's chosen path is never overwritten.
func (s *Service) Download(id, remotePath string) error {
	c, err := s.api(id)
	if err != nil {
		return err
	}
	name, err := client.SafeName(remotePath)
	if err != nil {
		return err
	}
	dst, err := s.app.Dialog.SaveFile().SetFilename(name).PromptForSingleSelection()
	if err != nil || dst == "" {
		return nil // cancelled
	}
	f, err := client.CreateExclusive(dst)
	if err != nil {
		return fmt.Errorf("cannot create %s: %w", filepath.Base(dst), err)
	}
	err = c.Download(context.Background(), remotePath, f, func(done, total int64) {
		s.app.Event.Emit("xfer:progress", xferProgress{name, done, total})
	})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst)
	}
	return err
}

// Upload asks which local file to send (native dialog) and puts it in remoteDir.
func (s *Service) Upload(id, remoteDir string) error {
	c, err := s.api(id)
	if err != nil {
		return err
	}
	local, err := s.app.Dialog.OpenFile().CanChooseFiles(true).PromptForSingleSelection()
	if err != nil || local == "" {
		return nil // cancelled
	}
	name, err := client.SafeName(filepath.Base(local))
	if err != nil {
		return err
	}
	remote := path.Join(remoteDir, name)
	send := func(overwrite bool) error {
		f, err := os.Open(local)
		if err != nil {
			return err
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			return err
		}
		if !fi.Mode().IsRegular() {
			return errors.New("not a regular file")
		}
		return c.Upload(context.Background(), remote, fi.Size(), overwrite, io.Reader(f), func(done int64) {
			s.app.Event.Emit("xfer:progress", xferProgress{name, done, fi.Size()})
		})
	}
	err = send(false)
	var ae *client.APIError
	if errors.As(err, &ae) && ae.Code == 409 && s.confirmOverwrite(name) {
		err = send(true)
	}
	return err
}

func (s *Service) confirmOverwrite(name string) bool {
	ans := make(chan bool, 1)
	d := s.app.Dialog.Question().SetTitle("File exists").SetMessage(fmt.Sprintf("%q already exists on the server. Overwrite it?", name))
	d.AddButton("Overwrite").OnClick(func() { ans <- true })
	d.SetCancelButton(d.AddButton("Cancel").SetAsDefault().OnClick(func() { ans <- false }))
	d.Show()
	select {
	case v := <-ans:
		return v
	case <-time.After(5 * time.Minute):
		return false
	}
}

// pasteBytes prepares clipboard text for the terminal: control characters (ESC
// included, so a paste can't close its own bracket) are dropped, newlines become
// CR, and text is wrapped for bracketed paste when the terminal asked for it.
func pasteBytes(text string, bracketed bool) []byte {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var b strings.Builder
	for _, r := range text {
		switch {
		case r == '\n':
			b.WriteByte('\r')
		case r == '\t' || r >= 0x20 && r != 0x7f && !(r >= 0x80 && r < 0xa0):
			b.WriteRune(r)
		}
	}
	if bracketed {
		return []byte("\x1b[200~" + b.String() + "\x1b[201~")
	}
	return []byte(b.String())
}

// feed coalesces terminal output into ~8 ms batches of at most 48 KB per event.
type feed struct {
	mu    sync.Mutex
	buf   []byte
	armed bool
	emit  func([]byte)
}

func (f *feed) add(b []byte) {
	f.mu.Lock()
	f.buf = append(f.buf, b...)
	if !f.armed {
		f.armed = true
		time.AfterFunc(8*time.Millisecond, f.flush)
	}
	f.mu.Unlock()
}

func (f *feed) flush() {
	f.mu.Lock()
	b := f.buf
	f.buf, f.armed = nil, false
	f.mu.Unlock()
	for len(b) > 0 {
		n := min(48<<10, len(b))
		f.emit(b[:n])
		b = b[n:]
	}
}
