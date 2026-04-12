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
}

// loadStickyRoutes restores persisted routes (sticky, managed, bookmark)
// from ~/.vibe/routes.json on daemon startup.
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
		rt := RouteSticky
		if entry.Type != "" {
			rt = entry.Type
		}
		r := &Route{
			Name:         strings.ToLower(name),
			Port:         entry.Port,
			Cmd:          entry.Cmd,
			Dir:          entry.Dir,
			ExternalURL:  entry.ExternalURL,
			RegisteredAt: entry.RegisteredAt,
			Type:         rt,
			IdleTimeout:  entry.IdleTimeout,
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
	return os.WriteFile(filepath.Join(dir, "routes.json"), data, 0644)
}
