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
	Port             int    `json:"port"`
	Socket           string `json:"socket"`
	TLD              string `json:"tld"`
	Mode             string `json:"mode"`
	PIDCheckInterval int    `json:"pid_check_interval"`
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
