package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Jensen95/shelly-local-comms/internal/shelly"
)

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.json")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Update(func(c *Config) error {
		c.Devices = append(c.Devices, shelly.Device{Addr: "192.168.1.50", Source: "manual"})
		c.Links = append(c.Links, Link{ID: "l1", TargetParams: map[string]any{"id": 0}})
		c.Passwords = map[string]string{"192.168.1.50": "pw"}
		return nil
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	cfg := reopened.Config()
	if len(cfg.Devices) != 1 || cfg.Devices[0].Addr != "192.168.1.50" {
		t.Fatalf("devices not persisted: %+v", cfg.Devices)
	}
	if len(cfg.Links) != 1 || cfg.Passwords["192.168.1.50"] != "pw" {
		t.Fatalf("links/passwords not persisted: %+v", cfg)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config file should be 0600 (holds passwords), got %v", info.Mode().Perm())
	}
}

func TestUpdateErrorLeavesConfigUntouched(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	sentinel := os.ErrInvalid
	err = s.Update(func(c *Config) error {
		c.Devices = append(c.Devices, shelly.Device{Addr: "x"})
		return sentinel
	})
	if err != sentinel {
		t.Fatalf("want sentinel error, got %v", err)
	}
	if len(s.Config().Devices) != 0 {
		t.Fatal("failed update mutated stored config")
	}
}

func TestConfigCloneIsolation(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(c *Config) error {
		c.Links = []Link{{ID: "l1", TargetParams: map[string]any{"on": true}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	snap := s.Config()
	snap.Links[0].TargetParams["on"] = false
	snap.Links[0].ID = "mutated"
	if got := s.Config().Links[0]; got.ID != "l1" || got.TargetParams["on"] != true {
		t.Fatalf("snapshot mutation leaked into store: %+v", got)
	}
}
