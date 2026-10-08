package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTripAndPerms(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "muxalot")
	s := New(dir)
	if len(s.Load()) != 0 {
		t.Fatal("empty store must load empty")
	}
	a := Server{ID: "a", Name: "one", URL: "https://x", DeviceID: "d1", KeyMode: "keyring"}
	b := Server{ID: "b", Name: "two", URL: "https://y", DeviceID: "d2", KeyMode: "passphrase"}
	if err := s.Add(a); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(b); err != nil {
		t.Fatal(err)
	}
	if got := s.Load(); len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("Load = %+v", got)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, want 0700", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Join(dir, "servers.json")); fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, want 0600", fi.Mode().Perm())
	}
	if err := s.Remove("a"); err != nil {
		t.Fatal(err)
	}
	if got := s.Load(); len(got) != 1 || got[0] != b {
		t.Fatalf("after Remove: %+v", got)
	}
}

func TestCorruptFileIsEmpty(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "servers.json"), []byte("{not json"), 0o600)
	s := New(dir)
	if len(s.Load()) != 0 {
		t.Fatal("corrupt file must load as empty")
	}
	if err := s.Add(Server{ID: "a"}); err != nil {
		t.Fatalf("Add over a corrupt file: %v", err)
	}
}

func TestTabColors(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	if m := s.TabColors(); len(m) != 0 {
		t.Fatalf("fresh store = %v", m)
	}
	if err := s.SetTabColor("s1", "main", "green"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTabColor("s1", "main", "red"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTabColor("s2", "x", ""); err != nil {
		t.Fatalf("clearing an unset color: %v", err)
	}
	r := New(dir)
	if c := r.TabColors()["s1"]["main"]; c != "red" {
		t.Fatalf("reloaded color = %q", c)
	}
	if err := r.SetTabColor("s1", "main", ""); err != nil {
		t.Fatal(err)
	}
	if m := r.TabColors(); len(m["s1"]) != 0 {
		t.Fatalf("after clear = %v", m)
	}
	// a corrupt file is as good as none
	os.WriteFile(filepath.Join(dir, "tabcolors.json"), []byte("{oh no"), 0o600)
	if m := New(dir).TabColors(); len(m) != 0 {
		t.Fatalf("corrupt = %v", m)
	}
}
