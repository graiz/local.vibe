package daemon

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestManagedStoppedRouteDoesNotProxyForeignListener(t *testing.T) {
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("foreign-app"))
	}))
	defer foreign.Close()

	s := testServer()
	port := testPortFromURL(t, foreign.URL)
	route := &Route{
		Name:         "lp",
		Type:         RouteManaged,
		Port:         port,
		Cmd:          "npm run dev",
		RegisteredAt: time.Now(),
	}
	route.Running.Store(false)
	route.Ready.Store(false)
	s.table.Add(route)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "lp.test"
	w := httptest.NewRecorder()
	s.routeRequest(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "foreign-app") {
		t.Fatalf("unexpected proxy to foreign listener on reused port")
	}
	if !strings.Contains(body, "/_api/routes/lp/start") {
		t.Fatalf("expected managed start page, got: %s", body)
	}
}

func TestManagedRunningRouteStillProxies(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	s := testServer()
	port := testPortFromURL(t, upstream.URL)
	route := &Route{
		Name:         "app",
		Type:         RouteManaged,
		Port:         port,
		Cmd:          "npm run dev",
		RegisteredAt: time.Now(),
	}
	route.SetPID(os.Getpid())
	route.Running.Store(true)
	route.Ready.Store(true)
	s.table.Add(route)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "app.test"
	w := httptest.NewRecorder()
	s.routeRequest(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	if got := w.Body.String(); got != "ok" {
		t.Fatalf("body = %q; want ok", got)
	}
}
