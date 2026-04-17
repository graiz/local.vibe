package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type stickyStore struct {
	StickyRoutes map[string]stickyEntry `json:"sticky_routes"`
}

type stickyEntry struct {
	Port         int       `json:"port,omitempty"`
	Cmd          string    `json:"cmd,omitempty"`
	Dir          string    `json:"dir,omitempty"`
	ExternalURL  string    `json:"external_url,omitempty"`
	Type         RouteType `json:"type,omitempty"`
	RegisteredAt time.Time `json:"registered_at"`
	IdleTimeout  int       `json:"idle_timeout,omitempty"`
	Icon         string    `json:"icon,omitempty"`
	AutoIcon     string    `json:"auto_icon,omitempty"`
}

// loadStickyRoutes restores persisted routes (sticky, managed, bookmark)
// from ~/.vibe/routes.json on daemon startup. Entries whose names fail
// the validName regex are skipped — a hand-edited routes.json must not
// be able to inject `../` sequences that later flow into log or cert paths.
func loadStickyRoutes(table *RouteTable, dir string) error {
	path := filepath.Join(dir, "routes.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var store stickyStore
	if err := json.Unmarshal(data, &store); err != nil {
		return err
	}
	for name, entry := range store.StickyRoutes {
		lower := strings.ToLower(name)
		if !validName.MatchString(lower) || lower == "local" {
			continue
		}
		rt := RouteSticky
		if entry.Type != "" {
			rt = entry.Type
		}
		r := &Route{
			Name:         lower,
			Port:         entry.Port,
			Cmd:          entry.Cmd,
			Dir:          entry.Dir,
			ExternalURL:  entry.ExternalURL,
			RegisteredAt: entry.RegisteredAt,
			Type:         rt,
			IdleTimeout:  entry.IdleTimeout,
			Icon:         entry.Icon,
			AutoIcon:     entry.AutoIcon,
		}
		// Managed routes start not running until launched; others are assumed ready.
		r.Running.Store(rt != RouteManaged)
		r.Ready.Store(rt != RouteManaged)
		table.Add(r)
	}
	return nil
}

// saveStickyRoutes writes all persistent routes (sticky, managed, bookmark)
// to ~/.vibe/routes.json. Called after any route change.
func saveStickyRoutes(table *RouteTable, dir string) error {
	store := stickyStore{StickyRoutes: make(map[string]stickyEntry)}
	for _, r := range table.List() {
		if r.Type == RouteSticky || r.Type == RouteManaged || r.Type == RouteBookmark {
			store.StickyRoutes[r.Name] = stickyEntry{
				Port:         r.Port,
				Cmd:          r.Cmd,
				Dir:          r.Dir,
				ExternalURL:  r.ExternalURL,
				Type:         r.Type,
				RegisteredAt: r.RegisteredAt,
				IdleTimeout:  r.IdleTimeout,
				Icon:         r.Icon,
				AutoIcon:     r.AutoIcon,
			}
		}
	}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	// Write-then-rename so a crash mid-write can't leave routes.json
	// truncated or half-written. The tmp file lives in the same dir so
	// rename stays on one filesystem and is atomic.
	target := filepath.Join(dir, "routes.json")
	tmp, err := os.CreateTemp(dir, "routes.json.*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}
