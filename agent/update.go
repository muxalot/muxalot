package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	updRepo     = "muxalot/muxalot"
	updAPI      = "https://api.github.com"
	updDownload = "https://github.com/" + updRepo + "/releases/download"
	updDir      = "/var/lib/muxalot-agent"
	updInterval = 6 * time.Hour
)

var tagRe = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

var (
	errNoUpdate     = errors.New("no update available")
	errApplying     = errors.New("update already requested")
	errNotInstalled = errors.New("self-update is not installed; re-run the installer")
)

func parseVer(s string) ([]int, bool) {
	p := strings.Split(strings.TrimPrefix(s, "v"), ".")
	if len(p) != 3 {
		return nil, false
	}
	v := make([]int, 3)
	for i := range p {
		n, err := strconv.Atoi(p[i])
		if err != nil || n < 0 {
			return nil, false
		}
		v[i] = n
	}
	return v, true
}

// newer reports latest > cur. Unparsable versions (e.g. "dev", "0.2.9-dev") never update.
func newer(latest, cur string) bool {
	l, ok1 := parseVer(latest)
	c, ok2 := parseVer(cur)
	return ok1 && ok2 && slices.Compare(l, c) > 0
}

type UpdateStatus struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Available bool   `json:"available"`
	URL       string `json:"url"`
	Notes     string `json:"notes"`
	Applying  bool   `json:"applying"`
	LastError string `json:"last_error,omitempty"`
}

type release struct {
	Tag        string `json:"tag_name"`
	URL        string `json:"html_url"`
	Body       string `json:"body"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

type Updater struct {
	cur, api, dir string
	hc            *http.Client

	mu   sync.Mutex
	etag string
	rel  release
}

func NewUpdater(cur, dir string) *Updater {
	return &Updater{cur: cur, api: updAPI, dir: dir, hc: &http.Client{Timeout: 10 * time.Second}}
}

func (u *Updater) check(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", u.api+"/repos/"+updRepo+"/releases/latest", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	u.mu.Lock()
	if u.etag != "" {
		req.Header.Set("If-None-Match", u.etag)
	}
	u.mu.Unlock()
	resp, err := u.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github: HTTP %d", resp.StatusCode)
	}
	var r release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return err
	}
	u.mu.Lock()
	u.rel, u.etag = r, resp.Header.Get("ETag")
	u.mu.Unlock()
	return nil
}

// Run checks now and every updInterval; failures are logged and retried next round.
func (u *Updater) Run(ctx context.Context) {
	for {
		if err := u.check(ctx); err != nil {
			log.Printf("update check: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(updInterval):
		}
	}
}

func (u *Updater) Status() UpdateStatus {
	u.mu.Lock()
	r := u.rel
	u.mu.Unlock()
	st := UpdateStatus{Current: u.cur, Latest: r.Tag, URL: r.URL, Notes: truncRunes(r.Body, 2000)}
	st.Available = !r.Draft && !r.Prerelease && tagRe.MatchString(r.Tag) && newer(r.Tag, u.cur)
	_, err := os.Stat(filepath.Join(u.dir, "request"))
	st.Applying = err == nil
	if b, err := os.ReadFile(filepath.Join(u.dir, "result")); err == nil {
		var res updateResult
		if json.Unmarshal(b, &res) == nil && !res.OK && res.Tag == r.Tag {
			st.LastError = res.Error
		}
	}
	return st
}

func truncRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// Request asks the root helper (muxalot-agent-update.path) to install the latest release.
func (u *Updater) Request() (string, error) {
	st := u.Status()
	if !st.Available {
		return "", errNoUpdate
	}
	f, err := os.OpenFile(filepath.Join(u.dir, "request"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	switch {
	case errors.Is(err, os.ErrExist):
		return "", errApplying
	case errors.Is(err, os.ErrNotExist):
		return "", errNotInstalled
	case err != nil:
		return "", err
	}
	_ = os.Remove(filepath.Join(u.dir, "result")) // a previous failure must not read as this attempt's
	_, err = f.WriteString(st.Latest)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return st.Latest, nil
}

type updateResult struct {
	Tag   string `json:"tag"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func fetch(hc *http.Client, url string, max int64) ([]byte, error) {
	resp, err := hc.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s: too large", url)
	}
	return b, nil
}

// sumFor finds name in sha256sum output.
func sumFor(sums []byte, name string) string {
	for _, l := range strings.Split(string(sums), "\n") {
		if f := strings.Fields(l); len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return f[0]
		}
	}
	return ""
}

// applyUpdate runs as root. The request file only names a tag; the binary and its checksum
// come from the GitHub release itself, so an agent that is compromised can at most ask for
// a real, newer release. The outcome always lands in dir/result for the agent to report.
func applyUpdate(hc *http.Client, dir, dl, cur, dst string) (err error) {
	tag := ""
	defer func() {
		res := updateResult{Tag: tag, OK: err == nil}
		if err != nil {
			res.Error = err.Error()
		}
		b, _ := json.Marshal(res)
		p := filepath.Join(dir, "result")
		if os.WriteFile(p, b, 0o644) == nil {
			if fi, e := os.Stat(dir); e == nil {
				if st, ok := fi.Sys().(*syscall.Stat_t); ok {
					_ = os.Chown(p, int(st.Uid), int(st.Gid))
				}
			}
		}
	}()
	reqPath := filepath.Join(dir, "request")
	f, err := os.OpenFile(reqPath, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		os.Remove(reqPath)
		return errors.New("request is not a regular file")
	}
	b, _ := io.ReadAll(io.LimitReader(f, 32))
	f.Close()
	os.Remove(reqPath)
	tag = strings.TrimSpace(string(b))
	if !tagRe.MatchString(tag) {
		return fmt.Errorf("bad tag %q", tag)
	}
	if !newer(tag, cur) {
		return fmt.Errorf("%s is not newer than %s", tag, cur)
	}
	asset := "muxalot-agent-linux-" + runtime.GOARCH
	sums, err := fetch(hc, dl+"/"+tag+"/SHA256SUMS", 1<<20)
	if err != nil {
		return err
	}
	bin, err := fetch(hc, dl+"/"+tag+"/"+asset, 64<<20)
	if err != nil {
		return err
	}
	want := sumFor(sums, asset)
	got := sha256.Sum256(bin)
	if want == "" || hex.EncodeToString(got[:]) != want {
		return errors.New("checksum mismatch")
	}
	tmp := filepath.Join(filepath.Dir(dst), ".muxalot-agent.new")
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// runApplyUpdate is the `apply-update` subcommand, run as root by muxalot-agent-update.service.
func runApplyUpdate(dir string) {
	if os.Getuid() != 0 {
		log.Fatal("apply-update must run as root")
	}
	hc := &http.Client{Timeout: 5 * time.Minute}
	if err := applyUpdate(hc, dir, updDownload, version, installBin); err != nil {
		log.Fatal(err)
	}
	if err := run("systemctl", "restart", "muxalot-agent"); err != nil {
		log.Fatal(err)
	}
}
