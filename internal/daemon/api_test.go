//go:build !windows

// Phase 1 of Windows support: this test file uses syscall.SysProcAttr.Setpgid
// and syscall.Kill, which don't exist on Windows. The tests themselves are
// useful and will be split into Windows-compatible variants in Phase 2.

package daemon

import (
	"bytes"
	"encoding/json"
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
)

// testServer is defined in test_helpers_test.go so it stays visible on
// Windows builds, where this file is excluded by build tag.

func TestAPIRegisterAndList(t *testing.T) {
	s := testServer()

	// Register a sticky route
	body, _ := json.Marshal(map[string]any{"name": "myapp", "port": 3000})
	req := httptest.NewRequest("POST", "/_api/routes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.apiHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("register status = %d; want 200; body: %s", w.Code, w.Body.String())
	}

	// List routes
	req = httptest.NewRequest("GET", "/_api/routes", nil)
	w = httptest.NewRecorder()
	s.apiHandler(w, req)

	var routes []routeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &routes); err != nil {
		t.Fatalf("list unmarshal: %v", err)
	}
	if len(routes) != 1 || routes[0].Name != "myapp" {
		t.Errorf("list = %+v; want 1 route named myapp", routes)
	}
}

func TestAPIRegisterValidation(t *testing.T) {
	s := testServer()

	tests := []struct {
		name string
		body map[string]any
		want int
	}{
		{"missing name", map[string]any{"port": 3000}, http.StatusBadRequest},
		{"missing port and url", map[string]any{"name": "x"}, http.StatusBadRequest},
		{"invalid name chars", map[string]any{"name": "My App!", "port": 3000}, http.StatusBadRequest},
		{"reserved name", map[string]any{"name": "local", "port": 3000}, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(tt.body)
			req := httptest.NewRequest("POST", "/_api/routes", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			s.apiHandler(w, req)
			if w.Code != tt.want {
				t.Errorf("status = %d; want %d; body: %s", w.Code, tt.want, w.Body.String())
			}
		})
	}
}

func TestAPIDeregister(t *testing.T) {
	s := testServer()

	// Register
	body, _ := json.Marshal(map[string]any{"name": "temp", "port": 5000})
	req := httptest.NewRequest("POST", "/_api/routes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.apiHandler(w, req)

	// Deregister
	req = httptest.NewRequest("DELETE", "/_api/routes/temp", nil)
	w = httptest.NewRecorder()
	s.apiHandler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("deregister status = %d; want 200", w.Code)
	}

	// Deregister again → 404
	req = httptest.NewRequest("DELETE", "/_api/routes/temp", nil)
	w = httptest.NewRecorder()
	s.apiHandler(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("double deregister status = %d; want 404", w.Code)
	}
}

