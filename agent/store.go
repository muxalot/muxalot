package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	pairCodeTTL  = 10 * time.Minute
	lastSeenStep = 5 * time.Minute
)

type Device struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	PubKey   string    `json:"pubkey"` // base64 (std) X.509 SubjectPublicKeyInfo, ECDSA P-256
	Created  time.Time `json:"created"`
	LastSeen time.Time `json:"last_seen"`
}

type PairCode struct {
	Hash    string    `json:"hash"` // hex sha256 of normalized code
	Expires time.Time `json:"expires"`
}

type storeData struct {
	Devices []Device   `json:"devices"`
	Codes   []PairCode `json:"codes"`
}

// Store persists device token hashes and pending pairing codes in a JSON
// file. State is re-read from disk on every operation so the `pair`, `revoke`
// CLI commands (separate processes) take effect on a running server. Writes
// take an exclusive flock so concurrent processes can't lose updates.
type Store struct {
	mu   sync.Mutex
	path string

	// Lookup cache, valid while state.json is the same file with the same mtime.
	// Lets requests for unknown or already-seen devices skip the file lock and parse.
	cacheInfo os.FileInfo
	cache     map[string]cachedDev
}

type cachedDev struct {
	dev Device
	pk  *ecdsa.PublicKey
}

// lockState takes the exclusive file lock for the duration of a state
// mutation (load-modify-save), coordinating with other muxalot-agent processes.
func (s *Store) lockState() (func(), error) {
	f, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{path: filepath.Join(dir, "state.json")}, nil
}

func (s *Store) load() (*storeData, error) {
	var d storeData
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return &d, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *Store) save(d *storeData) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func hashHex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func normalizeCode(c string) string {
	c = strings.ToUpper(strings.TrimSpace(c))
	c = strings.ReplaceAll(c, "-", "")
	return strings.ReplaceAll(c, " ", "")
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

// NewPairCode creates a one-time pairing code formatted XXXX-XXXX.
func (s *Store) NewPairCode() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lockState()
	if err != nil {
		return "", err
	}
	defer unlock()
	d, err := s.load()
	if err != nil {
		return "", err
	}
	enc := base32.StdEncoding.WithPadding(base32.NoPadding)
	raw := enc.EncodeToString(randomBytes(8))[:8] // 40 bits
	now := time.Now()
	live := d.Codes[:0]
	for _, c := range d.Codes {
		if c.Expires.After(now) {
			live = append(live, c)
		}
	}
	d.Codes = append(live, PairCode{Hash: hashHex(raw), Expires: now.Add(pairCodeTTL)})
	if err := s.save(d); err != nil {
		return "", err
	}
	return raw[:4] + "-" + raw[4:], nil
}

// ErrBadKey wraps every ParsePubKey failure.
var ErrBadKey = errors.New("public key")

var ErrBadCode = errors.New("invalid or expired pairing code")

// ParsePubKey validates a base64 X.509 SubjectPublicKeyInfo ECDSA P-256 key.
func ParsePubKey(b64 string) (*ecdsa.PublicKey, error) {
	der, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return nil, fmt.Errorf("%w is not valid base64", ErrBadKey)
	}
	k, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("%w is not valid X.509 SPKI", ErrBadKey)
	}
	ek, ok := k.(*ecdsa.PublicKey)
	if !ok || ek.Curve != elliptic.P256() {
		return nil, fmt.Errorf("%w must be ECDSA P-256", ErrBadKey)
	}
	return ek, nil
}

// Redeem exchanges a valid one-time code for registration of a device's
// public key. No secret is ever issued: the device keeps its private key.
func (s *Store) Redeem(code, name, pubkey string) (id string, err error) {
	if _, err := ParsePubKey(pubkey); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lockState()
	if err != nil {
		return "", err
	}
	defer unlock()
	d, err := s.load()
	if err != nil {
		return "", err
	}
	h := hashHex(normalizeCode(code))
	now := time.Now()
	found := -1
	for i, c := range d.Codes {
		if subtle.ConstantTimeCompare([]byte(c.Hash), []byte(h)) == 1 && c.Expires.After(now) {
			found = i
		}
	}
	if found < 0 {
		return "", ErrBadCode
	}
	d.Codes = append(d.Codes[:found], d.Codes[found+1:]...)
	id = hex.EncodeToString(randomBytes(4))
	d.Devices = addDevice(d.Devices, id, name, pubkey, now)
	return id, s.save(d)
}

func addDevice(devs []Device, id, name, pubkey string, now time.Time) []Device {
	if name = strings.TrimSpace(name); name == "" {
		name = "device-" + id
	}
	if len(name) > 64 {
		name = name[:64]
	}
	return append(devs, Device{ID: id, Name: name, PubKey: strings.TrimSpace(pubkey), Created: now, LastSeen: now})
}

// AddKey registers a public key directly (admin path, like authorized_keys).
func (s *Store) AddKey(name, pubkey string) (string, error) {
	if _, err := ParsePubKey(pubkey); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lockState()
	if err != nil {
		return "", err
	}
	defer unlock()
	d, err := s.load()
	if err != nil {
		return "", err
	}
	id := hex.EncodeToString(randomBytes(4))
	d.Devices = addDevice(d.Devices, id, name, pubkey, time.Now())
	return id, s.save(d)
}

// cacheFresh reports whether the cache still matches state.json on disk.
func (s *Store) cacheFresh() bool {
	fi, err := os.Stat(s.path)
	return err == nil && s.cacheInfo != nil && os.SameFile(fi, s.cacheInfo) && fi.ModTime().Equal(s.cacheInfo.ModTime())
}

// refill rebuilds the cache from d. Call it holding the state lock, so the file can't change under it.
func (s *Store) refill(d *storeData) {
	fi, err := os.Stat(s.path)
	if err != nil {
		s.cacheInfo, s.cache = nil, nil
		return
	}
	c := make(map[string]cachedDev, len(d.Devices))
	for _, dev := range d.Devices {
		if pk, err := ParsePubKey(dev.PubKey); err == nil {
			c[dev.ID] = cachedDev{dev, pk}
		}
	}
	s.cacheInfo, s.cache = fi, c
}

// Lookup returns the device with the given id and its parsed public key.
func (s *Store) Lookup(id string) (*Device, *ecdsa.PublicKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cacheFresh() {
		e, ok := s.cache[id]
		if !ok {
			return nil, nil
		}
		if time.Since(e.dev.LastSeen) <= lastSeenStep {
			dev := e.dev
			return &dev, e.pk
		}
	}
	unlock, err := s.lockState()
	if err != nil {
		return nil, nil
	}
	defer unlock()
	d, err := s.load()
	if err != nil {
		return nil, nil
	}
	defer s.refill(d)
	for i := range d.Devices {
		if d.Devices[i].ID == id {
			pk, err := ParsePubKey(d.Devices[i].PubKey)
			if err != nil {
				return nil, nil
			}
			dev := d.Devices[i]
			if time.Since(dev.LastSeen) > lastSeenStep {
				d.Devices[i].LastSeen = time.Now()
				_ = s.save(d)
			}
			return &dev, pk
		}
	}
	return nil, nil
}

func (s *Store) Devices() ([]Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return nil, err
	}
	return d.Devices, nil
}

func (s *Store) Revoke(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lockState()
	if err != nil {
		return false, err
	}
	defer unlock()
	d, err := s.load()
	if err != nil {
		return false, err
	}
	for i := range d.Devices {
		if d.Devices[i].ID == id {
			d.Devices = append(d.Devices[:i], d.Devices[i+1:]...)
			return true, s.save(d)
		}
	}
	return false, nil
}
