package client

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// maxBody caps API responses; a hostile server could otherwise stream us out of memory.
const maxBody = 1 << 20

var sessionRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// ValidSession reports whether name is an acceptable tmux session name.
func ValidSession(name string) bool { return sessionRe.MatchString(name) }

type SessionInfo struct {
	Name     string `json:"name"`
	Windows  int    `json:"windows"`
	Attached int    `json:"attached"`
	Created  int64  `json:"created"`
}

type FileEntry struct {
	Name  string `json:"name"`
	Dir   bool   `json:"dir"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
}

type LsResult struct {
	Path    string      `json:"path"`
	Entries []FileEntry `json:"entries"`
}

// APIError is a non-2xx answer from the agent.
type APIError struct {
	Code int
	Msg  string
}

func (e *APIError) Error() string { return e.Msg }

// CanonicalURL validates a server URL and returns it without a trailing slash
// or default port. Only https is accepted, except http to a loopback host (dev, tests).
func CanonicalURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", errors.New("invalid server URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("server URL must not contain credentials, query or fragment")
	}
	switch u.Scheme {
	case "https":
		if u.Port() == "443" {
			u.Host = u.Hostname()
			if strings.Contains(u.Host, ":") {
				u.Host = "[" + u.Host + "]"
			}
		}
	case "http":
		if !isLoopback(u.Hostname()) {
			return "", errors.New("server URL must be https")
		}
	default:
		return "", errors.New("server URL must be https")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func newHTTP() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	tr.ResponseHeaderTimeout = 60 * time.Second
	return &http.Client{
		Transport:     tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func readBody(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxBody {
		return nil, errors.New("response too large")
	}
	return b, nil
}

func apiError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	msg := strings.TrimSpace(string(b))
	if msg == "" {
		msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return &APIError{Code: resp.StatusCode, Msg: msg}
}

// Pair registers pubKey (base64 X.509 SPKI) with a one-time code and returns the device id.
func Pair(rawURL, code, name, pubKey string) (string, error) {
	base, err := CanonicalURL(rawURL)
	if err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]string{"code": code, "name": name, "pubkey": pubKey})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/pair", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := newHTTP().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", apiError(resp)
	}
	b, err := readBody(resp.Body)
	if err != nil {
		return "", err
	}
	var out struct {
		DeviceID string `json:"device_id"`
	}
	if err := json.Unmarshal(b, &out); err != nil || out.DeviceID == "" {
		return "", errors.New("unexpected pairing response")
	}
	return out.DeviceID, nil
}

// Client is an authenticated view of one paired server.
type Client struct {
	base     *url.URL
	deviceID string
	key      *ecdsa.PrivateKey
	http     *http.Client
}

func New(rawURL, deviceID string, key *ecdsa.PrivateKey) (*Client, error) {
	c, err := CanonicalURL(rawURL)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(c)
	return &Client{base: u, deviceID: deviceID, key: key, http: newHTTP()}, nil
}

// endpoint builds base + path (+ query) as a URL the agent will see.
func (c *Client) endpoint(path string, q url.Values) *url.URL {
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawPath = ""
	if q != nil {
		u.RawQuery = q.Encode()
	}
	return &u
}

func (c *Client) sign(u *url.URL, method string) (string, error) {
	return Authorization(c.key, c.deviceID, method, u.Host, u.RequestURI())
}

func (c *Client) do(ctx context.Context, method string, u *url.URL, body io.Reader, length int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = length
	}
	h, err := c.sign(u, method)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", h)
	return c.http.Do(req)
}

func (c *Client) getJSON(u *url.URL, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	resp, err := c.do(ctx, http.MethodGet, u, nil, 0)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiError(resp)
	}
	b, err := readBody(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func (c *Client) Sessions() ([]SessionInfo, error) {
	var out []SessionInfo
	err := c.getJSON(c.endpoint("/sessions", nil), &out)
	return out, err
}

// Kill ends a tmux session. The agent answers 204 if it is already gone.
func (c *Client) Kill(name string) error {
	if !ValidSession(name) {
		return errors.New("invalid session name")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	resp, err := c.do(ctx, http.MethodDelete, c.endpoint("/sessions/"+name, nil), nil, 0)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return apiError(resp)
	}
	return nil
}

func (c *Client) Ls(path string) (LsResult, error) {
	var out LsResult
	err := c.getJSON(c.endpoint("/ls", url.Values{"path": {path}}), &out)
	return out, err
}
