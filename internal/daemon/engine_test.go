package daemon

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Re-registering a stopped route (vibe start on an app that already has a
// route) replaces the table entry before the spawn. When the spawn then
// fails the OLD registration must come back — not vanish from the table
// while routes.json still carries it.
func TestRegisterFailureRestoresExistingRoute(t *testing.T) {
	s := testServer(t)
	s.ConfigDir = t.TempDir()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	// The port reads as held by a foreign process that killPort can't clear,
	// so the preflight reports a conflict.
	prevFind, prevKill := findPortHoldersFn, terminateProcessFn
	findPortHoldersFn = func(int) []int { return []int{2147483646} }
	terminateProcessFn = func(int) error { return nil }
	defer func() { findPortHoldersFn, terminateProcessFn = prevFind, prevKill }()

	oldDir := t.TempDir()
	s.table.Add(&Route{Name: "app", Type: RouteManaged, Port: port, Cmd: "sleep 30", Dir: oldDir, RegisteredAt: time.Now()})

	body, _ := json.Marshal(map[string]any{"name": "app", "port": port, "cmd": "sleep 30", "dir": t.TempDir()})
	req := httptest.NewRequest(http.MethodPost, "/_api/routes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.apiHandler(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("register over a held port = %d; want 409 (%s)", w.Code, w.Body.String())
	}
	r, ok := s.table.Get("app")
	if !ok {
		t.Fatal("failed re-register removed the existing route")
	}
	if r.Dir != oldDir {
		t.Errorf("route dir = %q; want the original registration %q", r.Dir, oldDir)
	}
}

// A PID-tracked registration for a process that is already gone is an error,
// not an ok:true for a route that the add hook removes a microsecond later.
func TestRegisterRejectsDeadPID(t *testing.T) {
	s := testServer(t)
	s.ConfigDir = t.TempDir()
	body := `{"name":"ghost","port":4100,"pid":2147483646}`
	req := httptest.NewRequest(http.MethodPost, "/_api/routes", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.apiHandler(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("register dead pid = %d; want 400 (%s)", w.Code, w.Body.String())
	}
}

// Each start's readiness poller is bound to the pid it was started for. A
// stop→start inside the 30s window leaves the first poller running, and its
// "never bound the port" verdict must not land on the healthy new child.
func TestWaitForReadyIgnoresSupersededPID(t *testing.T) {
	s := testServer(t)
	s.ReadyTimeout = 300 * time.Millisecond
	route := &Route{Name: "super", Type: RouteManaged, Port: 9, Cmd: "true", RegisteredAt: time.Now()}
	route.SetPID(os.Getpid())
	route.Running.Store(true)
	s.table.Add(route)

	done := make(chan struct{})
	go func() { s.waitForReady(route, os.Getpid()); close(done) }()
	time.Sleep(50 * time.Millisecond)
	route.SetPID(1) // a restart handed the route to another process
	<-done
	if f := route.LoadFailure(); f != nil {
		t.Errorf("superseded poller recorded a failure on the new child: %+v", f)
	}
}

// startManagedNow is the one spawn engine, so it is where vibe.json edits
// take effect — for CLI starts, on-demand recovery, and lost-access restarts
// alike.
func TestStartManagedNowSyncsVibeJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	s := testServer(t)
	s.ConfigDir = t.TempDir()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "vibe.json"), []byte(`{"name":"sync","cmd":"exit 7"}`), 0644); err != nil {
		t.Fatal(err)
	}
	route := &Route{Name: "sync", Type: RouteManaged, Port: 0, Cmd: "exit 3", Dir: dir, RegisteredAt: time.Now()}
	s.table.Add(route)
	err := s.startManagedNow(route)
	if err == nil || !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("start did not run the vibe.json cmd: %v", err)
	}
}

