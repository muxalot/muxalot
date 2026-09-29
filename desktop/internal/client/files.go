package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// SafeName reduces a server-supplied file name to a single, harmless path element.
func SafeName(name string) (string, error) {
	name = strings.NewReplacer("\\", "/").Replace(name)
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, 0) || len(name) > 255 {
		return "", errors.New("unsafe file name")
	}
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return "", errors.New("unsafe file name")
	}
	stem := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	switch stem {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return "", errors.New("unsafe file name")
	}
	return name, nil
}

// CreateExclusive creates a new 0600 file at path; it fails if anything exists there.
// O_EXCL also refuses a symlink at the final component, dangling or not.
func CreateExclusive(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

// Download streams a remote file into w. progress gets (bytes so far, total or -1).
func (c *Client) Download(ctx context.Context, remotePath string, w io.Writer, progress func(done, total int64)) error {
	resp, err := c.do(ctx, http.MethodGet, c.endpoint("/files", url.Values{"path": {remotePath}}), nil, 0)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiError(resp)
	}
	_, err = io.Copy(w, &progressReader{r: resp.Body, total: resp.ContentLength, fn: progress})
	return err
}

// Upload sends length bytes from r to remotePath. A 409 APIError means the file
// exists and overwrite was false.
func (c *Client) Upload(ctx context.Context, remotePath string, length int64, overwrite bool, r io.Reader, progress func(done int64)) error {
	q := url.Values{"path": {remotePath}}
	if overwrite {
		q.Set("overwrite", "1")
	}
	pr := &progressReader{r: r, total: length, fn: func(d, _ int64) {
		if progress != nil {
			progress(d)
		}
	}}
	resp, err := c.do(ctx, http.MethodPut, c.endpoint("/files", q), pr, length)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return apiError(resp)
	}
	return nil
}

type progressReader struct {
	r     io.Reader
	total int64
	done  int64
	fn    func(done, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	if n > 0 && p.fn != nil {
		p.fn(p.done, p.total)
	}
	return n, err
}
