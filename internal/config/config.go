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

type DaemonConfig struct {
	Port             int       `json:"port"`
	Socket           string    `json:"socket"`
	TLD              string    `json:"tld"`
	Mode             string    `json:"mode"`
	PIDCheckInterval int       `json:"pid_check_interval"`
	TLS              TLSConfig `json:"tls"`
	DNS              DNSConfig `json:"dns"`
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

type DashboardConfig struct {
	Enabled bool   `json:"enabled"`
	Theme   string `json:"theme"`
	View    string `json:"view"` // "list" or "grid"
}

type LoggingConfig struct {
	Level     string `json:"level"`
	File      string `json:"file"`
	MaxSizeMB int    `json:"max_size_mb"`
}

// DefaultConfig returns a Config with production defaults (port 7999, TLD "vibe").
func DefaultConfig() *Config {
	dir := Dir()
	return &Config{
		Daemon: DaemonConfig{
			Port:             7999,
			Socket:           filepath.Join(dir, "vibe.sock"),
			TLD:              "vibe",
			Mode:             "redirect",
			PIDCheckInterval: 5,
			TLS: TLSConfig{
				Enabled:  false,
				Port:     7443,
				CertsDir: filepath.Join(dir, "certs"),
			},
			DNS: DNSConfig{
				Enabled:  false,
				Listen:   "127.0.0.1:53",
				Upstream: "8.8.8.8:53",
			},
		},
		Dashboard: DashboardConfig{
			Enabled: true,
			Theme:   "dark",
			View:    "list",
		},
		Logging: LoggingConfig{
			Level:     "warn",
			File:      filepath.Join(dir, "daemon.log"),
			MaxSizeMB: 10,
		},
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
