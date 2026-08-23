package config

import (
	"encoding/json"
	"testing"
)

func TestAutoStartEnabledDefault(t *testing.T) {
	// Absent key → enabled (managed routes recover on demand by default).
	var d DaemonConfig
	if !d.AutoStartEnabled() {
		t.Errorf("AutoStartEnabled() with nil AutoStart = false; want true (default on)")
	}

	// Default config sets it explicitly on.
	if !DefaultConfig().Daemon.AutoStartEnabled() {
		t.Errorf("DefaultConfig auto-start = false; want true")
	}
}

func TestAutoStartEnabledExplicit(t *testing.T) {
	on, off := true, false
	if !(DaemonConfig{AutoStart: &on}).AutoStartEnabled() {
		t.Errorf("AutoStart=&true reported disabled")
	}
	if (DaemonConfig{AutoStart: &off}).AutoStartEnabled() {
		t.Errorf("AutoStart=&false reported enabled")
	}
}

func TestAutoStartJSONRoundTrip(t *testing.T) {
	// An omitted key must unmarshal to nil (→ default on), not false.
	var d DaemonConfig
	if err := json.Unmarshal([]byte(`{"port":7999}`), &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d.AutoStart != nil {
		t.Errorf("absent autostart unmarshaled to non-nil %v", *d.AutoStart)
	}
	if !d.AutoStartEnabled() {
		t.Errorf("absent autostart not treated as enabled")
	}

	// An explicit false must be honored.
	if err := json.Unmarshal([]byte(`{"autostart":false}`), &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d.AutoStart == nil || *d.AutoStart {
		t.Errorf("explicit autostart=false not honored: %v", d.AutoStart)
	}
	if d.AutoStartEnabled() {
		t.Errorf("autostart=false still reported enabled")
	}
}

func TestPeersConfigDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Daemon.Peers.Enabled {
		t.Fatal("peers must default to disabled")
	}
	if cfg.Daemon.Peers.Port != 7444 {
		t.Fatalf("peers port default = %d, want 7444", cfg.Daemon.Peers.Port)
	}
}

func TestPeersConfigAbsentKeyDisabled(t *testing.T) {
	// A config.json written before this feature existed must not enable it.
	cfg := DefaultConfig()
	if err := json.Unmarshal([]byte(`{"daemon":{"port":7999}}`), cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Daemon.Peers.Enabled {
		t.Fatal("absent daemon.peers key must mean disabled")
	}
	if cfg.Daemon.Peers.Port != 7444 {
		t.Fatalf("absent key must keep default port, got %d", cfg.Daemon.Peers.Port)
	}
}
