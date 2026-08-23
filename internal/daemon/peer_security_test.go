package daemon

// Regression armor for the peer subsystem's security invariants. A failure
// in this file is a real bug in production code — fix the code, never relax
// the test.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/graiz/local.vibe/internal/peer"
)

// A peer route's host must NOT be a trusted origin for A's API: pages under
// face.vibe are authored by another machine, and a compromised peer must not
// drive the register-executes-shell API. This holds because peer routes never
// enter s.table — this test pins that consequence down so a future refactor
// (e.g. materializing peer routes as table entries) trips it loudly.
func TestPeerRouteHostNotTrustedOrigin(t *testing.T) {
	a, _ := peerABWithBackend(t)
	if _, _, ok := a.findPeerRoute("face"); !ok {
		t.Fatal("setup: peer route not resolvable")
	}
	if a.originTrusted("http://face.vibe") {
		t.Fatal("peer-route origin trusted by the API — cross-machine escalation")
	}
	// Sanity: a local sticky route IS trusted (existing behavior unchanged).
	a.table.Add(&Route{Name: "mine", Type: RouteSticky, Port: 1234, RegisteredAt: time.Now()})
	if !a.originTrusted("http://mine.vibe") {
		t.Fatal("local sticky route origin no longer trusted — regression")
	}
}

// The peer listener must never expose daemon state to an unpaired caller,
// even one that completes a handshake during an open invite window.
func TestInviteWindowExposesOnlyPairing(t *testing.T) {
	b := newBareServer(t)
	if _, _, err := b.openPeerInvite(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.peerLn.Close() })
	b.table.Add(&Route{Name: "face", Type: RouteSticky, Port: 1, RegisteredAt: time.Now()})

	strangerID, err := peer.EnsureIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := peerHTTPClient(strangerID, b.peerFP)
	addr := b.peerLn.Addr().String()

	get := func(path, host string) int {
		req, err := http.NewRequest("GET", "https://"+addr+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if host != "" {
			req.Host = host
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}

	if code := get("/peer/routes", ""); code != http.StatusForbidden {
		t.Fatalf("unpinned GET /peer/routes during invite: got %d, want 403", code)
	}
	if code := get("/_api/routes", ""); code != http.StatusNotFound {
		t.Fatalf("unpinned GET /_api/routes during invite: got %d, want 404 blackhole", code)
	}
	if code := get("/", "face.vibe"); code != http.StatusForbidden {
		t.Fatalf("unpinned route request during invite: got %d, want 403", code)
	}
}

// State-changing peer API endpoints on the LOOPBACK listener are covered by
// the cross-site guard: a browser POST with a foreign Origin is rejected.
func TestPeerAPICrossSiteBlocked(t *testing.T) {
	s := newBareServer(t)
	req := httptest.NewRequest("POST", "http://127.0.0.1:7999/_api/peers/invite", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	s.apiHandler(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST /_api/peers/invite: got %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "cross-site") {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}
