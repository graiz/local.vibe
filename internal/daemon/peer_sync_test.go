package daemon

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/graiz/local.vibe/internal/peer"
)

func portOf(t *testing.T, rawURL string) int {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

// mustClientIdentity loads a server's own peer identity from its certs dir —
// same files EnsureIdentity wrote, so the same cert the peer pinned.
func mustClientIdentity(t *testing.T, s *Server) tls.Certificate {
	t.Helper()
	id, err := peer.EnsureIdentity(s.tlsCertsDir())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// pairAB opens an invite on b, pairs a to it, and returns b's listener
// host/port. Callers get a fully mutual pairing exactly as production
// produces it.
func pairAB(t *testing.T, a, b *Server) (string, int) {
	t.Helper()
	code, _, err := b.openPeerInvite()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.peerLn.Close() })
	host, port := splitAddr(t, b.peerLn.Addr().String())
	if err := a.ensurePeerIdentity(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.pairWithPeer(host, port, code); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if a.peerLn != nil {
			a.peerLn.Close()
		}
	})
	return host, port
}

func TestPeerSyncAndServeRoute(t *testing.T) {
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

	// findPeerRoute's miss path forces the refresh sweep.
	p, sum, ok := a.findPeerRoute("face")
	if !ok {
		t.Fatal("face not found via peer cache after refresh")
	}
	if p.Fingerprint != b.peerFP || sum.Name != "face" {
		t.Fatalf("wrong resolution: peer=%+v summary=%+v", p, sum)
	}
	// SANs must now include the peer route.
	found := false
	for _, h := range a.tlsHostnames() {
		if h == "face.vibe" {
			found = true
		}
	}
	if !found {
		t.Fatalf("face.vibe missing from tlsHostnames: %v", a.tlsHostnames())
	}
	// B's peer listener serves the route by Host to a pinned client.
	c := peerHTTPClient(mustClientIdentity(t, a), b.peerFP)
	req, err := http.NewRequest("GET", "https://"+b.peerLn.Addr().String()+"/anything", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "face.vibe"
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "hello from face" {
		t.Fatalf("proxied body = %q (status %d)", body, resp.StatusCode)
	}
}

func TestPeerSyncKeepsStaleCacheOnFailure(t *testing.T) {
	b := newBareServer(t)
	a := newBareServer(t)
	pairAB(t, a, b)
	route := &Route{Name: "face", Type: RouteSticky, Port: 1, RegisteredAt: time.Now()}
	b.table.Add(route)

	if _, _, ok := a.findPeerRoute("face"); !ok {
		t.Fatal("cache not populated")
	}

	// Kill B's listener, then force a refresh: the stale cache must survive
	// and the failure must be recorded.
	b.peerLn.Close()
	a.refreshAllPeers(true)

	p, _, ok := a.peerRouteFromCache("face")
	if !ok {
		t.Fatal("stale cache dropped on refresh failure")
	}
	st := a.peerStateFor(p.Name)
	a.peerMu.Lock()
	lastErr := st.lastErr
	a.peerMu.Unlock()
	if lastErr == "" {
		t.Fatal("refresh failure not recorded in lastErr")
	}
}

func TestPeerServeRouteStoppedManagedServesStartingPage(t *testing.T) {
	b := newBareServer(t)
	a := newBareServer(t)
	pairAB(t, a, b)
	// A managed route with no live process and no cmd (nothing to spawn):
	// remote viewers must get the static self-refreshing page, not the
	// interactive start page and not a proxy attempt.
	b.table.Add(&Route{Name: "app", Type: RouteManaged, Port: 1, RegisteredAt: time.Now()})

	c := peerHTTPClient(mustClientIdentity(t, a), b.peerFP)
	req, err := http.NewRequest("GET", "https://"+b.peerLn.Addr().String()+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "app.vibe"
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("stopped managed route: got %d, want 503", resp.StatusCode)
	}
	if !containsAll(string(body), `http-equiv="refresh"`, "app") {
		t.Fatalf("starting page missing refresh/name: %q", body)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
