package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCSPHeaderAndPolicy(t *testing.T) {
	h := withCSP(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if got := rec.Header().Get("Content-Security-Policy"); got != csp {
		t.Fatalf("CSP header = %q", got)
	}
	// scripts must stay strict: only our own origin, never inline or eval
	var script string
	for _, d := range strings.Split(csp, ";") {
		if d = strings.TrimSpace(d); strings.HasPrefix(d, "script-src") {
			script = d
		}
	}
	if script != "script-src 'self'" {
		t.Errorf("script-src = %q, want only 'self'", script)
	}
	for _, must := range []string{"default-src 'self'", "frame-src 'none'", "base-uri 'none'", "form-action 'none'", "object-src 'none'"} {
		if !strings.Contains(csp, must) {
			t.Errorf("CSP lacks %q", must)
		}
	}
	if strings.Contains(csp, "http:") || strings.Contains(csp, "https:") || strings.Contains(csp, "*") {
		t.Error("CSP must not allow remote origins")
	}
}
