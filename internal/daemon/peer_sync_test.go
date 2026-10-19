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

// peerGetRoute requests host through a's pairing with b's peer listener.
func peerGetRoute(t *testing.T, a, b *Server, host string) (int, string) {
	t.Helper()
	c := peerHTTPClient(mustClientIdentity(t, a), b.peerFP)
	req, err := http.NewRequest("GET", "https://"+b.peerLn.Addr().String()+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, string(body)
}

// Remote viewers get a static 503 (never the interactive start page, never a
// proxy attempt). It promises an automatic retry only while this machine is
// actually bringing the route up.
func TestPeerServeRouteRemotePages(t *testing.T) {
	b := newBareServer(t)
	a := newBareServer(t)
	pairAB(t, a, b)

	// Managed, no live process and no cmd: nothing will ever start it.
	b.table.Add(&Route{Name: "app", Type: RouteManaged, Port: 1, RegisteredAt: time.Now()})
	// Sticky on a dark port: vibe has no process to start.
	b.table.Add(&Route{Name: "web", Type: RouteSticky, Port: 1, RegisteredAt: time.Now()})
	// Cases needing a live managed child are in peer_remote_unix_test.go.

	for _, c := range []struct {
		host        string
		wantRefresh bool
		wantText    string
	}{
		{"app.vibe", false, "isn't running"},
		{"web.vibe", false, "isn't running"},
	} {
		code, body := peerGetRoute(t, a, b, c.host)
		if code != http.StatusServiceUnavailable {
			t.Errorf("%s: got %d, want 503", c.host, code)
		}
		if got := strings.Contains(body, `http-equiv="refresh"`); got != c.wantRefresh {
			t.Errorf("%s: auto-refresh=%v, want %v: %q", c.host, got, c.wantRefresh, body)
		}
		if !strings.Contains(body, c.wantText) {
			t.Errorf("%s: missing %q: %q", c.host, c.wantText, body)
		}
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
