package daemon

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/graiz/local.vibe/internal/config"
)

// peerABWithBackend wires the full relay: backend app behind route "face" on
// B, A paired to B with a populated cache. Returns A and the expected body.
func peerABWithBackend(t *testing.T) (*Server, *Server) {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello from face"))
	}))
	t.Cleanup(backend.Close)
	b := newBareServer(t)
	a := newBareServer(t)
	pairAB(t, a, b)
	route := &Route{Name: "face", Type: RouteSticky, Port: portOf(t, backend.URL), RegisteredAt: time.Now()}
	route.Running.Store(true)
	route.Ready.Store(true)
	b.table.Add(route)
	if _, _, ok := a.findPeerRoute("face"); !ok {
		t.Fatal("peer cache not populated")
	}
	return a, b
}

func TestRouteRequestResolvesPeerRoute(t *testing.T) {
	a, _ := peerABWithBackend(t)
	req := httptest.NewRequest("GET", "http://face.vibe/", nil)
	req.Host = "face.vibe"
	rec := httptest.NewRecorder()
	a.routeRequest(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "hello from face" {
		t.Fatalf("A->B relay: code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestLocalRouteShadowsPeerRoute(t *testing.T) {
	a, _ := peerABWithBackend(t)
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("local face"))
	}))
	t.Cleanup(local.Close)
	route := &Route{Name: "face", Type: RouteSticky, Port: portOf(t, local.URL), RegisteredAt: time.Now()}
	route.Running.Store(true)
	route.Ready.Store(true)
	a.table.Add(route)

	req := httptest.NewRequest("GET", "http://face.vibe/", nil)
	req.Host = "face.vibe"
	rec := httptest.NewRecorder()
	a.routeRequest(rec, req)
	if rec.Body.String() != "local face" {
		t.Fatalf("local route must shadow the peer route, got %q", rec.Body.String())
	}
}

func TestPeerUnreachableServesErrorPageNotBare502(t *testing.T) {
	a, b := peerABWithBackend(t)
	b.peerLn.Close()

	req := httptest.NewRequest("GET", "http://face.vibe/", nil)
	req.Host = "face.vibe"
	rec := httptest.NewRecorder()
	a.routeRequest(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("unreachable peer: code=%d, want 502", rec.Code)
	}
	body := rec.Body.String()
	if body == "" || !strings.Contains(body, "vibe peers") || !strings.Contains(body, "face") {
		t.Fatalf("bare or unhelpful 502 body: %q", body)
	}
}

func TestUnknownHostStillDashboardWhenPeersDisabled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Daemon.Port = 0
	cfg.Daemon.TLS.Enabled = false
	s := NewServer(cfg)
	s.ConfigDir = t.TempDir()

	req := httptest.NewRequest("GET", "http://face.vibe/", nil)
	req.Host = "face.vibe"
	rec := httptest.NewRecorder()
	s.routeRequest(rec, req)
	body, _ := io.ReadAll(rec.Body)
	// The unknown-host fallback is the dashboard; the peer branch must not
	// have created any state with the flag off.
	if !strings.Contains(string(body), "<html") && !strings.Contains(string(body), "<!DOCTYPE") && !strings.Contains(string(body), "<!doctype") {
		t.Fatalf("expected dashboard HTML, got %q", body[:min(len(body), 120)])
	}
	if len(s.peerStates) != 0 {
		t.Fatal("peer state created despite disabled flag")
	}
}
