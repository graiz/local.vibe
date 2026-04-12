package daemon

import (
	"testing"
	"time"
)

func TestPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()

	// Create a table with various route types
	table := NewRouteTable()
	table.Add(&Route{
		Name:         "web",
		Port:         3000,
		Type:         RouteSticky,
		RegisteredAt: time.Now(),
	})
	table.Add(&Route{
		Name:         "api",
		Port:         8080,
		Cmd:          "go run .",
		Dir:          "/tmp/api",
		Type:         RouteManaged,
		IdleTimeout:  120,
		RegisteredAt: time.Now(),
	})
	table.Add(&Route{
		Name:         "docs",
		ExternalURL:  "https://docs.example.com",
		Type:         RouteBookmark,
		RegisteredAt: time.Now(),
	})
	// PID-tracked routes should NOT be persisted
	table.Add(&Route{
		Name:         "ephemeral",
		Port:         9999,
		Type:         RoutePIDTracked,
		RegisteredAt: time.Now(),
	})

	if err := saveStickyRoutes(table, dir); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Load into a fresh table
	loaded := NewRouteTable()
	if err := loadStickyRoutes(loaded, dir); err != nil {
		t.Fatalf("load: %v", err)
	}

	routes := loaded.List()
	if len(routes) != 3 {
		t.Fatalf("loaded %d routes; want 3 (sticky+managed+bookmark, not pid)", len(routes))
	}

	// Check managed route preserved fields
	api, ok := loaded.Get("api")
	if !ok {
		t.Fatal("api route not loaded")
	}
	if api.Cmd != "go run ." {
		t.Errorf("api.Cmd = %q; want 'go run .'", api.Cmd)
	}
	if api.Dir != "/tmp/api" {
		t.Errorf("api.Dir = %q; want '/tmp/api'", api.Dir)
	}
	if api.IdleTimeout != 120 {
		t.Errorf("api.IdleTimeout = %d; want 120", api.IdleTimeout)
	}
	if api.Type != RouteManaged {
		t.Errorf("api.Type = %s; want managed", api.Type)
	}

	// Check bookmark
	docs, ok := loaded.Get("docs")
	if !ok {
		t.Fatal("docs route not loaded")
	}
	if docs.ExternalURL != "https://docs.example.com" {
		t.Errorf("docs.ExternalURL = %q", docs.ExternalURL)
	}

	// PID-tracked should not be there
	if _, ok := loaded.Get("ephemeral"); ok {
		t.Error("pid-tracked route should not be persisted")
	}
}
