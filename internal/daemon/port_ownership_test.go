//go:build !windows

package daemon

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/graiz/local.vibe/internal/config"
)

// spawnIsolatedSleep starts a `sleep` process in its own process group (so it
// shares no group with the test process) and returns its PID. The process
// listens on nothing, which makes it a clean stand-in for a managed child whose
// process group owns no — or a specific — port. Killed on test cleanup.
func spawnIsolatedSleep(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return cmd.Process.Pid
}

// TestLsofListenPortRestrictsToGivenPIDs is the direct regression for the
// missing `-a`: lsof ORs its filters by default, so `-iTCP -sTCP:LISTEN -p PID`
// without `-a` returns every listening socket on the machine. A process that
// listens on nothing must yield no port — otherwise process-group discovery
// hands back a stranger's port.
func TestLsofListenPortRestrictsToGivenPIDs(t *testing.T) {
	pid := spawnIsolatedSleep(t) // listens on nothing
	if port, ok := lsofListenPort([]int{pid}); ok {
		t.Fatalf("lsofListenPort returned %d for a process that listens on nothing; -a filter is missing", port)
	}
}

// TestDiscoverRoutePortIgnoresLogForLiveManagedChild is the Bug 1 regression:
// a managed route whose child is still alive must NOT have its registration
// rewritten to a port merely because the route's log names it and something
// answers there. That "something" is exactly the squatter (Superhuman/Beeper on
// a recycled ephemeral port) that hijacked foundersedge. A live child's real
// port is authoritative via its process group; a stale log line is not.
func TestDiscoverRoutePortIgnoresLogForLiveManagedChild(t *testing.T) {
	tmp := t.TempDir()

	// A real listener the log points at — stands in for the squatter that
	// happens to answer on the port an old log line mentions.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	squatPort := ln.Addr().(*net.TCPAddr).Port

	s := NewServer(&config.Config{Daemon: config.DaemonConfig{Port: 0, TLD: "test"}})
	s.ConfigDir = tmp

	name := "founders"
	if err := os.WriteFile(filepath.Join(tmp, name+".log"),
		[]byte(fmt.Sprintf("Server running at http://localhost:%d\n", squatPort)), 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	route := &Route{Name: name, Port: 9, Type: RouteManaged, Cmd: "jekyll serve --port $PORT"}
	route.SetPID(spawnIsolatedSleep(t)) // live child, its group listens on nothing
	route.Running.Store(true)
	s.table.Add(route)

	if got, ok := s.discoverRoutePort(route); ok {
		t.Fatalf("discoverRoutePort adopted port %d from the log for a live managed child; want no rewrite", got)
	}
}

// TestDiscoverRoutePortUsesLogWhenChildGone guards against over-restriction:
// when there is no live child to anchor on (the managed process died), the log
// fallback is still the only way to relocate the app, so it must remain active.
func TestDiscoverRoutePortUsesLogWhenChildGone(t *testing.T) {
	tmp := t.TempDir()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	realPort := ln.Addr().(*net.TCPAddr).Port

	s := NewServer(&config.Config{Daemon: config.DaemonConfig{Port: 0, TLD: "test"}})
	s.ConfigDir = tmp

	name := "gone"
	if err := os.WriteFile(filepath.Join(tmp, name+".log"),
		[]byte(fmt.Sprintf("Server running at http://localhost:%d\n", realPort)), 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	route := &Route{Name: name, Port: 9, Type: RouteManaged, Cmd: "npm run dev"}
	// No PID set → no live child → log fallback must still apply.
	s.table.Add(route)

	got, ok := s.discoverRoutePort(route)
	if !ok || got != realPort {
		t.Fatalf("discoverRoutePort = (%d, %v); want (%d, true) from log when child is gone", got, ok, realPort)
	}
}

// TestManagedRouteDoesNotProxyStrangerThatSpeaksHTTP is the Bug 2 regression:
// a managed route is Running with a live child, but its registered port is held
// by a FOREIGN process that returns a perfectly valid HTTP response (a 401,
// like Superhuman). The reverse proxy's round-trip succeeds, so the
// ErrorHandler never fires — vibe must still refuse to serve the stranger's
// response and instead surface the repair page.
func TestManagedRouteDoesNotProxyStrangerThatSpeaksHTTP(t *testing.T) {
	stranger := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("stranger-401-body"))
	}))
	defer stranger.Close()

	s := testServer()
	port := testPortFromURL(t, stranger.URL)

	route := &Route{Name: "sq", Type: RouteManaged, Port: port, Cmd: "jekyll serve", RegisteredAt: time.Now()}
	route.SetPID(spawnIsolatedSleep(t)) // live child whose group does NOT own `port`
	route.Running.Store(true)
	route.Ready.Store(true)
	s.table.Add(route)

	// Sanity: the stranger really is reachable on the registered port, so the
	// plain readiness check (isPortReady) is fooled — only ownership catches it.
	if !s.isPortReady(port) {
		t.Fatal("stranger should be reachable on the registered port")
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "sq.test"
	w := httptest.NewRecorder()
	s.routeRequest(w, req)

	body := w.Body.String()
	if strings.Contains(body, "stranger-401-body") {
		t.Fatalf("proxied the stranger's response instead of catching it:\n%s", body)
	}
	if !strings.Contains(body, "/_api/routes/sq/repair") {
		t.Fatalf("expected the repair page (which rediscovers the real port), got:\n%s", body)
	}
	if route.Ready.Load() {
		t.Error("route should be marked not-ready once a stranger is detected on its port")
	}
}

// TestHandleRepairRediscoversWhenPortForeign ensures the /repair endpoint no
// longer short-circuits as "already reachable" when the registered port is held
// by a stranger. Reachable-but-foreign must fall through to rediscovery.
func TestHandleRepairRediscoversWhenPortForeign(t *testing.T) {
	stranger := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer stranger.Close()

	tmp := t.TempDir()
	s := NewServer(&config.Config{Daemon: config.DaemonConfig{Port: 0, TLD: "test"}})
	s.ConfigDir = tmp

	port := testPortFromURL(t, stranger.URL)
	route := &Route{Name: "sq", Type: RouteManaged, Port: port, Cmd: "jekyll serve"}
	route.SetPID(spawnIsolatedSleep(t))
	route.Running.Store(true)
	s.table.Add(route)

	req := httptest.NewRequest("GET", "/_api/routes/sq/repair", nil)
	w := httptest.NewRecorder()
	s.apiHandler(w, req)

	// The stranger is reachable, so the OLD behavior returned {"note":"already
	// reachable"}. With the ownership-aware short-circuit it must instead attempt
	// rediscovery; the isolated sleep child owns no port, so discovery finds
	// nothing and it reports not-fixed rather than "already reachable".
	body := w.Body.String()
	if strings.Contains(body, "already reachable") {
		t.Fatalf("repair reported 'already reachable' for a stranger-held port:\n%s", body)
	}
}
