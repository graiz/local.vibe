package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestOriginPortIsCheckedOnVibeHosts closes the drive-by RCE hole left open by
// the .vibe branches of originTrusted ignoring the port. Every *.<tld> name
// resolves to 127.0.0.1, so http://local.<tld>:<any> reaches whatever
// unrelated service holds that port; such a page is schemefully same-site with
// the dashboard, so Sec-Fetch-Site does not block it either. If its origin is
// trusted, any loopback port that renders attacker-controlled HTML can POST to
// /_api/routes and get arbitrary cmd execution.
func TestOriginPortIsCheckedOnVibeHosts(t *testing.T) {
	s := testServer()
	s.cfg.Daemon.Port = 7999
	s.cfg.Daemon.TLS.Enabled = true
	s.cfg.Daemon.TLS.Port = 7443
	s.table.Add(&Route{Name: "app", Type: RouteManaged, Port: 3000, RegisteredAt: time.Now()})

	untrusted := []string{
		"http://local.test:31337",
		"https://local.test:31337",
		"http://app.test:31337",
		"http://app.test:3000", // the route's own dev port is NOT a daemon port
	}
	for _, o := range untrusted {
		if s.originTrusted(o) {
			t.Errorf("originTrusted(%q) = true; a non-daemon loopback port must never be trusted", o)
		}
	}

	trusted := []string{
		"http://local.test",       // :80 via the privileged-port redirect
		"https://local.test",      // :443 via the redirect
		"http://local.test:7999",  // daemon HTTP port
		"https://local.test:7443", // daemon TLS port
		"http://app.test:7999",
	}
	for _, o := range trusted {
		if !s.originTrusted(o) {
			t.Errorf("originTrusted(%q) = false; vibe's own UI surfaces must stay trusted", o)
		}
	}
}

// TestCrossSiteGuardRejectsForeignLoopbackPort exercises the hole end to end
// through the real API handler, as the attack would arrive.
func TestCrossSiteGuardRejectsForeignLoopbackPort(t *testing.T) {
	s := testServer()
	s.cfg.Daemon.Port = 7999
	s.ConfigDir = t.TempDir()

	body := `{"name":"pwned","cmd":"touch /tmp/pwned","port":3999}`
	req := httptest.NewRequest(http.MethodPost, "/_api/routes", strings.NewReader(body))
	req.Header.Set("Content-Type", "text/plain") // CORS simple request: no preflight
	req.Header.Set("Origin", "http://local.test:31337")
	req.Header.Set("Sec-Fetch-Site", "same-site")
	req.Host = "local.test"
	rec := httptest.NewRecorder()

	s.apiHandler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("register from a foreign loopback port returned %d, want 403 — "+
			"this is drive-by remote code execution", rec.Code)
	}
	if _, ok := s.table.Get("pwned"); ok {
		t.Fatal("route was registered from an untrusted origin")
	}
}
