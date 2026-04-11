package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/localvibe/vibe/internal/config"
)

func testServer() *Server {
	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			Port: 0,
			TLD:  "test",
		},
	}
	return NewServer(cfg)
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
	s.startedAt = s.startedAt // ensure it's set

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
