//go:build !windows

package daemon

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// TestHandleProxyErrorRecoversFromSquatter reproduces the silent-rot 502: a
// managed route is marked running/ready on a port, but its real process is gone
// and a *foreign* process now holds that port (e.g. Raycast on a recycled
// ephemeral port). The proxy upstream fails, and handleProxyError must run
// recovery — serving the start page rather than surfacing a bare 502 — and must
// flip the route out of the ready state.
func TestHandleProxyErrorRecoversFromSquatter(t *testing.T) {
	s := testServer()
	s.ConfigDir = t.TempDir()

	// A squatter: a listener that accepts TCP but isn't the route's process and
	// isn't in its process group. Stands in for Raycast on the recycled port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	squatPort := ln.Addr().(*net.TCPAddr).Port

	route := &Route{
		Name:         "rot",
		Type:         RouteManaged,
		Port:         squatPort,
		Cmd:          "", // no command → recovery falls to the start page, no spawn
		RegisteredAt: time.Now(),
	}
	route.SetPID(2147483646) // a dead/bogus PID, like a process that already exited
	route.Running.Store(true)
	route.Ready.Store(true)
	s.table.Add(route)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "rot.test"

	s.handleProxyError(rec, req, route)

	if rec.Code == http.StatusBadGateway {
		t.Errorf("got a bare 502; expected recovery, not a dead-end gateway error")
	}
	if route.Ready.Load() {
		t.Error("route should be marked not-ready after a proxy failure")
	}
	if route.Running.Load() {
		t.Error("route with a dead process should be marked not-running after recovery")
	}
}

// TestHealthyProxyDoesNotInvokeErrorHandler is a guard on the cost constraint:
// the ownership/recovery probing must only run on the error path. A healthy
// upstream must serve normally with the error handler never firing.
func TestHealthyProxyDoesNotInvokeErrorHandler(t *testing.T) {
	s := testServer()

	// A real HTTP backend that responds 200.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer backend.Close()
	port := backend.Listener.Addr().(*net.TCPAddr).Port

	route := &Route{Name: "ok", Type: RouteManaged, Port: port, RegisteredAt: time.Now()}
	route.SetPID(os.Getpid()) // a live, signalable PID so the pre-proxy check passes
	route.Running.Store(true)
	route.Ready.Store(true)
	s.table.Add(route)

	if !s.isPortReady(port) {
		t.Fatal("backend should be reachable")
	}
	// Healthy request through routeRequest should pass through and return 200,
	// with the route still ready (error handler never fires).
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "ok.test"
	s.routeRequest(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("healthy proxy returned %d, want 200", rec.Code)
	}
	if !route.Ready.Load() {
		t.Error("healthy route should remain ready")
	}
}
