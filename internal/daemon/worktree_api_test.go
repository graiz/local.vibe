//go:build !windows

// Worktree registration tests spawn a real managed child (sleep) via the
// ProcessManager, so they're gated to non-Windows like api_test.go.

package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func postRegister(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/routes", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleRegister(w, req)
	return w
}

func TestRegisterWorktreeRejectsBadShapes(t *testing.T) {
	s := testServer()
	s.ConfigDir = t.TempDir()
	cases := []struct {
		body string
		want int
	}{
		{`{"name":"wt.app","url":"https://example.com"}`, http.StatusBadRequest},              // bookmark
		{`{"name":"wt.app","port":3000}`, http.StatusBadRequest},                              // no cmd
		{`{"name":"wt.app","cmd":"sleep 9","oauth_callback_port":8123}`, http.StatusBadRequest}, // oauth bridge
		{`{"name":"a.b.c","cmd":"sleep 9"}`, http.StatusBadRequest},                           // nesting
		{`{"name":"wt.local","cmd":"sleep 9"}`, http.StatusBadRequest},                        // reserved
	}
	for _, c := range cases {
		if w := postRegister(t, s, c.body); w.Code != c.want {
			t.Errorf("register %s = %d; want %d (body: %s)", c.body, w.Code, c.want, w.Body.String())
		}
	}
}