func TestAPIUpdate(t *testing.T) {
	s := testServer()

	// Register
	body, _ := json.Marshal(map[string]any{"name": "app", "port": 3000})
	req := httptest.NewRequest("POST", "/_api/routes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.apiHandler(w, req)

	// Update port
	body, _ = json.Marshal(map[string]any{"port": 4000})
	req = httptest.NewRequest("PUT", "/_api/routes/app", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	s.apiHandler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("update status = %d; want 200", w.Code)
	}

	r, _ := s.table.Get("app")
	if r.Port != 4000 {
		t.Errorf("port after update = %d; want 4000", r.Port)
	}
}

// TestAPIUpdateRenameWithValidationFailure regresses a bug where a rename
// + later validation failure (bad URL) removed the old key from the table
// but never re-added it, silently deleting the route.
func TestAPIUpdateRenameWithValidationFailure(t *testing.T) {
	s := testServer()

	body, _ := json.Marshal(map[string]any{"name": "foo", "port": 3000})
	req := httptest.NewRequest("POST", "/_api/routes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.apiHandler(httptest.NewRecorder(), req)

	body, _ = json.Marshal(map[string]any{"name": "bar", "url": "not-a-valid-url"})
	req = httptest.NewRequest("PUT", "/_api/routes/foo", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.apiHandler(w, req)

	if w.Code == http.StatusOK {
		t.Fatalf("expected validation error, got 200")
	}
	if _, ok := s.table.Get("foo"); !ok {
		t.Errorf("route foo vanished after failed rename+update")
	}
	if _, ok := s.table.Get("bar"); ok {
		t.Errorf("route bar should not exist after failed validation")
	}
}

func TestAPIUpdateIdleTimeout(t *testing.T) {
	s := testServer()

	// Register managed route (no cmd so it won't try to start a process)
	body, _ := json.Marshal(map[string]any{"name": "svc", "port": 5000})
	req := httptest.NewRequest("POST", "/_api/routes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.apiHandler(w, req)

	// Set idle timeout
	body, _ = json.Marshal(map[string]any{"idle_timeout": 120})
	req = httptest.NewRequest("PUT", "/_api/routes/svc", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	s.apiHandler(w, req)

	r, _ := s.table.Get("svc")
	if r.IdleTimeout != 120 {
		t.Errorf("idle_timeout = %d; want 120", r.IdleTimeout)
	}
}

func TestAPIHealth(t *testing.T) {
	s := testServer()
	// startedAt is set by testServer()

	req := httptest.NewRequest("GET", "/_api/health", nil)
	w := httptest.NewRecorder()
	s.apiHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("health status = %d", w.Code)
	}
	var h map[string]any
	json.Unmarshal(w.Body.Bytes(), &h)
	if h["status"] != "ok" {
		t.Errorf("health status = %v; want ok", h["status"])
	}
}

func TestAPIBookmarkRoute(t *testing.T) {
	s := testServer()

	body, _ := json.Marshal(map[string]any{"name": "ext", "url": "https://example.com"})
	req := httptest.NewRequest("POST", "/_api/routes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.apiHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("register bookmark status = %d; body: %s", w.Code, w.Body.String())
	}

	r, ok := s.table.Get("ext")
	if !ok {
		t.Fatal("bookmark route not found")
	}
	if r.Type != RouteBookmark {
		t.Errorf("type = %s; want bookmark", r.Type)
	}
	if r.ExternalURL != "https://example.com" {
		t.Errorf("external_url = %s; want https://example.com", r.ExternalURL)
	}
}

// TestAPIBookmarkURLValidation rejects non-http(s) schemes and malformed
// URLs so the 307 redirect target can't become a javascript: / file: / data:
// open-redirect vector.
func TestAPIBookmarkURLValidation(t *testing.T) {
	cases := []struct {
		name, url string
		want      int
	}{
		{"js-scheme", "javascript:alert(1)", http.StatusBadRequest},
		{"file-scheme", "file:///etc/passwd", http.StatusBadRequest},
		{"data-scheme", "data:text/html,<script>1</script>", http.StatusBadRequest},
		{"no-host", "https://", http.StatusBadRequest},
		{"http-ok", "http://10.0.0.1:8080", http.StatusOK},
		{"https-ok", "https://example.com/path?q=1", http.StatusOK},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer()
			name := fmt.Sprintf("bm%d", i)
			body, _ := json.Marshal(map[string]any{"name": name, "url": tc.url})
			req := httptest.NewRequest("POST", "/_api/routes", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			s.apiHandler(w, req)
			if w.Code != tc.want {
				t.Errorf("status = %d; want %d; body: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

// TestAPIStartStopAnchoring ensures that POST /_api/<non-routes>/start
// does NOT fall through to the start handler — prior to the fix the route
// pattern matched on suffix only.
func TestAPIStartStopAnchoring(t *testing.T) {
	s := testServer()
	for _, path := range []string{"/_api/something/stop", "/_api/something/start"} {
		req := httptest.NewRequest("POST", path, nil)
		w := httptest.NewRecorder()
		s.apiHandler(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d; want 404", path, w.Code)
		}
	}
}

// TestAPIErrorResponseIsValidJSON covers the JSON-injection fix: prior to
// the switch to writeJSONError, error messages with quotes or newlines
// (e.g. tailed process output) could break the {"error":"..."} structure.
func TestAPIErrorResponseIsValidJSON(t *testing.T) {
	s := testServer()
	// invalid name — exercises the error path
	body, _ := json.Marshal(map[string]any{"name": "Bad Name!", "port": 3000})
	req := httptest.NewRequest("POST", "/_api/routes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.apiHandler(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", w.Code)
	}
	var parsed map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("response is not valid JSON: %v; body: %s", err, w.Body.String())
	}
	if parsed["error"] == "" {
		t.Errorf("parsed error field is empty; body: %s", w.Body.String())
	}
}

// TestManagedRouteReadyWaitsForPort verifies that a managed route starts with
// Running=true but Ready=false, and only flips Ready=true once its port is
// actually accepting connections. This covers REPL-wrapped servers (e.g.
// iex -S mix phx.server) where the process is Running before the port is Ready.
func TestManagedRouteReadyWaitsForPort(t *testing.T) {
	t.Parallel()
	s := testServer()

	// Pick a free port by briefly listening, then closing.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	// Register a managed route whose command sleeps 3s before starting a listener.
	// This simulates a REPL wrapper that's alive before the server is ready.
	cmd := fmt.Sprintf("sleep 3 && nc -l %d", port)
	body, _ := json.Marshal(map[string]any{
		"name": "delayed",
		"port": port,
		"cmd":  cmd,
		"dir":  t.TempDir(),
	})
	req := httptest.NewRequest("POST", "/_api/routes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.apiHandler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("register status = %d; body: %s", w.Code, w.Body.String())
	}

	// Immediately after registration, Running should be true but Ready should be false.
	route, ok := s.table.Get("delayed")
	if !ok {
		t.Fatal("route not found")
	}
	if !route.Running.Load() {
		t.Error("expected Running=true immediately after start")
	}
	if route.Ready.Load() {
		t.Error("expected Ready=false immediately after start (port not yet listening)")
	}

	// Wait for the server to come up (sleep 3s + some buffer).
	deadline := time.After(6 * time.Second)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	ready := false
	for !ready {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for Ready to become true")
		case <-ticker.C:
			ready = route.Ready.Load()
		}
	}

	// Clean up.
	s.procs.Stop("delayed")
}

// TestManagedRouteReadyImmediatePort verifies that when the server binds its
// port quickly (normal case), Ready becomes true within the polling window.
func TestManagedRouteReadyImmediatePort(t *testing.T) {
	t.Parallel()
	s := testServer()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	// Start a listener immediately via nc.
	cmd := fmt.Sprintf("nc -l %d", port)
	body, _ := json.Marshal(map[string]any{
		"name": "quick",
		"port": port,
		"cmd":  cmd,
		"dir":  t.TempDir(),
	})
	req := httptest.NewRequest("POST", "/_api/routes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.apiHandler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("register status = %d; body: %s", w.Code, w.Body.String())
	}

	// Should become ready well within the 30s readiness-polling window. The
	// child is spawned through a login shell ($SHELL -lic), which under
	// full-suite parallel load can take several seconds to reach the command —
	// 2s flaked.
	deadline := time.After(10 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for Ready=true")
		case <-ticker.C:
			route, _ := s.table.Get("quick")
			if route.Ready.Load() {
				s.procs.Stop("quick")
				return
			}
		}
	}
}

// TestManagedRouteNeverBindsPort verifies that when a process stays alive but
// never binds its port, Ready remains false after the timeout expires. Uses a
// short ReadyTimeout so the test completes quickly.
func TestManagedRouteNeverBindsPort(t *testing.T) {
	t.Parallel()
	s := testServer()
	s.ReadyTimeout = 2 * time.Second

	// Pick a port that nothing will bind.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	// "sleep 60" stays alive but never listens on the port.
	body, _ := json.Marshal(map[string]any{
		"name": "hangs",
		"port": port,
		"cmd":  "sleep 60",
		"dir":  t.TempDir(),
	})
	req := httptest.NewRequest("POST", "/_api/routes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.apiHandler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("register status = %d; body: %s", w.Code, w.Body.String())
	}

	route, _ := s.table.Get("hangs")
	if !route.Running.Load() {
		t.Error("expected Running=true")
	}
	if route.Ready.Load() {
		t.Error("expected Ready=false immediately after start")
	}

	// Wait for the ReadyTimeout to expire plus a small buffer.
	time.Sleep(3 * time.Second)

	if route.Ready.Load() {
		t.Error("expected Ready=false after timeout (process never bound port)")
	}
	if !route.Running.Load() {
		t.Error("expected Running=true (process is still alive)")
	}

	s.procs.Stop("hangs")
}

// TestStartManagedRoutePortOccupied verifies that starting a managed route
// returns an error when the target port is already occupied by another process
// that the daemon cannot kill. Uses a subprocess to hold the port so that
// killPort's SIGTERM hits the child rather than the test process itself.
func TestStartManagedRoutePortOccupied(t *testing.T) {
	t.Parallel()
	s := testServer()
	s.ReadyTimeout = 2 * time.Second

	// Pick a free port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	// Spawn a subprocess that binds the port and ignores SIGTERM so killPort can't free it.
	// Python's BaseHTTPServer is available on macOS and will bind the port.
	holder := exec.Command("python3", "-c", fmt.Sprintf(
		`import signal, http.server, socketserver
signal.signal(signal.SIGTERM, signal.SIG_IGN)
with socketserver.TCPServer(("127.0.0.1", %d), http.server.BaseHTTPRequestHandler) as s:
    s.serve_forever()`, port))
	holder.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := holder.Start(); err != nil {
		t.Fatalf("start port holder: %v", err)
	}
	t.Cleanup(func() {
		// Force-kill the holder since it ignores SIGTERM.
		syscall.Kill(-holder.Process.Pid, syscall.SIGKILL)
		holder.Wait()
	})

	// Wait for the holder to bind the port.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !s.isPortReady(port) {
		t.Fatalf("port holder never bound port %d", port)
	}

	// Register a managed route on the occupied port.
	body, _ := json.Marshal(map[string]any{
		"name": "blocked",
		"port": port,
		"cmd":  "sleep 60",
		"dir":  t.TempDir(),
	})
	req := httptest.NewRequest("POST", "/_api/routes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.apiHandler(w, req)

	// The daemon should detect the port is still occupied after the kill attempt
	// and return an error instead of silently starting a doomed process.
	if w.Code == http.StatusOK {
		route, _ := s.table.Get("blocked")
		if route != nil {
			s.procs.Stop("blocked")
		}
		t.Fatalf("expected error when port %d is occupied, but got 200 OK: %s", port, w.Body.String())
	}

	// Should get an error response indicating port conflict, with a recovery
	// hint identifying the holder PID so the dashboard can offer a one-click
	// "Kill PID X and retry" button instead of a useless plain "Retry".
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	errMsg, _ := resp["error"].(string)
	if errMsg == "" {
		t.Error("expected error message in response body")
	}
	rec, ok := resp["recovery"].(map[string]any)
	if !ok {
		t.Fatalf("expected recovery hint in response; body: %s", w.Body.String())
	}
	if rec["action"] != "kill_pid" {
		t.Errorf("recovery action = %v; want kill_pid", rec["action"])
	}
	if int(rec["pid"].(float64)) != holder.Process.Pid {
		t.Errorf("recovery pid = %v; want %d", rec["pid"], holder.Process.Pid)
	}
	t.Logf("got expected error+recovery: status=%d error=%q recovery=%v", w.Code, errMsg, rec)
}

// TestHandleStartPortOccupiedAttachesRecovery is the screener.vibe regression:
// a stale dev-server process holds the route's port, the user clicks Start
// from the not-running page, the daemon can't free the port, and we MUST
// surface a recovery hint with the offending PID so the startpage can offer
// a one-click "Kill PID X and retry" instead of a bare "Retry" that fails
// the same way again. Also verifies the recovery is persisted on the route's
// Failure so a page reload rehydrates the button.
func TestHandleStartPortOccupiedAttachesRecovery(t *testing.T) {
	t.Parallel()
	s := testServer()
	s.ReadyTimeout = 2 * time.Second

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	holder := exec.Command("python3", "-c", fmt.Sprintf(
		`import signal, http.server, socketserver
signal.signal(signal.SIGTERM, signal.SIG_IGN)
with socketserver.TCPServer(("127.0.0.1", %d), http.server.BaseHTTPRequestHandler) as s:
    s.serve_forever()`, port))
	holder.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := holder.Start(); err != nil {
		t.Fatalf("start port holder: %v", err)
	}
	t.Cleanup(func() {
		syscall.Kill(-holder.Process.Pid, syscall.SIGKILL)
		holder.Wait()
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if dialErr == nil {
			conn.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !s.isPortReady(port) {
		t.Fatalf("port holder never bound port %d", port)
	}

	// Pre-register the managed route in the table without starting it — this
	// matches the state after a daemon restart with persisted routes, before
	// the user clicks Start.
	route := &Route{
		Name:         "screener-repro",
		Port:         port,
		Type:         RouteManaged,
		Cmd:          "sleep 60",
		Dir:          t.TempDir(),
		RegisteredAt: time.Now(),
	}
	s.table.Add(route)
	t.Cleanup(func() { s.table.Remove(route.Name) })

	req := httptest.NewRequest("POST", fmt.Sprintf("/_api/routes/%s/start", route.Name), nil)
	w := httptest.NewRecorder()
	s.apiHandler(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d; want 409; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v; body: %s", err, w.Body.String())
	}
	rec, ok := resp["recovery"].(map[string]any)
	if !ok {
		t.Fatalf("expected recovery hint; body: %s", w.Body.String())
	}
	if rec["action"] != "kill_pid" {
		t.Errorf("recovery action = %v; want kill_pid", rec["action"])
	}
	if int(rec["pid"].(float64)) != holder.Process.Pid {
		t.Errorf("recovery pid = %v; want %d", rec["pid"], holder.Process.Pid)
	}

	// Reload-survival: the recovery must be persisted on the Route's Failure
	// so the startpage's initRecovery hydration rehydrates the button.
	failure := route.LoadFailure()
	if failure == nil {
		t.Fatalf("expected route.Failure to be set after port conflict")
	}
	if failure.Recovery == nil {
		t.Fatalf("expected route.Failure.Recovery to be set; got %+v", failure)
	}
	if failure.Recovery.PID != holder.Process.Pid {
		t.Errorf("persisted recovery pid = %d; want %d", failure.Recovery.PID, holder.Process.Pid)
	}
}

// TestBuildPortConflictRecoveryFiltersAndShape covers three branches of the
// port-conflict recovery builder using injected lsof/ps stubs:
//   - the daemon's own PID is filtered out (safeKillPID would reject it),
//   - exactly one remaining external PID becomes a kill_pid hint with the
//     command name embedded in the message,
//   - multiple external PIDs collapse to a kill_port hint.
func TestBuildPortConflictRecoveryFiltersAndShape(t *testing.T) {
	// NOT t.Parallel(): mutates package-level findPortHoldersFn / pidCommandFn,
	// which the integration-style port-occupied tests read via the real handler.
	s := testServer()

	origHolders := findPortHoldersFn
	origCmd := pidCommandFn
	t.Cleanup(func() {
		findPortHoldersFn = origHolders
		pidCommandFn = origCmd
	})

	pidCommandFn = func(pid int) string {
		if pid == 999002 {
			return "node"
		}
		return ""
	}

	// Single external holder + daemon's own PID.
	findPortHoldersFn = func(int) []int { return []int{os.Getpid(), 999002} }
	rec := s.buildPortConflictRecovery(4242)
	if rec == nil {
		t.Fatalf("expected recovery for single external holder; got nil")
	}
	if rec.Action != "kill_pid" || rec.PID != 999002 {
		t.Errorf("got action=%q pid=%d; want kill_pid pid=999002", rec.Action, rec.PID)
	}
	if !strings.Contains(rec.Message, "node") {
		t.Errorf("message %q should mention command name", rec.Message)
	}

	// Multiple external holders → kill_port.
	findPortHoldersFn = func(int) []int { return []int{999002, 999003, 999004} }
	rec = s.buildPortConflictRecovery(4242)
	if rec == nil {
		t.Fatalf("expected recovery for multi holders; got nil")
	}
	if rec.Action != "kill_port" || rec.Port != 4242 {
		t.Errorf("got action=%q port=%d; want kill_port port=4242", rec.Action, rec.Port)
	}

	// Only the daemon's own PID → no recovery (transient/false positive).
	findPortHoldersFn = func(int) []int { return []int{os.Getpid()} }
	if rec := s.buildPortConflictRecovery(4242); rec != nil {
		t.Errorf("expected nil recovery when only daemon PID holds the port; got %+v", rec)
	}
}

// TestValidateReservePorts covers the validation rules for the reserve_ports
// vibe.json field: legal name shape, primary/oauth collisions reject, the
// reserved name "PORT" rejects, range checks bite, and same-port-twice
// rejects (a single port should have one canonical name in $PORT_<NAME>).
func TestValidateReservePorts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		reserve      map[string]int
		primary      int
		oauthCB      int
		want         map[string]int
		wantErrMatch string
	}{
		{name: "empty", reserve: nil, primary: 3012, want: nil},
		{name: "ok single", reserve: map[string]int{"server": 3001}, primary: 3012, want: map[string]int{"server": 3001}},
		{name: "ok mixed case lowered", reserve: map[string]int{"Server": 3001}, primary: 3012, want: map[string]int{"server": 3001}},
		{name: "rejects empty name", reserve: map[string]int{"": 3001}, primary: 3012, wantErrMatch: "empty"},
		{name: "rejects bad name shape", reserve: map[string]int{"1bad": 3001}, primary: 3012, wantErrMatch: "must match"},
		{name: "rejects reserved PORT name", reserve: map[string]int{"PORT": 3001}, primary: 3012, wantErrMatch: "reserved"},
		{name: "rejects primary collision", reserve: map[string]int{"server": 3012}, primary: 3012, wantErrMatch: "primary port"},
		{name: "rejects oauth collision", reserve: map[string]int{"server": 8787}, primary: 3012, oauthCB: 8787, wantErrMatch: "oauth_callback_port"},
		{name: "rejects out of range low", reserve: map[string]int{"server": 0}, primary: 3012, wantErrMatch: "out of range"},
		{name: "rejects out of range high", reserve: map[string]int{"server": 70000}, primary: 3012, wantErrMatch: "out of range"},
		{name: "rejects same port twice", reserve: map[string]int{"server": 3001, "api": 3001}, primary: 3012, wantErrMatch: "both map to port"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateReservePorts(tc.reserve, tc.primary, tc.oauthCB)
			if tc.wantErrMatch != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrMatch) {
					t.Fatalf("err = %v; want error containing %q", err, tc.wantErrMatch)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !equalIntMaps(got, tc.want) {
				t.Errorf("got %v; want %v", got, tc.want)
			}
		})
	}
}

func equalIntMaps(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// TestFindFreePortSkipsReservePorts is the core invariant that prevents the
// screener.vibe / task-tracker collision from happening at config time:
// when route A reserves port 3001 via reserve_ports, the auto-assigner must
// not hand 3001 to route B.
func TestFindFreePortSkipsReservePorts(t *testing.T) {
	t.Parallel()
	table := NewRouteTable()
	table.Add(&Route{
		Name:         "screener",
		Port:         3012,
		Type:         RouteManaged,
		ReservePorts:   map[string]int{"server": 3001, "metrics": 3002, "admin": 3000},
		RegisteredAt: time.Now(),
	})

	for i := 0; i < 5; i++ {
		got, err := findFreePort(table)
		if err != nil {
			t.Fatalf("findFreePort: %v", err)
		}
		if got == 3000 || got == 3001 || got == 3002 || got == 3012 {
			t.Fatalf("findFreePort returned reserved port %d", got)
		}
	}
}

// TestHandleStartReservePortConflictAttachesRecovery is the screener.vibe
// regression for the multi-port case: a stale process (e.g. another app's
// NextAuth) holds one of the route's reserve_ports. Without this, screener's
// bun server silently fails to bind its named "server" port and Vite's
// proxy bleeds traffic into the wrong app. With this, the user gets a
// clear "Kill PID X (node) and retry" recovery — same UX as the primary
// port case — and the persisted Failure references the offending name.
func TestHandleStartReservePortConflictAttachesRecovery(t *testing.T) {
	t.Parallel()
	s := testServer()
	s.ReadyTimeout = 2 * time.Second

	// Pick two distinct free ports: one for the route's primary (will be
	// free), one for a reserve_port (will be held by an external process).
	ln1, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	primary := ln1.Addr().(*net.TCPAddr).Port
	ln1.Close()

	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	reservePort := ln2.Addr().(*net.TCPAddr).Port
	ln2.Close()

	holder := exec.Command("python3", "-c", fmt.Sprintf(
		`import signal, http.server, socketserver
signal.signal(signal.SIGTERM, signal.SIG_IGN)
with socketserver.TCPServer(("127.0.0.1", %d), http.server.BaseHTTPRequestHandler) as s:
    s.serve_forever()`, reservePort))
	holder.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := holder.Start(); err != nil {
		t.Fatalf("start port holder: %v", err)
	}
	t.Cleanup(func() {
		syscall.Kill(-holder.Process.Pid, syscall.SIGKILL)
		holder.Wait()
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", reservePort), 100*time.Millisecond)
		if dialErr == nil {
			conn.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !s.isPortReady(reservePort) {
		t.Fatalf("port holder never bound port %d", reservePort)
	}

	route := &Route{
		Name:         "screener-multi",
		Port:         primary,
		ReservePorts:   map[string]int{"server": reservePort},
		Type:         RouteManaged,
		Cmd:          "sleep 60",
		Dir:          t.TempDir(),
		RegisteredAt: time.Now(),
	}
	s.table.Add(route)
	t.Cleanup(func() { s.table.Remove(route.Name) })

	req := httptest.NewRequest("POST", fmt.Sprintf("/_api/routes/%s/start", route.Name), nil)
	w := httptest.NewRecorder()
	s.apiHandler(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d; want 409; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v; body: %s", err, w.Body.String())
	}
	errMsg, _ := resp["error"].(string)
	if !strings.Contains(errMsg, fmt.Sprintf("port %d", reservePort)) {
		t.Errorf("error %q should reference reserve port %d", errMsg, reservePort)
	}
	failureMsg := route.LoadFailure().Message
	if !strings.Contains(failureMsg, "\"server\"") {
		t.Errorf("persisted failure message %q should reference the reserve_ports name", failureMsg)
	}
	rec, ok := resp["recovery"].(map[string]any)
	if !ok {
		t.Fatalf("expected recovery hint; body: %s", w.Body.String())
	}
	if rec["action"] != "kill_pid" {
		t.Errorf("action = %v; want kill_pid", rec["action"])
	}
	if int(rec["pid"].(float64)) != holder.Process.Pid {
		t.Errorf("pid = %v; want %d", rec["pid"], holder.Process.Pid)
	}

	failure := route.LoadFailure()
	if failure == nil || failure.Recovery == nil {
		t.Fatalf("expected route.Failure.Recovery to be persisted for reload survival")
	}
	if failure.Recovery.PID != holder.Process.Pid {
		t.Errorf("persisted pid = %d; want %d", failure.Recovery.PID, holder.Process.Pid)
	}
}

// TestWriteStartFailureIncludesRecoveryHint verifies that a StartError whose
// Tail matches a known log-scan pattern (e.g. Next.js orphan PID) is turned
// into a structured recovery hint in the JSON response, so the dashboard can
// render a one-click "Kill PID X and retry" button.
func TestWriteStartFailureIncludesRecoveryHint(t *testing.T) {
	t.Parallel()
	tail := "⨯ Another next dev server is already running.\n- PID: 98765\n"
	se := &StartError{
		Err:  fmt.Errorf("process exited immediately: exit status 1"),
		Tail: tail,
	}
	w := httptest.NewRecorder()
	writeStartFailure(w, http.StatusInternalServerError, se, "", "")

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500", w.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON: %v; body: %s", err, w.Body.String())
	}
	if resp["log"] != tail {
		t.Errorf("log field = %v; want tail", resp["log"])
	}
	rec, ok := resp["recovery"].(map[string]any)
	if !ok {
		t.Fatalf("response has no recovery hint; body: %s", w.Body.String())
	}
	if rec["action"] != "kill_pid" {
		t.Errorf("action = %v; want kill_pid", rec["action"])
	}
	if pid, _ := rec["pid"].(float64); int(pid) != 98765 {
		t.Errorf("pid = %v; want 98765", rec["pid"])
	}
}

// TestHandleStartSafeKillPIDRejectsDaemon ensures the recovery flow can't be
// used to SIGTERM the daemon's own PID via a crafted kill_pid payload.
func TestAPINonLocalHostKnownDaemonPathStillHitsDaemonAPI(t *testing.T) {
	s := testServer()
	body, _ := json.Marshal(map[string]any{"name": "app", "port": 4444})
	req := httptest.NewRequest(http.MethodPost, "/_api/routes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.apiHandler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("register status = %d; body: %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/_api/routes", nil)
	req.Host = "app.test"
	w = httptest.NewRecorder()
	s.apiHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"name":"app"`) {
		t.Fatalf("expected daemon route list JSON, got: %s", w.Body.String())
	}
}

func TestAPINonLocalHostUnknownPathProxiesUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_api/configuration" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"upstream":true}`))
	}))
	defer upstream.Close()

	s := testServer()
	s.table.Add(&Route{
		Name:         "app",
		Type:         RouteSticky,
		Port:         testPortFromURL(t, upstream.URL),
		RegisteredAt: time.Now(),
	})

	req := httptest.NewRequest(http.MethodGet, "/_api/configuration", nil)
	req.Host = "app.test"
	w := httptest.NewRecorder()
	s.apiHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	if got := strings.TrimSpace(w.Body.String()); got != `{"upstream":true}` {
		t.Fatalf("body = %q; want upstream payload", got)
	}
}

func TestHandleStartSafeKillPIDRejectsDaemon(t *testing.T) {
	t.Parallel()
	s := testServer()

	// Register a managed route so there's a target for /start.
	body, _ := json.Marshal(map[string]any{"name": "owned", "port": 5555})
	req := httptest.NewRequest("POST", "/_api/routes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.apiHandler(w, req)

	// Attach a cmd directly so handleStart won't short-circuit on empty Cmd.
	route, _ := s.table.Get("owned")
	route.Cmd = "sleep 60"
	route.Type = RouteManaged

	// Try to kill the daemon itself.
	kill, _ := json.Marshal(map[string]any{"kill_pid": os.Getpid()})
	req = httptest.NewRequest("POST", "/_api/routes/owned/start", bytes.NewReader(kill))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	w = httptest.NewRecorder()
	s.apiHandler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 (refuse self-kill); body: %s", w.Code, w.Body.String())
	}
}

// TestHandleStopNoAcceptHeaderReturnsJSON guards the CLI fix: handlers used
// to 303-redirect to https://local.{tld}/ when Accept != "application/json",
// which the Unix-socket transport can't follow (TLS over unix conn fails),
// surfacing as a spurious "daemon not running" CLI error. Now we only redirect
// for actual browser form posts (Content-Type: application/x-www-form-urlencoded),
// and a CLI-shaped DELETE with no Accept header gets a JSON 200.
func TestHandleStopNoAcceptHeaderReturnsJSON(t *testing.T) {
	t.Parallel()
	s := testServer()

	// Seed a managed route so /stop has a target.
	s.table.Add(&Route{
		Name:         "cli-stop",
		Type:         RouteManaged,
		Port:         5556,
		Cmd:          "sleep 60",
		RegisteredAt: time.Now(),
	})

	req := httptest.NewRequest(http.MethodDelete, "/_api/routes/cli-stop/stop", nil)
	w := httptest.NewRecorder()
	s.apiHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q; want application/json", ct)
	}
	if loc := w.Header().Get("Location"); loc != "" {
		t.Errorf("unexpected Location header %q — handler should not redirect for CLI requests", loc)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON: %v; body: %s", err, w.Body.String())
	}
	if resp["ok"] != true {
		t.Errorf("ok = %v; want true; body: %s", resp["ok"], w.Body.String())
	}
}

// TestHandleStopBrowserFormStillRedirects ensures the dashboard's form-post
// path still gets the 303-redirect back to the dashboard.
func TestHandleStopBrowserFormStillRedirects(t *testing.T) {
	t.Parallel()
	s := testServer()
	s.table.Add(&Route{
		Name:         "form-stop",
		Type:         RouteManaged,
		Port:         5557,
		Cmd:          "sleep 60",
		RegisteredAt: time.Now(),
	})

	req := httptest.NewRequest(http.MethodPost, "/_api/routes/form-stop/stop", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.apiHandler(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d; want 303; body: %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "local.test") {
		t.Errorf("Location = %q; want redirect to local.{tld}", loc)
	}
}

// TestHandleStartSyncsVibeJSON verifies that an edit to vibe.json (adding
// oauth_callback_port) takes effect on the next vibe start <name> without
// requiring deregister + re-register. This was the screener.vibe bug:
// `startExisting` used to bypass the register flow entirely, so fields the
// user added to vibe.json after first registration never made it onto the
// route.
func TestHandleStartSyncsVibeJSON(t *testing.T) {
	// Not t.Parallel: this test takes a free TCP port and probes the OAuth
	// bridge bind, which races with other parallel tests grabbing random ports.
	s := testServer()

	dir := t.TempDir()

	// Pick a free port for the OAuth bridge so the bind probe in
	// validateOAuthBridgeConfig succeeds regardless of what else is running
	// on the dev machine.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	cbPort := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	// Initial vibe.json with no oauth_callback_port.
	initial := []byte(`{"name":"sync-target","cmd":"sleep 60"}`)
	if err := os.WriteFile(filepath.Join(dir, "vibe.json"), initial, 0644); err != nil {
		t.Fatalf("write vibe.json: %v", err)
	}

	// Register the route the way the CLI does on first start.
	s.table.Add(&Route{
		Name:         "sync-target",
		Type:         RouteManaged,
		Port:         5558,
		Cmd:          "sleep 60",
		Dir:          dir,
		RegisteredAt: time.Now(),
	})

	// User edits vibe.json after registration to add an oauth_callback_port.
	updated := fmt.Appendf(nil, `{"name":"sync-target","cmd":"sleep 60","oauth_callback_port":%d}`, cbPort)
	if err := os.WriteFile(filepath.Join(dir, "vibe.json"), updated, 0644); err != nil {
		t.Fatalf("update vibe.json: %v", err)
	}

	// Call the sync path directly — handleStart calls this before procs.Start,
	// so we don't have to actually spawn anything to verify the field updates.
	if err := s.syncRouteFromVibeJSON(s.mustGet(t, "sync-target")); err != nil {
		t.Fatalf("syncRouteFromVibeJSON: %v", err)
	}
	got := s.mustGet(t, "sync-target")
	if got.OAuthCallbackPort != cbPort {
		t.Errorf("OAuthCallbackPort = %d; want %d (vibe.json edit should have synced)", got.OAuthCallbackPort, cbPort)
	}
	t.Cleanup(func() { s.stopOAuthBridgeListeners() })
}

// TestSyncRouteFromVibeJSONIgnoresWrongName confirms we don't cross-pollinate
// fields when vibe.json's name disagrees with the route — the user is probably
// running `vibe start <other>` from the wrong directory.
func TestSyncRouteFromVibeJSONIgnoresWrongName(t *testing.T) {
	t.Parallel()
	s := testServer()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "vibe.json"),
		[]byte(`{"name":"different","cmd":"sleep 60","oauth_callback_port":9999}`), 0644); err != nil {
		t.Fatalf("write vibe.json: %v", err)
	}
	r := &Route{Name: "victim", Type: RouteManaged, Cmd: "true", Dir: dir, Port: 5559, RegisteredAt: time.Now()}
	s.table.Add(r)
	if err := s.syncRouteFromVibeJSON(r); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if r.OAuthCallbackPort != 0 {
		t.Errorf("OAuthCallbackPort = %d; want 0 (mismatched name should be ignored)", r.OAuthCallbackPort)
	}
}

func (s *Server) mustGet(t *testing.T, name string) *Route {
	t.Helper()
	r, ok := s.table.Get(name)
	if !ok {
		t.Fatalf("route %q not in table", name)
	}
	return r
}

// TestStickyRoundTripPreservesOAuthCallback guards a real daemon-restart bug:
// stickyEntry was missing OAuthCallbackPort entirely, so a managed route's
// callback bridge config was silently dropped on every save and never restored
// on load. Reproduces by saving + loading a round trip through the same dir.
func TestStickyRoundTripPreservesOAuthCallback(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tab := NewRouteTable()
	tab.Add(&Route{
		Name:              "rt",
		Type:              RouteManaged,
		Port:              5560,
		Cmd:               "true",
		OAuthCallbackPort: 8789,
		ReservePorts:      map[string]int{"server": 5561},
		RegisteredAt:      time.Now(),
	})
	if err := saveStickyRoutes(tab, dir); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded := NewRouteTable()
	if err := loadStickyRoutes(loaded, dir); err != nil {
		t.Fatalf("load: %v", err)
	}
	r, ok := loaded.Get("rt")
	if !ok {
		t.Fatal("route missing after load")
	}
	if r.OAuthCallbackPort != 8789 {
		t.Errorf("OAuthCallbackPort = %d; want 8789 (round-trip lost the field)", r.OAuthCallbackPort)
	}
	if r.ReservePorts["server"] != 5561 {
		t.Errorf("ReservePorts[server] = %d; want 5561", r.ReservePorts["server"])
	}
}
