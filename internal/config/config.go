package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config holds all daemon configuration. Loaded from ~/.vibe/config.json
// with sensible defaults for fields not specified.
type Config struct {
	Daemon    DaemonConfig    `json:"daemon"`
	Dashboard DashboardConfig `json:"dashboard"`
	Logging   LoggingConfig   `json:"logging"`
}

// Default ports. Every place that spells a port — setup's pf/portproxy rules,
// the peer CLI, the client's TCP fallback — reads these, so a change here is
// a change everywhere.
const (
	DefaultPort     = 7999 // daemon HTTP
	DefaultTLSPort  = 7443 // daemon HTTPS
	DefaultPeerPort = 7444 // experimental LAN peer listener
)

type DaemonConfig struct {
	Port   int       `json:"port"`
	Socket string    `json:"socket"`
	TLD    string    `json:"tld"`
	TLS    TLSConfig `json:"tls"`
	DNS    DNSConfig `json:"dns"`
	// Peers configures the EXPERIMENTAL cross-machine peer feature: paired
	// vibe daemons on a LAN can browse each other's routes through an mTLS
	// relay listener on Port. Off by default; an absent key means disabled —
	// a plain bool is safe here because (unlike autostart) the zero value
	// and the safe default agree.
	Peers PeersConfig `json:"peers"`
	// AutoStart controls on-demand recovery of managed routes. When true
	// (default), visiting a stopped managed route's URL spawns its process
	// and shows a "reconnecting" page instead of a manual Start button.
	// Surviving children from a prior daemon are always re-adopted regardless
	// of this flag (adoption never spawns anything). Set false to keep the
	// old click-to-start behavior. Uses a pointer so an omitted key defaults
	// to on rather than Go's zero-value false.
	AutoStart *bool `json:"autostart,omitempty"`
}

// AutoStartEnabled reports whether on-demand managed-route auto-start is on.
// Defaults to true when the config key is absent.
func (d DaemonConfig) AutoStartEnabled() bool {
	return d.AutoStart == nil || *d.AutoStart
}

type TLSConfig struct {
	Enabled  bool   `json:"enabled"`
	Port     int    `json:"port"`
	CertsDir string `json:"certs_dir"`
}

// DNSConfig controls the embedded DNS resolver. Used on platforms that lack
// a native per-domain resolver hook (currently Windows). On macOS the
// /etc/resolver/vibe entry handles routing without us listening on :53, so
// Enabled defaults to false everywhere — `vibe setup` flips it to true on
// Windows.
type DNSConfig struct {
	Enabled  bool   `json:"enabled"`
	Listen   string `json:"listen"`   // default 127.0.0.1:53
	Upstream string `json:"upstream"` // default 8.8.8.8:53
}

// PeersConfig gates the experimental peer subsystem. When Enabled, the daemon
// may start a LAN-facing mTLS listener on Port for paired-peer traffic only —
// the loopback API/dashboard listeners are unaffected either way.
type PeersConfig struct {
	Enabled bool `json:"enabled"`
	Port    int  `json:"port"`
}

type DashboardConfig struct {
	View string `json:"view"` // "list" or "grid"
}

// LoggingConfig controls per-route process logs (~/.vibe/<name>.log).
// MaxSizeMB rotates a log at the next start once it has grown past this.
type LoggingConfig struct {
	MaxSizeMB int `json:"max_size_mb"`
}

// DefaultConfig returns a Config with production defaults (port 7999, TLD "vibe").
func DefaultConfig() *Config {
	dir := Dir()
	autoStart := true
	return &Config{
		Daemon: DaemonConfig{
			Port:      DefaultPort,
			Socket:    filepath.Join(dir, "vibe.sock"),
			TLD:       "vibe",
			AutoStart: &autoStart,
			TLS: TLSConfig{
				Enabled:  false,
				Port:     DefaultTLSPort,
				CertsDir: filepath.Join(dir, "certs"),
			},
			DNS: DNSConfig{
				Enabled:  false,
				Listen:   "127.0.0.1:53",
				Upstream: "8.8.8.8:53",
			},
			Peers: PeersConfig{Enabled: false, Port: DefaultPeerPort},
		},
		Dashboard: DashboardConfig{View: "list"},
		Logging:   LoggingConfig{MaxSizeMB: 10},
	}
}

// Load reads ~/.vibe/config.json and overlays it on defaults.
// Returns defaults if the file doesn't exist.
func Load() (*Config, error) {
	cfg := DefaultConfig()
	path := filepath.Join(Dir(), "config.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Dir returns the vibe config directory (~/.vibe).
func Dir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".vibe")
}
