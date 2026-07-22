package daemon

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseRouteName(t *testing.T) {
	cases := []struct {
		name       string
		wantParent string
		wantErr    bool
	}{
		{"myapp", "", false},
		{"my-app2", "", false},
		{"feature-auth.myapp", "myapp", false},
		{"a.b", "b", false},
		{"UPPER", "", true}, // caller lowercases first; parse itself rejects
		{"-bad", "", true},
		{"wt.-bad", "", true},
		{"bad-.app", "", true},
		{"a.b.c", "", true},
		{"wt.local", "", true},
		{"local.app", "", true},
		{".app", "", true},
		{"wt.", "", true},
	}
	for _, c := range cases {
		parent, err := parseRouteName(c.name)
		if c.wantErr != (err != nil) {
			t.Errorf("parseRouteName(%q) err = %v; wantErr %v", c.name, err, c.wantErr)
			continue
		}
		if parent != c.wantParent {
			t.Errorf("parseRouteName(%q) parent = %q; want %q", c.name, parent, c.wantParent)
		}
	}
}

func TestWorktreeDirGone(t *testing.T) {
	dir := t.TempDir()
	wt := &Route{Name: "f.app", Parent: "app", Type: RouteManaged, Dir: dir}
	if worktreeDirGone(wt) {
		t.Errorf("dir exists; want gone=false")
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if !worktreeDirGone(wt) {
		t.Errorf("dir removed; want gone=true")
	}
	// Non-worktree routes are never "gone", even with a missing Dir.
	main := &Route{Name: "app", Type: RouteManaged, Dir: filepath.Join(dir, "nope")}
	if worktreeDirGone(main) {
		t.Errorf("non-worktree route reported gone")
	}
}

// Persisted worktree routes reload with Parent derived from the dotted name;
// a worktree whose dir vanished while the daemon was down is dropped at load.
func TestPersistenceWorktreeRoundTripAndPrune(t *testing.T) {
	cfgDir := t.TempDir()
	liveDir := t.TempDir()
	goneDir := t.TempDir()

	table := NewRouteTable()
	table.Add(&Route{Name: "app", Type: RouteManaged, Port: 3100, Cmd: "sleep 1", RegisteredAt: time.Now()})
	table.Add(&Route{Name: "live.app", Parent: "app", Type: RouteManaged, Port: 3101, Cmd: "sleep 1", Dir: liveDir, RegisteredAt: time.Now()})
	table.Add(&Route{Name: "gone.app", Parent: "app", Type: RouteManaged, Port: 3102, Cmd: "sleep 1", Dir: goneDir, RegisteredAt: time.Now()})
	if err := saveStickyRoutes(table, cfgDir); err != nil {
		t.Fatal(err)
	}

	if err := os.RemoveAll(goneDir); err != nil {
		t.Fatal(err)
	}
	fresh := NewRouteTable()
	if err := loadStickyRoutes(fresh, cfgDir); err != nil {
		t.Fatal(err)
	}
	if _, ok := fresh.Get("gone.app"); ok {
		t.Errorf("gone.app survived load despite missing dir")
	}
	r, ok := fresh.Get("live.app")
	if !ok {
		t.Fatalf("live.app missing after reload")
	}
	if r.Parent != "app" {
		t.Errorf("reloaded Parent = %q; want \"app\"", r.Parent)
	}
	if _, ok := fresh.Get("app"); !ok {
		t.Errorf("plain route app missing after reload")
	}
}

func TestGoneWorktreeHostRedirectsToParent(t *testing.T) {
	s := testServer() // TLD "test", TLS disabled → scheme http

	parentRoute := &Route{Name: "app", Type: RouteSticky, Port: 3300, RegisteredAt: time.Now()}
	parentRoute.Running.Store(true)
	parentRoute.Ready.Store(true)
	s.table.Add(parentRoute)

	get := func(host string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/some/path", nil)
		req.Host = host
		w := httptest.NewRecorder()
		s.routeRequest(w, req)
		return w
	}

	// Unknown worktree host with a registered parent → 307 to the parent.
	w := get("gone.app.test")
	if w.Code != http.StatusTemporaryRedirect {
		t.Fatalf("code = %d; want 307", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "http://app.test/" {
		t.Errorf("Location = %q; want http://app.test/", loc)
	}

	// Parent known only via a sibling worktree (parent-as-string): still 307.
	sib := &Route{Name: "live.other", Parent: "other", Type: RouteManaged, Port: 3301, Cmd: "sleep 1", RegisteredAt: time.Now()}
	s.table.Add(sib)
	if w := get("gone.other.test"); w.Code != http.StatusTemporaryRedirect {
		t.Errorf("sibling-known parent: code = %d; want 307", w.Code)
	}

	// Entirely unknown parent → falls through to the dashboard (200), no redirect.
	if w := get("gone.nobody.test"); w.Code != http.StatusOK {
		t.Errorf("unknown parent: code = %d; want 200 dashboard", w.Code)
	}
}

func TestRecoverManagedRoutePrunesGoneWorktree(t *testing.T) {
	s := testServer()
	s.ConfigDir = t.TempDir()

	dir := t.TempDir()
	wt := &Route{Name: "f.app", Parent: "app", Type: RouteManaged, Port: 3400, Cmd: "sleep 1", Dir: dir, RegisteredAt: time.Now()}
	s.table.Add(wt)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "f.app.test"
	w := httptest.NewRecorder()
	served := s.recoverManagedRoute(w, req, wt)

	if !served {
		t.Fatal("recoverManagedRoute returned served=false; want a served redirect")
	}
	if w.Code != http.StatusTemporaryRedirect {
		t.Errorf("code = %d; want 307", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "http://app.test/" {
		t.Errorf("Location = %q; want http://app.test/", loc)
	}
	if _, ok := s.table.Get("f.app"); ok {
		t.Errorf("route survived prune; want removed")
	}
}
