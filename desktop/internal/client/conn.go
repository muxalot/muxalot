package client

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type State string

const (
	Connecting   State = "connecting"
	Connected    State = "connected"
	Reconnecting State = "reconnecting"
	Exited       State = "exited"
)

const (
	readLimit = 4 << 20 // a hostile server can't make us buffer more than this per frame
	pingEvery = 20 * time.Second
	idleLimit = 60 * time.Second
)

// backoffUnit is the first reconnect delay; tests shrink it.
var backoffUnit = 500 * time.Millisecond

// Conn streams one tmux session. Binary frames are raw terminal bytes, text
// frames are JSON control messages (see agent/server.go). It reconnects with
// backoff; the session lives in tmux on the server, so reattaching restores the screen.
type Conn struct {
	c       *Client
	session string
	onData  func([]byte)
	onState func(State)

	mu     sync.Mutex // guards ws, cols, rows, closed
	ws     *websocket.Conn
	cols   int
	rows   int
	closed bool
	wmu    sync.Mutex // serialises writes
	done   chan struct{}
	kick   chan struct{}
	unit   time.Duration // first reconnect delay, copied from backoffUnit so tests can't race it
}

// Dial starts streaming session. Callbacks run on the connection's goroutine.
func (c *Client) Dial(session string, cols, rows int, onData func([]byte), onState func(State)) (*Conn, error) {
	if !ValidSession(session) {
		return nil, errors.New("invalid session name")
	}
	if cols <= 0 || rows <= 0 {
		cols, rows = 80, 24
	}
	cn := &Conn{c: c, session: session, onData: onData, onState: onState, cols: cols, rows: rows,
		done: make(chan struct{}), kick: make(chan struct{}, 1), unit: backoffUnit}
	go cn.run()
	return cn, nil
}

func (cn *Conn) state(s State) { cn.onState(s) }

func (cn *Conn) isClosed() bool {
	cn.mu.Lock()
	defer cn.mu.Unlock()
	return cn.closed
}

func (cn *Conn) run() {
	for attempt := 0; !cn.isClosed(); {
		if attempt == 0 {
			cn.state(Connecting)
		} else {
			cn.state(Reconnecting)
		}
		ws := cn.dial()
		if ws != nil {
			attempt = 0
			cn.state(Connected)
			if cn.read(ws) { // server said the tmux client ended
				cn.state(Exited)
				return
			}
		}
		if cn.isClosed() {
			return
		}
		attempt++
		delay := min(15*time.Second, cn.unit<<min(attempt, 5))
		select {
		case <-time.After(delay):
		case <-cn.kick:
			attempt = 0
		case <-cn.done:
			return
		}
	}
}

func (cn *Conn) dial() *websocket.Conn {
	cn.mu.Lock()
	cols, rows := cn.cols, cn.rows
	cn.mu.Unlock()
	u := cn.c.endpoint("/ws", url.Values{
		"session": {cn.session},
		"cols":    {strconv.Itoa(cols)},
		"rows":    {strconv.Itoa(rows)},
	})
	auth, err := cn.c.sign(u, http.MethodGet)
	if err != nil {
		return nil
	}
	wsu := *u
	if wsu.Scheme == "https" {
		wsu.Scheme = "wss"
	} else {
		wsu.Scheme = "ws"
	}
	d := websocket.Dialer{HandshakeTimeout: 15 * time.Second, Proxy: http.ProxyFromEnvironment}
	ws, _, err := d.Dial(wsu.String(), http.Header{"Authorization": {auth}})
	if err != nil {
		return nil
	}
	ws.SetReadLimit(readLimit)
	cn.mu.Lock()
	if cn.closed {
		cn.mu.Unlock()
		ws.Close()
		return nil
	}
	cn.ws = ws
	cn.mu.Unlock()
	return ws
}

// read pumps frames until the socket dies; it reports whether the server ended the session.
func (cn *Conn) read(ws *websocket.Conn) (exited bool) {
	stop := make(chan struct{})
	defer func() {
		close(stop)
		ws.Close()
		cn.mu.Lock()
		if cn.ws == ws {
			cn.ws = nil
		}
		cn.mu.Unlock()
	}()
	extend := func() { ws.SetReadDeadline(time.Now().Add(idleLimit)) }
	extend()
	ws.SetPongHandler(func(string) error { extend(); return nil })
	go func() {
		t := time.NewTicker(pingEvery)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
			case <-stop:
				return
			}
		}
	}()
	for {
		typ, msg, err := ws.ReadMessage()
		if err != nil {
			return false
		}
		extend()
		switch typ {
		case websocket.BinaryMessage:
			cn.onData(msg)
		case websocket.TextMessage:
			var m struct {
				T string `json:"t"`
			}
			if json.Unmarshal(msg, &m) == nil && m.T == "exit" {
				cn.mu.Lock()
				cn.closed = true
				cn.mu.Unlock()
				return true
			}
		}
	}
}

func (cn *Conn) write(typ int, b []byte) error {
	cn.mu.Lock()
	ws := cn.ws
	cn.mu.Unlock()
	if ws == nil {
		return errors.New("not connected")
	}
	cn.wmu.Lock()
	defer cn.wmu.Unlock()
	ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return ws.WriteMessage(typ, b)
}

// Send writes keyboard bytes to the terminal. Input while disconnected is dropped.
func (cn *Conn) Send(b []byte) error { return cn.write(websocket.BinaryMessage, b) }

func (cn *Conn) Resize(cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	cn.mu.Lock()
	cn.cols, cn.rows = cols, rows
	cn.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"t": "resize", "cols": cols, "rows": rows})
	cn.write(websocket.TextMessage, b)
}

// Kick drops the current socket and reconnects immediately (e.g. the network changed).
func (cn *Conn) Kick() {
	cn.mu.Lock()
	ws := cn.ws
	cn.mu.Unlock()
	if ws != nil {
		ws.Close()
	}
	select {
	case cn.kick <- struct{}{}:
	default:
	}
}

// Close ends the connection; the tmux session keeps running on the server.
func (cn *Conn) Close() {
	cn.mu.Lock()
	if cn.closed {
		cn.mu.Unlock()
		return
	}
	cn.closed = true
	ws := cn.ws
	cn.mu.Unlock()
	close(cn.done)
	if ws != nil {
		ws.Close()
	}
}
