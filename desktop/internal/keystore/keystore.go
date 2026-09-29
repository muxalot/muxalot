// Package keystore holds each server's ECDSA P-256 device key, either in the OS
// keyring or in a passphrase-wrapped file. There is no silent fallback between them.
package keystore

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/zalando/go-keyring"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

type Mode string

const (
	ModeKeyring    Mode = "keyring"
	ModePassphrase Mode = "passphrase"
)

var ErrWrongPassphrase = errors.New("wrong passphrase")

const magic = "MXK1"

var idRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Keyring is the slice of an OS keyring we use; a fake stands in for tests.
type Keyring interface {
	Get(id string) (string, error)
	Set(id, secret string) error
	Delete(id string) error
}

// System is the OS keyring (Secret Service, Keychain, Credential Manager).
type System struct{}

const service = "muxalot"

func (System) Get(id string) (string, error) { return keyring.Get(service, id) }
func (System) Set(id, secret string) error   { return keyring.Set(service, id, secret) }
func (System) Delete(id string) error        { return keyring.Delete(service, id) }

type Store struct {
	dir string // passphrase key files live in dir/keys
	kr  Keyring
}

func New(dir string, kr Keyring) *Store { return &Store{dir: dir, kr: kr} }

func (s *Store) file(id string) string { return filepath.Join(s.dir, "keys", id+".key") }

// Create makes a new key for id and returns its base64 X.509 public key for pairing.
// In passphrase mode the file is created 0600 and never overwritten.
func (s *Store) Create(id string, mode Mode, passphrase string) (string, error) {
	if !idRe.MatchString(id) {
		return "", errors.New("invalid key id")
	}
	if mode == ModePassphrase && passphrase == "" {
		return "", errors.New("passphrase required")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", err
	}
	switch mode {
	case ModeKeyring:
		if err := s.kr.Set("key:"+id, base64.StdEncoding.EncodeToString(der)); err != nil {
			return "", fmt.Errorf("OS keyring unavailable (%v); choose passphrase storage instead", err)
		}
	case ModePassphrase:
		if err := s.writeFile(id, der, passphrase); err != nil {
			return "", err
		}
	default:
		return "", errors.New("unknown key storage mode")
	}
	pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(pub), nil
}

func (s *Store) Load(id string, mode Mode, passphrase string) (*ecdsa.PrivateKey, error) {
	if !idRe.MatchString(id) {
		return nil, errors.New("invalid key id")
	}
	var der []byte
	switch mode {
	case ModeKeyring:
		v, err := s.kr.Get("key:" + id)
		if err != nil {
			return nil, fmt.Errorf("OS keyring: %w", err)
		}
		if der, err = base64.StdEncoding.DecodeString(v); err != nil {
			return nil, errors.New("stored key is corrupt")
		}
	case ModePassphrase:
		var err error
		if der, err = s.readFile(id, passphrase); err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("unknown key storage mode")
	}
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, errors.New("stored key is corrupt")
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok || ec.Curve != elliptic.P256() {
		return nil, errors.New("stored key is not P-256")
	}
	return ec, nil
}

// Delete removes the key; a missing key is not an error.
func (s *Store) Delete(id string, mode Mode) {
	if !idRe.MatchString(id) {
		return
	}
	switch mode {
	case ModeKeyring:
		_ = s.kr.Delete("key:" + id)
	case ModePassphrase:
		_ = os.Remove(s.file(id))
	}
}

// file layout: magic | salt(16) | nonce(24) | XChaCha20-Poly1305(pkcs8), key = argon2id(passphrase, salt)
func derive(passphrase string, salt []byte) []byte {
	return argon2.IDKey([]byte(passphrase), salt, 3, 64*1024, 4, chacha20poly1305.KeySize)
}

func (s *Store) writeFile(id string, der []byte, passphrase string) error {
	if err := os.MkdirAll(filepath.Dir(s.file(id)), 0o700); err != nil {
		return err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	aead, err := chacha20poly1305.NewX(derive(passphrase, salt))
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	out := append([]byte(magic), salt...)
	out = append(out, nonce...)
	out = aead.Seal(out, nonce, der, []byte(magic))
	f, err := os.OpenFile(s.file(id), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(out); err != nil {
		f.Close()
		os.Remove(s.file(id))
		return err
	}
	return f.Close()
}

func (s *Store) readFile(id, passphrase string) ([]byte, error) {
	b, err := os.ReadFile(s.file(id))
	if err != nil {
		return nil, err
	}
	const head = len(magic) + 16 + chacha20poly1305.NonceSizeX
	if len(b) < head+chacha20poly1305.Overhead || string(b[:len(magic)]) != magic {
		return nil, errors.New("stored key is corrupt")
	}
	salt := b[len(magic) : len(magic)+16]
	nonce := b[len(magic)+16 : head]
	aead, err := chacha20poly1305.NewX(derive(passphrase, salt))
	if err != nil {
		return nil, err
	}
	der, err := aead.Open(nil, nonce, b[head:], []byte(magic))
	if err != nil {
		return nil, ErrWrongPassphrase // wrong passphrase and tampering are indistinguishable
	}
	return der, nil
}
