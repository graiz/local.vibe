package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
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

// TestLoadStickyRoutesSkipsInvalidNames ensures a hand-edited routes.json
// can't inject path-traversal sequences via the route name field.
func TestLoadStickyRoutesSkipsInvalidNames(t *testing.T) {
	dir := t.TempDir()
	// Construct a routes.json with a malicious name.
	raw := `{"sticky_routes":{"../../../etc/passwd":{"port":3000,"type":"sticky"},"good":{"port":4000,"type":"sticky"}}}`
	if err := os.WriteFile(filepath.Join(dir, "routes.json"), []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	loaded := NewRouteTable()
	if err := loadStickyRoutes(loaded, dir); err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, ok := loaded.Get("../../../etc/passwd"); ok {
		t.Error("invalid route name should be rejected on load")
	}
	if _, ok := loaded.Get("good"); !ok {
		t.Error("valid route name should still load")
	}
}

// TestSaveStickyRoutesAtomic verifies the tmp-then-rename pattern: the
// target file is never missing or half-written, and the tmp file is cleaned
// up on a successful save.
func TestSaveStickyRoutesAtomic(t *testing.T) {
	dir := t.TempDir()
	table := NewRouteTable()
	table.Add(&Route{Name: "a", Port: 3000, Type: RouteSticky, RegisteredAt: time.Now()})
	if err := saveStickyRoutes(table, dir); err != nil {
		t.Fatalf("save: %v", err)
	}
	// The final file must exist and parse as JSON.
	data, err := os.ReadFile(filepath.Join(dir, "routes.json"))
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	var store stickyStore
	if err := json.Unmarshal(data, &store); err != nil {
		t.Fatalf("target JSON invalid: %v", err)
	}
	// No leftover tmp files.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "routes.json" {
			t.Errorf("leftover file in config dir: %s", e.Name())
		}
	}
}