// A proxy error against a child the daemon still tracks as alive is a port
// flap (an HMR full reload closes the socket for a moment), not a death.
// Recovery must not flip the route to stopped — with any stale Failure on
// the route that left a healthy app on the start page forever.
func TestRecoverKeepsTrackedChildOnFlap(t *testing.T) {
	s := testServer(t)
	s.ConfigDir = t.TempDir()
	route := &Route{Name: "flap", Type: RouteManaged, Port: 9, Cmd: "true", RegisteredAt: time.Now()}
	route.SetPID(os.Getpid())
	route.Running.Store(true)
	route.SetFailure(&Failure{Message: "Server started but never bound port 9 within 30s."})
	s.procs.Adopt("flap", os.Getpid())
	s.table.Add(route)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "flap.test"
	w := httptest.NewRecorder()
	if !s.recoverManagedRoute(w, req, route) {
		t.Fatal("expected a page to be served")
	}
	if !route.Running.Load() {
		t.Error("tracked child was flipped to not-running on a transient proxy error")
	}
	if !strings.Contains(w.Body.String(), "/_api/routes/flap/repair") {
		t.Errorf("expected the reconnecting page, got: %s", w.Body.String())
	}
}

// A route whose port answers is healthy; a Failure recorded earlier (a slow
// first compile outlasting the readiness window) is stale and must be
// cleared on the local proxy path as it already is on the peer path.
func TestHealthyProxyClearsStaleFailure(t *testing.T) {
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) }))
	defer app.Close()
	s := testServer(t)
	route := &Route{Name: "slow", Type: RouteManaged, Port: portOf(t, app.URL), Cmd: "true", RegisteredAt: time.Now()}
	route.SetPID(os.Getpid())
	route.Running.Store(true)
	route.Ready.Store(true)
	route.SetFailure(&Failure{Message: "never bound"})
	s.table.Add(route)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "slow.test"
	w := httptest.NewRecorder()
	s.routeRequest(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("proxy = %d", w.Code)
	}
	if route.LoadFailure() != nil {
		t.Error("stale failure survived a healthy proxy round-trip")
	}
}

// A routes.json the daemon can't parse is moved aside, not silently
// overwritten by the next save with an empty table.
func TestLoadStickyRoutesQuarantinesMalformedFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "routes.json"), []byte(`{"sticky_routes":{"a":{"port":1,},}}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := loadStickyRoutes(NewRouteTable(), dir); err == nil {
		t.Fatal("malformed routes.json loaded without error")
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	quarantined := false
	for _, n := range names {
		if strings.HasPrefix(n, "routes.json.bad-") {
			quarantined = true
		}
		if n == "routes.json" {
			t.Error("malformed routes.json left in place to be clobbered")
		}
	}
	if !quarantined {
		t.Errorf("no routes.json.bad-* copy kept; dir has %v", names)
	}
}

// Per-route logs are rotated at spawn once they pass the configured size.
func TestLogRotatedAtSpawn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	logDir := filepath.Join(home, ".vibe")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logDir, "rot.log")
	if err := os.WriteFile(logPath, bytes.Repeat([]byte("x"), 4096), 0600); err != nil {
		t.Fatal(err)
	}
	pm := NewProcessManager()
	pm.maxLogBytes = 1024
	_, _ = pm.Start(&Route{Name: "rot", Type: RouteManaged, Cmd: "true", Dir: home, Port: 1})
	if _, err := os.Stat(logPath + ".1"); err != nil {
		t.Errorf("rotated log missing: %v", err)
	}
	if info, err := os.Stat(logPath); err != nil || info.Size() >= 4096 {
		t.Errorf("log not truncated by rotation: size %d err %v", info.Size(), err)
	}
}

// Re-registering a PID-tracked route with the same pid must not spawn a
// second exit watcher (each holds a kqueue/pidfd until the process dies).
func TestTrackedPIDWatcherDeduped(t *testing.T) {
	s := testServer(t)
	s.watchTrackedPID("w", os.Getpid())
	s.watchTrackedPID("w", os.Getpid())
	n := 0
	s.pidWatchers.Range(func(_, _ any) bool { n++; return true })
	if n != 1 {
		t.Errorf("watchers = %d; want 1", n)
	}
}
