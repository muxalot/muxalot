package keystore

import (
	"crypto/x509"
	"encoding/base64"
	"errors"
	"os"
	"testing"
)

type fakeKR struct {
	m   map[string]string
	err error
}

func (f *fakeKR) Get(id string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	v, ok := f.m[id]
	if !ok {
		return "", errors.New("not found")
	}
	return v, nil
}
func (f *fakeKR) Set(id, s string) error {
	if f.err != nil {
		return f.err
	}
	f.m[id] = s
	return nil
}
func (f *fakeKR) Delete(id string) error { delete(f.m, id); return nil }

func newStore(t *testing.T) (*Store, *fakeKR) {
	kr := &fakeKR{m: map[string]string{}}
	return New(t.TempDir(), kr), kr
}

func TestPassphraseRoundTrip(t *testing.T) {
	s, _ := newStore(t)
	pubB64, err := s.Create("a1", ModePassphrase, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(s.file("a1")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode = %v, %v", fi, err)
	}
	if fi, _ := os.Stat(s.dir + "/keys"); fi.Mode().Perm() != 0o700 {
		t.Errorf("keys dir mode = %v, want 0700", fi.Mode().Perm())
	}
	k, err := s.Load("a1", ModePassphrase, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if base64.StdEncoding.EncodeToString(der) != pubB64 {
		t.Error("loaded key does not match the pairing public key")
	}
	if _, err := s.Load("a1", ModePassphrase, "wrong"); !errors.Is(err, ErrWrongPassphrase) {
		t.Errorf("wrong passphrase err = %v", err)
	}
	if _, err := s.Create("a1", ModePassphrase, "x"); err == nil {
		t.Error("must not overwrite an existing key file")
	}
}

func TestPassphraseTamper(t *testing.T) {
	s, _ := newStore(t)
	s.Create("a1", ModePassphrase, "pw")
	b, _ := os.ReadFile(s.file("a1"))
	b[len(b)-1] ^= 1
	os.WriteFile(s.file("a1"), b, 0o600)
	if _, err := s.Load("a1", ModePassphrase, "pw"); err == nil {
		t.Error("tampered file must not load")
	}
	os.WriteFile(s.file("a1"), []byte("junk"), 0o600)
	if _, err := s.Load("a1", ModePassphrase, "pw"); err == nil {
		t.Error("short file must not load")
	}
}

func TestKeyringModeAndNoFallback(t *testing.T) {
	s, kr := newStore(t)
	pubB64, err := s.Create("k1", ModeKeyring, "")
	if err != nil {
		t.Fatal(err)
	}
	k, err := s.Load("k1", ModeKeyring, "")
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if base64.StdEncoding.EncodeToString(der) != pubB64 {
		t.Error("keyring key does not match pairing public key")
	}
	s.Delete("k1", ModeKeyring)
	if _, err := s.Load("k1", ModeKeyring, ""); err == nil {
		t.Error("deleted key must not load")
	}

	kr.err = errors.New("no secret service")
	if _, err := s.Create("k2", ModeKeyring, ""); err == nil {
		t.Fatal("Create must fail when the keyring is unavailable")
	}
	if _, err := os.Stat(s.file("k2")); err == nil {
		t.Fatal("must not fall back to a key file")
	}
}

func TestRejectsBadInput(t *testing.T) {
	s, _ := newStore(t)
	for _, id := range []string{"", "../x", "a/b", "a b"} {
		if _, err := s.Create(id, ModePassphrase, "pw"); err == nil {
			t.Errorf("Create(%q) must fail", id)
		}
	}
	if _, err := s.Create("ok", ModePassphrase, ""); err == nil {
		t.Error("empty passphrase must be refused")
	}
	if _, err := s.Create("ok", "bogus", "pw"); err == nil {
		t.Error("unknown mode must be refused")
	}
}
