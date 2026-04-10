package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/localvibe/vibe/internal/config"
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
func loadStickyRoutes(table *RouteTable) error {
	path := filepath.Join(config.Dir(), "routes.json")
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
		table.Add(&Route{
			Name:         strings.ToLower(name),
			Port:         entry.Port,
			Cmd:          entry.Cmd,
			Dir:          entry.Dir,
			ExternalURL:  entry.ExternalURL,
			RegisteredAt: entry.RegisteredAt,
			Type:         rt,
			Healthy:      rt != RouteManaged, // managed routes start unhealthy until launched
			IdleTimeout:  entry.IdleTimeout,
		})
	}
	return nil
}

// saveStickyRoutes writes all persistent routes (sticky, managed, bookmark)
// to ~/.vibe/routes.json. Called after any route change.
func saveStickyRoutes(table *RouteTable) error {
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
	if err := os.MkdirAll(config.Dir(), 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(config.Dir(), "routes.json"), data, 0644)
}
