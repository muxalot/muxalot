package main

import (
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// Every exported method is callable from any script in the webview. Changing this
// list is a security decision: see the comment on Service.
func TestBoundSurface(t *testing.T) {
	want := []string{"Attach", "Detach", "Download", "Forget", "Ls", "Pair", "Paste", "Resize",
		"Send", "Servers", "Sessions", "SetClipboard", "Unlock", "Upload"}
	var got []string
	typ := reflect.TypeOf(&Service{})
	for i := 0; i < typ.NumMethod(); i++ {
		got = append(got, typ.Method(i).Name)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bound surface changed:\n got  %v\n want %v", got, want)
	}
}

func TestPasteBytes(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		bracketed bool
		want      string
	}{
		{"plain", "ls -la", false, "ls -la"},
		{"bracketed", "ls", true, "\x1b[200~ls\x1b[201~"},
		{"crlf and lf become CR", "a\r\nb\nc", false, "a\rb\rc"},
		{"escape stripped", "a\x1b[201~b", true, "\x1b[200~a[201~b\x1b[201~"},
		{"ctrl-c and NUL stripped", "a\x03b\x00c\x7fd", false, "abcd"},
		{"C1 controls stripped", "a\u009bb", false, "ab"},
		{"tab and unicode kept", "\tü€", false, "\tü€"},
		{"empty", "", true, "\x1b[200~\x1b[201~"},
	}
	for _, c := range cases {
		if got := string(pasteBytes(c.in, c.bracketed)); got != c.want {
			t.Errorf("%s: pasteBytes(%q, %v) = %q, want %q", c.name, c.in, c.bracketed, got, c.want)
		}
	}
	big := strings.Repeat("x", 1<<20)
	if n := len(pasteBytes(big, true)); n != len(big)+len("\x1b[200~\x1b[201~") {
		t.Errorf("large paste length = %d", n)
	}
}

func TestFeedCoalescesAndChunks(t *testing.T) {
	var mu sync.Mutex
	var chunks [][]byte
	f := &feed{emit: func(b []byte) { mu.Lock(); chunks = append(chunks, append([]byte(nil), b...)); mu.Unlock() }}
	f.add([]byte("ab"))
	f.add([]byte("cd"))
	f.add(make([]byte, 100<<10))
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	total := 0
	for _, c := range chunks {
		if len(c) > 48<<10 {
			t.Fatalf("chunk of %d bytes exceeds 48 KB", len(c))
		}
		total += len(c)
	}
	if total != 4+100<<10 || string(chunks[0][:4]) != "abcd" {
		t.Fatalf("total = %d, first = %q", total, chunks[0][:4])
	}
}
