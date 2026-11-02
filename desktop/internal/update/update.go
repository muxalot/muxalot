// Package update checks GitHub for a newer muxalot release and swaps the desktop binary in place.
package update

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

const (
	API      = "https://api.github.com/repos/muxalot/muxalot/releases/latest"
	Download = "https://github.com/muxalot/muxalot/releases/download"
)

var tagRe = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

type Info struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	URL       string `json:"url"`
	Notes     string `json:"notes"`
	Available bool   `json:"available"`
	CanApply  bool   `json:"canApply"` // false: alert only (deb, other OS), the user updates by hand
}

// Newer reports latest > cur. Unparsable versions (e.g. "dev", "0.2.9-dev") never update.
func Newer(latest, cur string) bool {
	l, ok1 := parse(latest)
	c, ok2 := parse(cur)
	return ok1 && ok2 && slices.Compare(l, c) > 0
}

func parse(s string) ([]int, bool) {
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

// canApply: only an AppImage or the Windows exe can replace itself; a dpkg-owned file must not be.
func canApply() bool {
	switch runtime.GOOS {
	case "linux":
		return os.Getenv("APPIMAGE") != "" && runtime.GOARCH == "amd64"
	case "windows":
		return runtime.GOARCH == "amd64"
	}
	return false
}

// Check asks GitHub for the latest stable release.
func Check(ctx context.Context, hc *http.Client, api, cur string) (Info, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", api, nil)
	if err != nil {
		return Info{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := hc.Do(req)
	if err != nil {
		return Info{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Info{}, fmt.Errorf("github: HTTP %d", resp.StatusCode)
	}
	var r struct {
		Tag        string `json:"tag_name"`
		URL        string `json:"html_url"`
		Body       string `json:"body"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return Info{}, err
	}
	notes := []rune(r.Body)
	if len(notes) > 2000 {
		notes = notes[:2000]
	}
	return Info{
		Current: cur, Latest: r.Tag, URL: r.URL, Notes: string(notes),
		Available: !r.Draft && !r.Prerelease && tagRe.MatchString(r.Tag) && Newer(r.Tag, cur),
		CanApply:  canApply(),
	}, nil
}

func get(ctx context.Context, hc *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return resp, nil
}

// fetchVerified downloads dl/tag/asset to dst and checks it against the release's sums file.
// On any failure dst is removed.
func fetchVerified(ctx context.Context, hc *http.Client, dl, tag, asset, sumsName, dst string) (err error) {
	sr, err := get(ctx, hc, dl+"/"+tag+"/"+sumsName)
	if err != nil {
		return err
	}
	sums, err := io.ReadAll(io.LimitReader(sr.Body, 1<<20))
	sr.Body.Close()
	if err != nil {
		return err
	}
	want := ""
	for _, l := range strings.Split(string(sums), "\n") {
		if f := strings.Fields(l); len(f) == 2 && strings.TrimPrefix(f[1], "*") == asset {
			want = f[0]
		}
	}
	if want == "" {
		return errors.New("no checksum published for " + asset)
	}
	ar, err := get(ctx, hc, dl+"/"+tag+"/"+asset)
	if err != nil {
		return err
	}
	defer ar.Body.Close()
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			os.Remove(dst)
		}
	}()
	h := sha256.New()
	const max = 512 << 20
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(ar.Body, max+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n > max {
		return errors.New("download too large")
	}
	if hex.EncodeToString(h.Sum(nil)) != want {
		return errors.New("checksum mismatch")
	}
	return nil
}

// Apply replaces the running desktop binary with info.Latest and restarts into it.
// It returns only on failure (the old binary is left untouched).
func Apply(ctx context.Context, hc *http.Client, dl string, info Info) error {
	if !info.Available || !info.CanApply || !tagRe.MatchString(info.Latest) {
		return errors.New("no applicable update")
	}
	ver := strings.TrimPrefix(info.Latest, "v")
	switch runtime.GOOS {
	case "linux":
		app := os.Getenv("APPIMAGE")
		tmp := app + ".new"
		if err := fetchVerified(ctx, hc, dl, info.Latest, "muxalot-desktop_"+ver+"_amd64.AppImage", "SHA256SUMS-appimage", tmp); err != nil {
			return err
		}
		if err := os.Rename(tmp, app); err != nil {
			os.Remove(tmp)
			return err
		}
		return restart(app)
	case "windows":
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		zipPath := exe + ".zip"
		defer os.Remove(zipPath)
		if err := fetchVerified(ctx, hc, dl, info.Latest, "muxalot-desktop_"+ver+"_windows-amd64.zip", "SHA256SUMS-windows", zipPath); err != nil {
			return err
		}
		// a running exe can be renamed but not overwritten
		if err := extractExe(zipPath, exe+".new"); err != nil {
			return err
		}
		if err := os.Rename(exe, exe+".old"); err != nil {
			os.Remove(exe + ".new")
			return err
		}
		if err := os.Rename(exe+".new", exe); err != nil {
			_ = os.Rename(exe+".old", exe)
			return err
		}
		return restart(exe)
	}
	return errors.New("self-update is not supported on " + runtime.GOOS)
}

func extractExe(zipPath, dst string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, zf := range zr.File {
		if zf.Name != "muxalot-desktop.exe" {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, io.LimitReader(rc, 512<<20))
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(dst)
		}
		return err
	}
	return errors.New("muxalot-desktop.exe not in the archive")
}

// CleanupOld removes the previous exe left behind by a Windows update.
func CleanupOld() {
	if exe, err := os.Executable(); err == nil {
		_ = os.Remove(exe + ".old")
	}
}
