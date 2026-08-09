package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// Store persists Config as JSON on disk. Safe for concurrent use.
type Store struct {
	mu   sync.Mutex
	path string
	cfg  Config
}

// DefaultConfigPath returns the per-user config location,
// e.g. ~/.config/shellyctl/config.json.
func DefaultConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "shellyctl", "config.json"), nil
}

// OpenStore loads the config at path, starting empty when the file does
// not exist yet.
func OpenStore(path string) (*Store, error) {
	s := &Store{path: path}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, &s.cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	return s, nil
}

// Config returns a deep-enough copy of the current configuration for
// read-only use.
func (s *Store) Config() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.clone()
}

// Update applies fn to the configuration under lock and persists the
// result atomically (write temp file, rename).
func (s *Store) Update(fn func(*Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cfg.clone()
	if err := fn(&next); err != nil {
		return err
	}
	if err := writeAtomic(s.path, next); err != nil {
		return err
	}
	s.cfg = next
	return nil
}

func writeAtomic(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (c Config) clone() Config {
	out := c
	out.Devices = append(out.Devices[:0:0], c.Devices...)
	out.Links = make([]Link, len(c.Links))
	for i, l := range c.Links {
		out.Links[i] = l
		if l.TargetParams != nil {
			out.Links[i].TargetParams = make(map[string]any, len(l.TargetParams))
			for k, v := range l.TargetParams {
				out.Links[i].TargetParams[k] = v
			}
		}
	}
	if c.Passwords != nil {
		out.Passwords = make(map[string]string, len(c.Passwords))
		for k, v := range c.Passwords {
			out.Passwords[k] = v
		}
	}
	return out
}
