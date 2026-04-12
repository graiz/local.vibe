package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/localvibe/vibe/internal/config"
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
