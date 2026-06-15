//go:build !windows

// Tests for on-demand managed-route recovery (adopt / auto-spawn / start page).
// Uses Setpgid + nc to spawn real listening process groups, so it's gated to
// non-Windows like api_test.go.

package daemon

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// freeTCPPort returns a port that was momentarily free.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// waitPortReady blocks until the given port accepts connections (or fails).
func waitPortReady(t *testing.T, s *Server, port int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if s.isPortReady(port) {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("port %d never became ready", port)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// startManagedListener spawns `nc -k -l <port>` as a managed child via the
// real ProcessManager and returns the route plus the process-group leader PID,
// simulating a managed route that is up and listening. The -k keeps nc
// listening after the readiness probe connects (plain `nc -l` serves a single
// connection then exits, which would race the adoption check).
func startManagedListener(t *testing.T, s *Server, name string, port int) (*Route, int) {
	t.Helper()
	route := &Route{
		Name:         name,
		Type:         RouteManaged,
		Port:         port,
		Cmd:          fmt.Sprintf("nc -k -l %d", port),
		Dir:          t.TempDir(),
		RegisteredAt: time.Now(),
	}
	pid, err := s.procs.Start(route)
	if err != nil {
		t.Fatalf("start listener: %v", err)
	}
	t.Cleanup(func() { _ = s.procs.Stop(name) })
	route.SetPID(pid)
	route.Running.Store(true)
	route.Ready.Store(true)
	waitPortReady(t, s, port)
	return route, pid
}

func TestAdoptOrphanReattachesProcessGroup(t *testing.T) {
	s := testServer()
	s.ConfigDir = t.TempDir()
	port := freeTCPPort(t)
	_, pid := startManagedListener(t, s, "adopt", port)

	// Simulate a daemon restart: a fresh route carrying only the persisted PID
	// (Running=false). adoptOrphan should re-attach via the surviving process
	// group regardless of the registered port.
	fresh := &Route{Name: "adopt", Type: RouteManaged, Port: port, Cmd: "nc", RegisteredAt: time.Now()}
	fresh.SetPID(pid)

	gotPID, gotPort, ok := s.adoptOrphan(fresh)
	if !ok {
		t.Fatalf("adoptOrphan returned ok=false; expected to re-adopt surviving child")
	}
	if gotPID != pid {
		t.Errorf("adopted pid = %d; want %d", gotPID, pid)
	}
	// Adoption is anchored to the route's registered port: it confirms a group
	// member is serving exactly that port, and never substitutes some other
	// listener the login shell ($SHELL -lic) may have spawned into the group.
	if gotPort != port {
		t.Errorf("adopted port = %d; want the registered port %d", gotPort, port)
	}
}

// TestAdoptOrphanIgnoresForeignGroupListener guards the regression where the
// process group contained an unrelated listener (e.g. one a login shell
// spawned): adoption must not succeed when the route's *registered* port isn't
// the one being served, even if the group is listening on some other port.
func TestAdoptOrphanIgnoresForeignGroupListener(t *testing.T) {
	s := testServer()
	s.ConfigDir = t.TempDir()
	port := freeTCPPort(t)
	_, pid := startManagedListener(t, s, "anchor", port)

	// Registered port is one the group is NOT serving → no adoption, even
	// though the live group is listening on `port`.
	other := freeTCPPort(t)
	fresh := &Route{Name: "anchor", Type: RouteManaged, Port: other, RegisteredAt: time.Now()}
	fresh.SetPID(pid)

	if _, _, ok := s.adoptOrphan(fresh); ok {
		t.Errorf("adopted on a port the route never registered (%d); want no adoption", other)
	}
}

func TestAdoptOrphanRejectsDeadOrMissingPID(t *testing.T) {
	s := testServer()

	// No PID → nothing to adopt.
	none := &Route{Name: "n", Type: RouteManaged, Port: 12345, RegisteredAt: time.Now()}
	if _, _, ok := s.adoptOrphan(none); ok {
		t.Errorf("adoptOrphan with no PID returned ok=true")
	}

	// A dead PID (very high, not alive) → no adoption.
	dead := &Route{Name: "d", Type: RouteManaged, Port: 12345, RegisteredAt: time.Now()}
	dead.SetPID(2147483646)
	if _, _, ok := s.adoptOrphan(dead); ok {
		t.Errorf("adoptOrphan with dead PID returned ok=true")
	}
}

func TestRecoverManagedRouteAdoptsSurvivingChild(t *testing.T) {
	s := testServer()
	s.ConfigDir = t.TempDir()
	port := freeTCPPort(t)
	_, pid := startManagedListener(t, s, "adopt2", port)

	// Fresh daemon view: route present with persisted PID but marked stopped.
	fresh := &Route{Name: "adopt2", Type: RouteManaged, Port: port, Cmd: fmt.Sprintf("nc -l %d", port), RegisteredAt: time.Now()}
	fresh.SetPID(pid)
	fresh.Running.Store(false)
	fresh.Ready.Store(false)
	s.table.Add(fresh)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	served := s.recoverManagedRoute(w, req, fresh)

	if served {
		t.Fatalf("recoverManagedRoute served a page; expected adoption (served=false → proceed to proxy)")
	}
	if !fresh.Running.Load() {
		t.Errorf("expected adopted route to be marked Running")
	}
	if !fresh.Ready.Load() {
		t.Errorf("expected adopted route to be marked Ready")
	}
}

// TestAdoptedRouteIsStoppable guards the leak where a re-adopted child could
// not be killed: adoption sets route.PID but the process was never registered
// with ProcessManager, so Stop/Deregister left it running and holding its port.
func TestAdoptedRouteIsStoppable(t *testing.T) {
	s := testServer()
	s.ConfigDir = t.TempDir()
	port := freeTCPPort(t)
	_, pid := startManagedListener(t, s, "stopme", port)

	// Simulate the post-restart adoption bookkeeping.
	s.procs.Adopt("stopme", pid)
	if !s.procs.IsRunning("stopme") {
		t.Fatalf("adopted route not reported running")
	}
	if !s.procs.OwnsPID(pid) {
		t.Errorf("OwnsPID(%d) = false for adopted route", pid)
	}

	if err := s.procs.Stop("stopme"); err != nil {
		t.Fatalf("Stop adopted route: %v", err)
	}
	// The process group should be gone shortly after SIGTERM.
	deadline := time.After(3 * time.Second)
	for processAlive(pid) {
		select {
		case <-deadline:
			t.Fatalf("adopted process %d still alive after Stop", pid)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func TestRecoverManagedRouteAutoStartDisabledShowsStartPage(t *testing.T) {
	s := testServer()
	off := false
	s.cfg.Daemon.AutoStart = &off

	port := freeTCPPort(t) // nothing listening here
	route := &Route{Name: "noauto", Type: RouteManaged, Port: port, Cmd: "nc -l 1", RegisteredAt: time.Now()}
	s.table.Add(route)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	served := s.recoverManagedRoute(w, req, route)

	if !served {
		t.Fatalf("expected start page to be served")
	}
	if route.Running.Load() {
		t.Errorf("auto-start disabled but route was started")
	}
	if !strings.Contains(w.Body.String(), "/_api/routes/noauto/start") {
		t.Errorf("expected manual start page; body: %s", w.Body.String())
	}
}

func TestRecoverManagedRouteCrashLoopShowsStartPage(t *testing.T) {
	s := testServer()
	port := freeTCPPort(t)
	route := &Route{Name: "crashy", Type: RouteManaged, Port: port, Cmd: "nc -l 1", RegisteredAt: time.Now()}
	route.SetFailure(&Failure{Message: "process exited immediately"})
	s.table.Add(route)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	served := s.recoverManagedRoute(w, req, route)

	if !served {
		t.Fatalf("expected start page to be served")
	}
	if route.Running.Load() {
		t.Errorf("route with a recorded failure was respawned (crash-loop guard failed)")
	}
	if !strings.Contains(w.Body.String(), "/_api/routes/crashy/start") {
		t.Errorf("expected start page; body: %s", w.Body.String())
	}
}

func TestRecoverManagedRouteAutoSpawnsOnFreePort(t *testing.T) {
	s := testServer()
	s.ConfigDir = t.TempDir()
	port := freeTCPPort(t)
	route := &Route{
		Name:         "spawn",
		Type:         RouteManaged,
		Port:         port,
		Cmd:          fmt.Sprintf("nc -k -l %d", port),
		Dir:          t.TempDir(),
		RegisteredAt: time.Now(),
	}
	s.table.Add(route)
	t.Cleanup(func() { _ = s.procs.Stop("spawn") })

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	served := s.recoverManagedRoute(w, req, route)

	if !served {
		t.Fatalf("expected the reconnecting page to be served while the process boots")
	}
	if !route.Running.Load() {
		t.Errorf("expected route to be Running after auto-spawn")
	}
	if _, ok := route.PIDValue(); !ok {
		t.Errorf("expected a PID after auto-spawn")
	}
	if !strings.Contains(w.Body.String(), "/_api/routes/spawn/repair") {
		t.Errorf("expected reconnecting (repair) page; body: %s", w.Body.String())
	}
	// The in-flight marker must be cleared once startManagedNow returns.
	if s.isAutoStarting("spawn") {
		t.Errorf("auto-start in-flight marker not cleared")
	}
}

func TestPersistedManagedPIDRoundTrip(t *testing.T) {
	dir := t.TempDir()
	table := NewRouteTable()
	r := &Route{Name: "mp", Type: RouteManaged, Port: 4321, Cmd: "x", RegisteredAt: time.Now()}
	r.SetPID(54321)
	r.Running.Store(true)
	table.Add(r)

	if err := saveStickyRoutes(table, dir); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded := NewRouteTable()
	if err := loadStickyRoutes(loaded, dir); err != nil {
		t.Fatalf("load: %v", err)
	}
	got, ok := loaded.Get("mp")
	if !ok {
		t.Fatal("route not reloaded")
	}
	pid, ok := got.PIDValue()
	if !ok || pid != 54321 {
		t.Errorf("reloaded PID = %d (set=%v); want 54321", pid, ok)
	}
	// Managed routes always load stopped — adoption decides if they're running.
	if got.Running.Load() {
		t.Errorf("reloaded managed route should be Running=false until adopted")
	}
}

// Ensure a stopped managed route whose PID is dead does not persist that PID
// (so a later load can't misfire adoption against a recycled PID).
func TestStoppedManagedRouteDropsPID(t *testing.T) {
	dir := t.TempDir()
	table := NewRouteTable()
	r := &Route{Name: "sp", Type: RouteManaged, Port: 4322, Cmd: "x", RegisteredAt: time.Now()}
	r.SetPID(54321)
	r.Running.Store(false) // stopped
	table.Add(r)

	if err := saveStickyRoutes(table, dir); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded := NewRouteTable()
	if err := loadStickyRoutes(loaded, dir); err != nil {
		t.Fatalf("load: %v", err)
	}
	got, _ := loaded.Get("sp")
	if pid, ok := got.PIDValue(); ok {
		t.Errorf("stopped managed route persisted a PID (%d); want none", pid)
	}
}

// TestSweepFlagsSquatterOnManagedPort covers the silent-rot case: a managed
// route that became Ready, then had its registered port taken over by a
// process outside its group (e.g. the child died, the OS recycled the port to
// a squatter and the PID to an unrelated live process). The sweep must notice
// the port is no longer served by the route's own group, flip Running/Ready
// off, and seed a failure so the start page can offer a restart.
func TestSweepFlagsSquatterOnManagedPort(t *testing.T) {
	s := testServer()
	s.ConfigDir = t.TempDir()
	port := freeTCPPort(t)
	route, _ := startManagedListener(t, s, "rot", port)
	s.table.Add(route)

	// Healthy: the sweep must NOT disturb a route whose registered port is
	// still served by its own group (guards against false positives).
	s.sweepRoutes()
	if !route.Ready.Load() || !route.Running.Load() {
		t.Fatalf("sweep flagged a healthy managed route: running=%v ready=%v",
			route.Running.Load(), route.Ready.Load())
	}

	// Simulate the registered port being recycled away from the group: repoint
	// the route at a port its process group does not serve. The tracked PID is
	// still alive (the listener keeps running), so processAlive stays true —
	// only the port-ownership check can catch this.
	route.Port = freeTCPPort(t)

	s.sweepRoutes()

	if route.Running.Load() || route.Ready.Load() {
		t.Errorf("sweep left a squatted route healthy: running=%v ready=%v",
			route.Running.Load(), route.Ready.Load())
	}
	if route.LoadFailure() == nil {
		t.Error("sweep did not seed a failure; start page would have nothing to offer a restart from")
	}
	if _, ok := route.PIDValue(); ok {
		t.Error("sweep left a stale PID on a squatted route")
	}
}
