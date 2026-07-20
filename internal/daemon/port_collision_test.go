package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestHandleStartReportsOAuthPortCollision reproduces the apply/edge bug: route
// `apply`'s primary port equals route `edge`'s oauth_callback_port. Starting
// apply must return a clear 409 naming the conflict, not a generic failure (and
// not — pre-fix — a daemon-killing port preflight).
func TestHandleStartReportsOAuthPortCollision(t *testing.T) {
	s := testServer()
	s.ConfigDir = t.TempDir()

	edge := &Route{Name: "edge", Type: RouteManaged, Port: 3005, OAuthCallbackPort: 3000, RegisteredAt: time.Now()}
	s.table.Add(edge)
	apply := &Route{Name: "apply", Type: RouteManaged, Port: 3000, Cmd: "npm run dev", RegisteredAt: time.Now()}
	s.table.Add(apply)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/_api/routes/apply/start", nil)
	req.Header.Set("Accept", "application/json")
	s.handleStart(rec, req, "apply")

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 Conflict; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "3000") || !strings.Contains(body, "edge") || !strings.Contains(body, "oauth_callback_port") {
		t.Errorf("message not clear about the collision: %s", body)
	}
	// The process must never have been started.
	if apply.Running.Load() {
		t.Error("apply should not be running after a collision-blocked start")
	}
}

// TestVibePortClaimSources covers each kind of vibe-internal port claim.
func TestVibePortClaimSources(t *testing.T) {
	s := testServer() // daemon Port defaults to 0 in testServer; set them explicitly
	s.cfg.Daemon.Port = 7999
	s.cfg.Daemon.TLS.Enabled = true
	s.cfg.Daemon.TLS.Port = 7443
	s.table.Add(&Route{Name: "other", Type: RouteManaged, Port: 4001, OAuthCallbackPort: 4002, ReservePorts: map[string]int{"api": 4003}, RegisteredAt: time.Now()})

	cases := map[int]string{
		7999: "HTTP port",
		7443: "HTTPS port",
		4001: `route "other"`,
		4002: "oauth_callback_port",
		4003: "reserve_ports",
	}
	for port, want := range cases {
		got := s.vibePortClaim("self", port)
		if !strings.Contains(got, want) {
			t.Errorf("vibePortClaim(%d) = %q; want it to mention %q", port, got, want)
		}
	}

	// A free port and the route's own ports must not be flagged.
	if msg := s.vibePortClaim("self", 4099); msg != "" {
		t.Errorf("free port flagged: %q", msg)
	}
	if msg := s.vibePortClaim("other", 4001); msg != "" {
		t.Errorf("route flagged against itself: %q", msg)
	}
}

// TestReservePortsClaimSharesVibePortClaim verifies the register/sync reserve
// scanner is unified with vibePortClaim: it flags a reserve value colliding with
// another route's port AND with the daemon's own listener (which the old
// standalone reservePortConflictsWith missed), and returns "" for a clean set.
func TestReservePortsClaimSharesVibePortClaim(t *testing.T) {
	s := testServer()
	s.cfg.Daemon.Port = 7999
	s.table.Add(&Route{Name: "other", Type: RouteManaged, Port: 4001, RegisteredAt: time.Now()})

	// Collision with another route's primary port.
	if msg := s.reservePortsClaim("mine", map[string]int{"api": 4001}); !strings.Contains(msg, "other") {
		t.Errorf("reserve collision with another route not caught: %q", msg)
	}
	// Collision with the daemon's own HTTP port — the case the old scanner missed.
	if msg := s.reservePortsClaim("mine", map[string]int{"api": 7999}); !strings.Contains(msg, "HTTP port") {
		t.Errorf("reserve collision with daemon port not caught: %q", msg)
	}
	// A clean set is not flagged, and the route isn't flagged against itself.
	if msg := s.reservePortsClaim("other", map[string]int{"api": 4090}); msg != "" {
		t.Errorf("clean reserve set flagged: %q", msg)
	}
}
