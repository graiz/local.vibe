package daemon

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func eperm(path string) string {
	return "[Error: EPERM: operation not permitted, scandir '" + path + "'] {\n  code: 'EPERM',\n}\n"
}

func TestDeniedPathInDir(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "app")
	if err := os.Mkdir(app, 0755); err != nil {
		t.Fatal(err)
	}

	if p, ok := deniedPathInDir(eperm(app), dir); !ok || p != app {
		t.Errorf("readable path inside dir: got (%q, %v); want (%q, true)", p, ok, app)
	}
	// Outside the route's dir: the app's own business, not a stale child.
	if _, ok := deniedPathInDir(eperm(t.TempDir()), dir); ok {
		t.Error("path outside dir matched")
	}
	// Gone (or unreadable to the daemon too): a restart wouldn't help.
	if _, ok := deniedPathInDir(eperm(filepath.Join(dir, "missing")), dir); ok {
		t.Error("unopenable path matched")
	}
	// A sibling sharing the prefix string is not inside dir.
	if _, ok := deniedPathInDir(eperm(dir+"-other/app"), dir); ok {
		t.Error("prefix-sharing sibling matched")
	}
	if _, ok := deniedPathInDir("Error: something else\n", dir); ok {
		t.Error("tail without EPERM matched")
	}
}

// A child that lost access to its working tree serves 500 on every request
// while its PID and port read healthy. A page load hitting that signature must
// be diverted to the starting page (restart), not proxied as a bare 500; other
// requests pass through, and a second hit inside the interval doesn't loop.
func TestLostDirAccessDivertsPageLoad(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream-500", http.StatusInternalServerError)
	}))
	defer upstream.Close()

	appDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(appDir, "app"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".vibe"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".vibe", "tasks.log"), []byte(eperm(filepath.Join(appDir, "app"))), 0644); err != nil {
		t.Fatal(err)
	}

	s := testServer(t)
	route := &Route{
		Name: "tasks",
		Type: RouteManaged,
		Port: testPortFromURL(t, upstream.URL),
		// Exits at once: the restart is attempted for real but spawns nothing
		// lasting, and the failed start records a failure we can assert on.
		Cmd:          "exit 3",
		Dir:          appDir,
		RegisteredAt: time.Now(),
	}
	// A live PID, so the route reads as healthy-but-erroring rather than dead.
	// The restart's port-clearing step refuses to signal the daemon's own pid.
	route.SetPID(os.Getpid())
	route.Running.Store(true)
	route.Ready.Store(true)
	s.table.Add(route)

	get := func(headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = "tasks.test"
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		s.routeRequest(w, req)
		return w
	}

	// An XHR/asset 5xx is never replaced with HTML.
	if w := get(map[string]string{"Sec-Fetch-Mode": "cors"}); !strings.Contains(w.Body.String(), "upstream-500") {
		t.Fatalf("non-navigation request was diverted: %d %s", w.Code, w.Body.String())
	}

	w := get(map[string]string{"Sec-Fetch-Mode": "navigate"})
	if strings.Contains(w.Body.String(), "upstream-500") || !strings.Contains(w.Body.String(), "/_api/routes/tasks/repair") {
		t.Fatalf("page load not diverted to the starting page: %d %s", w.Code, w.Body.String())
	}

	// Free the port so the background restart proceeds, then wait for it.
	upstream.Close()
	deadline := time.Now().Add(15 * time.Second)
	for s.isAutoStarting("tasks") && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if s.isAutoStarting("tasks") {
		t.Fatal("restart never finished")
	}
	if route.LoadFailure() == nil {
		t.Error("restart did not attempt a fresh start (no failure recorded from `exit 3`)")
	}
	// The stale child's PID must not linger: markRestartable hides both the
	// restart affordance and the failure while it reads as alive.
	if pid, ok := route.PIDValue(); ok {
		t.Errorf("stale PID %d still on the route after the restart failed", pid)
	}

	// Inside the restart interval the signature no longer triggers — a child
	// that hits the same wall gets its 5xx passed through, not a restart loop.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	if s.lostDirAccess(route, &http.Response{StatusCode: 500, Request: req}) {
		t.Error("second hit inside the interval triggered another restart")
	}
}

// Routes are often registered through a symlinked path (~/dev/app → a
// CloudStorage dir) while Node prints the resolved path in its EPERM error.
// The detector must compare resolved paths or it never fires on the exact
// layout that motivated it. macOS's t.TempDir() lives under /var, a symlink to
// /private/var, so the two spellings differ here without any extra setup.
func TestDeniedPathInDirResolvesSymlinks(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "app")
	if err := os.Mkdir(app, 0755); err != nil {
		t.Fatal(err)
	}
	resolved := realpath(app)
	if resolved == app {
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(dir, link); err != nil {
			t.Skip("cannot create symlink")
		}
		dir = link
	}
	if _, ok := deniedPathInDir(eperm(resolved), dir); !ok {
		t.Errorf("resolved path %q not matched against dir %q", resolved, dir)
	}
}

// The restart slot is claimed only by a restart that actually runs. A second
// page load that arrives while a start is already in flight must not burn the
// cooldown, or a fresh child hitting the same wall is denied its restart.
func TestLostAccessSlotClaimedOnlyWhenRestarting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	appDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(appDir, "app"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".vibe"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".vibe", "busy.log"), []byte(eperm(filepath.Join(appDir, "app"))), 0644); err != nil {
		t.Fatal(err)
	}
	s := testServer(t)
	route := &Route{Name: "busy", Type: RouteManaged, Port: 9, Cmd: "exit 3", Dir: appDir, RegisteredAt: time.Now()}
	route.SetPID(os.Getpid())
	route.Running.Store(true)
	s.table.Add(route)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "busy.test"
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	if !s.lostDirAccess(route, &http.Response{StatusCode: 500, Request: req}) {
		t.Fatal("signature not detected")
	}
	if _, claimed := s.lostAccessRestarts.Load("busy"); claimed {
		t.Fatal("detection alone claimed the restart slot")
	}

	// Another start owns the route: the page load is diverted but no restart
	// runs, so the slot stays free.
	if !s.beginAutoStart("busy") {
		t.Fatal("beginAutoStart")
	}
	defer s.endAutoStart("busy")
	w := httptest.NewRecorder()
	s.restartLostAccess(w, req, route)
	if !strings.Contains(w.Body.String(), "/_api/routes/busy/repair") {
		t.Fatalf("expected the starting page, got %d %s", w.Code, w.Body.String())
	}
	if _, claimed := s.lostAccessRestarts.Load("busy"); claimed {
		t.Error("slot claimed although no restart ran")
	}
}

// A route deregistered and re-registered under the same name must not inherit
// the previous cooldown.
func TestLostAccessCooldownClearedOnRemove(t *testing.T) {
	s := testServer(t)
	s.table.Add(&Route{Name: "gone", Type: RouteManaged, Port: 9, Cmd: "true", RegisteredAt: time.Now()})
	s.lostAccessRestarts.Store("gone", time.Now())
	s.table.Remove("gone")
	if _, has := s.lostAccessRestarts.Load("gone"); has {
		t.Error("cooldown survived route removal")
	}
}
