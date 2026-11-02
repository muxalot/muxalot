package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		l, c string
		want bool
	}{{"v0.2.10", "0.2.9", true}, {"v0.2.9", "0.2.9", false}, {"v0.3.0", "dev", false}, {"v0.3.0", "0.2.9-dev", false}, {"x", "0.1.0", false}} {
		if got := Newer(c.l, c.c); got != c.want {
			t.Errorf("Newer(%q,%q)=%v", c.l, c.c, got)
		}
	}
}

func TestCheck(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v0.3.0","html_url":"https://x/r","body":"n"}`)
	}))
	defer ts.Close()
	in, err := Check(context.Background(), ts.Client(), ts.URL, "0.2.9")
	if err != nil || !in.Available || in.Latest != "v0.3.0" {
		t.Fatalf("%+v %v", in, err)
	}
	if in, _ = Check(context.Background(), ts.Client(), ts.URL, "dev"); in.Available {
		t.Fatal("dev build must not alert")
	}
}

func TestFetchVerified(t *testing.T) {
	body := []byte("payload")
	s := sha256.Sum256(body)
	for name, sum := range map[string]string{"ok": hex.EncodeToString(s[:]), "bad": hex.EncodeToString(make([]byte, 32))} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v0.3.0/SUMS":
				fmt.Fprintf(w, "%s  a.AppImage\n", sum)
			case "/v0.3.0/a.AppImage":
				_, _ = w.Write(body)
			default:
				http.NotFound(w, r)
			}
		}))
		dst := filepath.Join(t.TempDir(), "a")
		err := fetchVerified(context.Background(), ts.Client(), ts.URL, "v0.3.0", "a.AppImage", "SUMS", dst)
		_, statErr := os.Stat(dst)
		if (name == "ok") != (err == nil) || (name == "ok") != (statErr == nil) {
			t.Errorf("%s: err=%v stat=%v", name, err, statErr)
		}
		ts.Close()
	}
}
