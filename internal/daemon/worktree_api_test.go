//go:build !windows

// Worktree registration tests spawn a real managed child (sleep) via the
// ProcessManager, so they're gated to non-Windows like api_test.go.

package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func postRegister(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/routes", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleRegister(w, req)
	return w
}

func TestRegisterWorktreeRejectsBadShapes(t *testing.T) {
	s := testServer()
	s.ConfigDir = t.TempDir()
	cases := []struct {
		body string
		want int
	}{
		{`{"name":"wt.app","url":"https://example.com"}`, http.StatusBadRequest},              // bookmark
		{`{"name":"wt.app","port":3000}`, http.StatusBadRequest},                              // no cmd
		{`{"name":"wt.app","cmd":"sleep 9","oauth_callback_port":8123}`, http.StatusBadRequest}, // oauth bridge
		{`{"name":"a.b.c","cmd":"sleep 9"}`, http.StatusBadRequest},                           // nesting
		{`{"name":"wt.local","cmd":"sleep 9"}`, http.StatusBadRequest},                        // reserved
	}
	for _, c := range cases {
		if w := postRegister(t, s, c.body); w.Code != c.want {
			t.Errorf("register %s = %d; want %d (body: %s)", c.body, w.Code, c.want, w.Body.String())
		}
	}
}

func TestRegisterWorktreeOverrides(t *testing.T) {
	s := testServer()
	s.ConfigDir = t.TempDir()

	// Port 3200 is what the copied vibe.json would claim; it must be ignored.
	// reserve_ports keeps its name but must get a fresh value.
	w := postRegister(t, s, `{"name":"feat.app","cmd":"sleep 30","port":3200,"dir":"`+t.TempDir()+`","reserve_ports":{"server":3201}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("register = %d: %s", w.Code, w.Body.String())
	}
	t.Cleanup(func() { _ = s.procs.Stop("feat.app") })

	var resp struct {
		Port int    `json:"port"`
		URL  string `json:"url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	route, ok := s.table.Get("feat.app")
	if !ok {
		t.Fatal("route missing after register")
	}
	if route.Parent != "app" {
		t.Errorf("Parent = %q; want \"app\"", route.Parent)
	}
	if route.Port == 3200 || route.Port == 0 {
		t.Errorf("Port = %d; want a fresh auto-assigned port, not the vibe.json value", route.Port)
	}
	if resp.Port != route.Port {
		t.Errorf("response port %d != route port %d", resp.Port, route.Port)
	}
	if !strings.Contains(resp.URL, "feat.app.test") {
		t.Errorf("URL = %q; want host feat.app.test", resp.URL)
	}
	if p := route.ReservePorts["server"]; p == 3201 || p == 0 {
		t.Errorf("reserve_ports[server] = %d; want fresh non-zero value", p)
	}
	if route.IdleTimeout != defaultWorktreeIdleMinutes {
		t.Errorf("IdleTimeout = %d; want default %d", route.IdleTimeout, defaultWorktreeIdleMinutes)
	}

	// An explicit idle_timeout wins over the worktree default.
	w2 := postRegister(t, s, `{"name":"feat2.app","cmd":"sleep 30","idle_timeout":5}`)
	if w2.Code != http.StatusOK {
		t.Fatalf("register feat2 = %d: %s", w2.Code, w2.Body.String())
	}
	t.Cleanup(func() { _ = s.procs.Stop("feat2.app") })
	r2, _ := s.table.Get("feat2.app")
	if r2.IdleTimeout != 5 {
		t.Errorf("explicit IdleTimeout = %d; want 5", r2.IdleTimeout)
	}
}