func TestRegisterWorktreeOverrides(t *testing.T) {
	s := testServer()
	s.ConfigDir = t.TempDir()

	// Port 3200 is what the copied vibe.json would claim; it must be ignored.
	// reserve_ports keeps its name but must get a fresh value.
	w := postRegister(t, s, `{"name":"feat.app","cmd":"sleep 30","port":3200,"dir":"`+t.TempDir()+`","reserve_ports":{"server":3201}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("register = %d: %s", w.Code, w.Body.String())
	}
	t.Cleanup(func() { _ = s.procs.Stop("feat.app") })

	var resp struct {
		Port int    `json:"port"`
		URL  string `json:"url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	route, ok := s.table.Get("feat.app")
	if !ok {
		t.Fatal("route missing after register")
	}
	if route.Parent != "app" {
		t.Errorf("Parent = %q; want \"app\"", route.Parent)
	}
	if route.Port == 3200 || route.Port == 0 {
		t.Errorf("Port = %d; want a fresh auto-assigned port, not the vibe.json value", route.Port)
	}
	if resp.Port != route.Port {
		t.Errorf("response port %d != route port %d", resp.Port, route.Port)
	}
	if !strings.Contains(resp.URL, "feat.app.test") {
		t.Errorf("URL = %q; want host feat.app.test", resp.URL)
	}
	if p := route.ReservePorts["server"]; p == 3201 || p == 0 {
		t.Errorf("reserve_ports[server] = %d; want fresh non-zero value", p)
	}
	if route.IdleTimeout != defaultWorktreeIdleMinutes {
		t.Errorf("IdleTimeout = %d; want default %d", route.IdleTimeout, defaultWorktreeIdleMinutes)
	}

	// An explicit idle_timeout wins over the worktree default.
	w2 := postRegister(t, s, `{"name":"feat2.app","cmd":"sleep 30","idle_timeout":5}`)
	if w2.Code != http.StatusOK {
		t.Fatalf("register feat2 = %d: %s", w2.Code, w2.Body.String())
	}
	t.Cleanup(func() { _ = s.procs.Stop("feat2.app") })
	r2, _ := s.table.Get("feat2.app")
	if r2.IdleTimeout != 5 {
		t.Errorf("explicit IdleTimeout = %d; want 5", r2.IdleTimeout)
	}
}

// A stopped app with any worktree — a registered route (running or not) or an
// unregistered one discovered on disk — serves the picker (start page)
// instead of silently auto-starting; with no worktrees at all the existing
// auto-start behavior is untouched.
func TestRecoverPrefersPickerWhenWorktreeExists(t *testing.T) {
	s := testServer()
	s.ConfigDir = t.TempDir()

	main := &Route{Name: "app", Type: RouteManaged, Port: 3800, Cmd: "sleep 30", RegisteredAt: time.Now()}
	s.table.Add(main)
	wt := &Route{Name: "feat.app", Parent: "app", Type: RouteManaged, Port: 3801, Cmd: "sleep 30", Dir: t.TempDir(), RegisteredAt: time.Now()}
	wt.Running.Store(true)
	s.table.Add(wt)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "app.test"
	w := httptest.NewRecorder()
	served := s.recoverManagedRoute(w, req, main)

	if !served {
		t.Fatal("recoverManagedRoute returned served=false; want the picker page served")
	}
	body := w.Body.String()
	if !strings.Contains(body, `<div class="wt-list">`) || !strings.Contains(body, "feat") {
		t.Errorf("expected picker with worktree list; got:\n%s", body)
	}
	if main.Running.Load() {
		t.Errorf("main was auto-started despite a running worktree; want picker instead")
	}

	// A stopped-but-registered worktree still means "worktrees exist" → picker.
	wt.Running.Store(false)
	w2 := httptest.NewRecorder()
	_ = s.recoverManagedRoute(w2, req, main)
	if main.Running.Load() {
		t.Errorf("main auto-started despite a registered (stopped) worktree")
	}

	// No worktrees at all → the auto-start gate reopens (spawn is attempted).
	s.table.Remove("feat.app")
	w3 := httptest.NewRecorder()
	_ = s.recoverManagedRoute(w3, req, main)
	if !main.Running.Load() {
		t.Errorf("main did not auto-start with no worktrees")
	}
	t.Cleanup(func() { _ = s.procs.Stop("app") })
}

// An unregistered on-disk worktree also suppresses auto-start — the magic
// case: an agent created a git worktree but never ran vibe start in it.
func TestRecoverPrefersPickerForDiscoveredWorktree(t *testing.T) {
	s := testServer()
	s.ConfigDir = t.TempDir()
	repo, _ := initGitRepoWithWorktree(t, "feature/magic")

	main := &Route{Name: "app", Type: RouteManaged, Port: 3810, Cmd: "sleep 30", Dir: repo, RegisteredAt: time.Now()}
	s.table.Add(main)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "app.test"
	w := httptest.NewRecorder()
	served := s.recoverManagedRoute(w, req, main)

	if !served {
		t.Fatal("recoverManagedRoute returned served=false; want the picker served")
	}
	if main.Running.Load() {
		t.Errorf("main auto-started despite a discovered on-disk worktree")
	}
	if !strings.Contains(w.Body.String(), "feature-magic") {
		t.Errorf("picker missing discovered worktree feature-magic")
	}
}

// Load-time pruning of a gone worktree must also kill a child that survived
// the daemon restart — dropping the routes.json entry alone would orphan a
// running server nobody tracks.
func TestLoadPruneKillsSurvivingWorktreeChild(t *testing.T) {
	s := testServer()
	cfgDir := t.TempDir()
	s.ConfigDir = cfgDir

	// Spawn a real child in its own process group via the ProcessManager,
	// exactly like a managed route's child.
	dir := t.TempDir() // no .git link → gone under the worktree rule
	wt := &Route{Name: "f.app", Parent: "app", Type: RouteManaged, Port: 3960, Cmd: "sleep 60", Dir: dir, RegisteredAt: time.Now()}
	pid, err := s.procs.Start(wt)
	if err != nil {
		t.Fatalf("start child: %v", err)
	}
	wt.SetPID(pid)
	wt.Running.Store(true)
	table := NewRouteTable()
	table.Add(wt)
	if err := saveStickyRoutes(table, cfgDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.procs.Stop("f.app") }) // backstop if the prune fails

	fresh := NewRouteTable()
	if err := loadStickyRoutes(fresh, cfgDir); err != nil {
		t.Fatal(err)
	}
	if _, ok := fresh.Get("f.app"); ok {
		t.Errorf("gone worktree survived load")
	}
	deadline := time.After(3 * time.Second)
	for processAlive(pid) {
		select {
		case <-deadline:
			t.Fatalf("surviving child pid %d not killed by load prune", pid)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// POST /_api/routes/{app}/worktrees registers a discovered worktree with the
// parent's cmd and starts it.
func TestAdoptWorktreeEndpoint(t *testing.T) {
	s := testServer()
	s.ConfigDir = t.TempDir()
	repo, wtDir := initGitRepoWithWorktree(t, "feature/magic")

	parent := &Route{Name: "app", Type: RouteManaged, Port: 3820, Cmd: "sleep 30", Dir: repo, RegisteredAt: time.Now()}
	s.table.Add(parent)

	req := httptest.NewRequest(http.MethodPost, "/_api/routes/app/worktrees", strings.NewReader(`{"path":"`+wtDir+`"}`))
	req.Host = "local.test"
	w := httptest.NewRecorder()
	s.apiHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("adopt = %d: %s", w.Code, w.Body.String())
	}
	t.Cleanup(func() { _ = s.procs.Stop("feature-magic.app") })

	var resp struct {
		URL  string `json:"url"`
		Port int    `json:"port"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	route, ok := s.table.Get("feature-magic.app")
	if !ok {
		t.Fatal("feature-magic.app not registered")
	}
	if route.Parent != "app" || route.Cmd != "sleep 30" {
		t.Errorf("route = parent %q cmd %q; want parent app, cmd inherited", route.Parent, route.Cmd)
	}
	if route.Port == 0 || resp.Port != route.Port {
		t.Errorf("port = %d (resp %d); want fresh assigned port", route.Port, resp.Port)
	}
	if !route.Running.Load() {
		t.Errorf("adopted worktree not running")
	}
	if !strings.Contains(resp.URL, "feature-magic.app.test") {
		t.Errorf("URL = %q; want feature-magic.app.test host", resp.URL)
	}

	// A path that isn't one of the app's discovered worktrees is rejected.
	req2 := httptest.NewRequest(http.MethodPost, "/_api/routes/app/worktrees", strings.NewReader(`{"path":"`+t.TempDir()+`"}`))
	req2.Host = "local.test"
	w2 := httptest.NewRecorder()
	s.apiHandler(w2, req2)
	if w2.Code != http.StatusNotFound {
		t.Errorf("foreign path adopt = %d; want 404", w2.Code)
	}
}
