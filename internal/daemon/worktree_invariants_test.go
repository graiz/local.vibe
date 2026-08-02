//go:build !windows

package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// putRoute issues a PUT /_api/routes/{name} and returns the recorder.
func putRoute(t *testing.T, s *Server, name, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/_api/routes/"+name, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "local.test"
	w := httptest.NewRecorder()
	s.apiHandler(w, req)
	return w
}

// TestUpdateEnforcesWorktreeInvariants pins the invariants that register
// enforces but handleUpdate historically did not. Each of these produced a
// route whose behavior changed silently on the next daemon restart, because
// Parent is recomputed from the name at load rather than stored.
func TestUpdateEnforcesWorktreeInvariants(t *testing.T) {
	s := testServer()
	s.ConfigDir = t.TempDir()
	s.table.Add(&Route{
		Name: "feat.app", Parent: "app", Type: RouteManaged,
		Port: 3300, Cmd: "sleep 30", Dir: t.TempDir(), RegisteredAt: time.Now(),
	})

	cases := []struct {
		what string
		body string
	}{
		// A single-label rename passes validName but strands Parent set.
		{"rename off the dotted name", `{"name":"feat"}`},
		{"convert to bookmark", `{"url":"https://example.com"}`},
		{"set an oauth bridge port", `{"oauth_callback_port":8123}`},
	}
	for _, c := range cases {
		w := putRoute(t, s, "feat.app", c.body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: PUT = %d, want 400 (body: %s)", c.what, w.Code, w.Body.String())
		}
	}
	// The route must be untouched by the rejected updates.
	rt, ok := s.table.Get("feat.app")
	if !ok {
		t.Fatal("worktree route disappeared after rejected updates")
	}
	if rt.Parent != "app" || rt.ExternalURL != "" || rt.OAuthCallbackPort != 0 {
		t.Errorf("route mutated by a rejected update: %+v", rt)
	}
}

// TestRegisterRejectsNonWorktreeDir covers the accept-then-destroy path: a
// dotted name makes every request run the worktree prune, so registering one
// against a directory that isn't a worktree used to succeed, spawn a child,
// and then deregister itself and kill that child on the first visit.
func TestRegisterRejectsNonWorktreeDir(t *testing.T) {
	s := testServer()
	s.ConfigDir = t.TempDir()

	w := postRegister(t, s, `{"name":"feat.app","cmd":"sleep 30","dir":"`+t.TempDir()+`"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("register into a non-worktree dir = %d, want 400 (body: %s)", w.Code, w.Body.String())
	}
	if _, ok := s.table.Get("feat.app"); ok {
		t.Error("route was registered despite an invalid worktree dir")
	}
}

// TestRegisterRejectsPrimaryPortHeldByAnotherRoute covers the half of the
// killPort guard that lived on the register path: without a vibePortClaim on
// the PRIMARY port, a register whose port belongs to another running managed
// route fell through to preflightPort → killPort and SIGTERM'd that route's
// server instead of reporting the conflict.
func TestRegisterRejectsPrimaryPortHeldByAnotherRoute(t *testing.T) {
	s := testServer()
	s.ConfigDir = t.TempDir()
	s.table.Add(&Route{
		Name: "other", Type: RouteManaged, Port: 3410, Cmd: "sleep 30", RegisteredAt: time.Now(),
	})

	w := postRegister(t, s, `{"name":"newapp","cmd":"sleep 30","port":3410}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("register onto another route's port = %d, want 409 (body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "other") {
		t.Errorf("conflict message should name the owning route, got: %s", w.Body.String())
	}
}
