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
	"syscall"
	"testing"
	"time"

	"github.com/graiz/local.vibe/internal/config"
)

func testServer() *Server {
	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			Port: 0,
			TLD:  "test",
		},
	}
	s := NewServer(cfg)
	// Use a temp dir so tests never clobber the real ~/.vibe/routes.json.
	s.ConfigDir = os.TempDir()
	return s
}

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

	// Should become ready within 2 seconds.
	deadline := time.After(2 * time.Second)
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

	// Should get an error response indicating port conflict.
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	errMsg, _ := resp["error"].(string)
	if errMsg == "" {
		t.Error("expected error message in response body")
	}
	t.Logf("got expected error: status=%d error=%q", w.Code, errMsg)
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
	writeStartFailure(w, http.StatusInternalServerError, se)

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
