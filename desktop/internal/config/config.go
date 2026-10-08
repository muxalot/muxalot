// Package config persists the list of paired servers as JSON (no secrets).
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type Server struct {
	ID       string `json:"id"` // local id; also the key alias
	Name     string `json:"name"`
	URL      string `json:"url"`
	DeviceID string `json:"deviceId"`
	KeyMode  string `json:"keyMode"`
}

type Store struct {
	mu   sync.Mutex
	dir  string
	path string
}

// Dir returns the per-user muxalot config directory (not created).
func Dir() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "muxalot"), nil
}

func New(dir string) *Store { return &Store{dir: dir, path: filepath.Join(dir, "servers.json")} }

// NewID returns a random local server id.
func NewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Load returns the saved servers; a missing or corrupt file yields an empty list.
func (s *Store) Load() []Server {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

func (s *Store) load() []Server {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return nil
	}
	var out []Server
	if json.Unmarshal(b, &out) != nil {
		return nil
	}
	return out
}

func (s *Store) save(list []Server) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, "servers-*.tmp") // CreateTemp is 0600
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

func (s *Store) Add(srv Server) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.save(append(s.load(), srv))
}

func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keep []Server
	for _, x := range s.load() {
		if x.ID != id {
			keep = append(keep, x)
		}
	}
	return s.save(keep)
}

// ---------- tab chip colors (client-side decoration only) ----------

func (s *Store) tabColorsPath() string { return filepath.Join(s.dir, "tabcolors.json") }

// TabColors returns the persisted tab colors: server id -> session -> color spec.
func (s *Store) TabColors() map[string]map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tabColors()
}

func (s *Store) tabColors() map[string]map[string]string {
	b, err := os.ReadFile(s.tabColorsPath())
	if err != nil {
		return map[string]map[string]string{}
	}
	var out map[string]map[string]string
	if json.Unmarshal(b, &out) != nil {
		return map[string]map[string]string{}
	}
	return out
}

// SetTabColor records a tab's chip color; an empty color clears it.
func (s *Store) SetTabColor(serverID, session, color string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := s.tabColors()
	if color == "" {
		if sess := all[serverID]; sess != nil {
			delete(sess, session)
			if len(sess) == 0 {
				delete(all, serverID)
			}
		}
	} else {
		if all[serverID] == nil {
			all[serverID] = map[string]string{}
		}
		all[serverID][session] = color
	}
	b, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.tabColorsPath(), b, 0o600)
}
