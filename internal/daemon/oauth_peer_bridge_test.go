package daemon

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/graiz/local.vibe/internal/peer"
)

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// noRedirectClient never follows redirects, so a test can assert the bridge
// did NOT auto-forward.
var noRedirectClient = &http.Client{
	Timeout:       3 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func seedPeerRoutes(s *Server, routes ...peer.RouteSummary) peer.Peer {
	p := peer.Peer{Name: "peerbox", Host: "127.0.0.1", Port: 7444, Fingerprint: "fp-peerbox", AddedAt: time.Now()}
	s.peerList = append(s.peerList, p)
	s.peerStates[p.Name] = &peerState{lastOK: time.Now(), routes: routes}
	return p
}

// The bug this exists for: a peer route with oauth_callback_port N had its
// bridge only on the peer's loopback, so the provider's redirect to
// http://localhost:N on the browsing machine dead-ended. The viewer must
// learn N through sync, bind its own bridge, and offer — never auto-perform —
// the hop to the route's .vibe origin.
func TestPeerOAuthBridgeEndToEnd(t *testing.T) {
	b := newBareServer(t)
	a := newBareServer(t)
	pairAB(t, a, b)

	cbPort := freeLoopbackPort(t)
	route := &Route{Name: "billing", Type: RouteSticky, Port: 1, OAuthCallbackPort: cbPort, RegisteredAt: time.Now()}
	b.table.Add(route) // no reconcile on b: both daemons share this host's loopback
	t.Cleanup(a.stopOAuthBridgeListeners)

	a.refreshAllPeers(true)
	_, sum, ok := a.findPeerRoute("billing")
	if !ok || sum.OAuthCallbackPort != cbPort {
		t.Fatalf("summary did not carry oauth_callback_port: ok=%v sum=%+v", ok, sum)
	}

	resp, err := noRedirectClient.Get(fmt.Sprintf("http://127.0.0.1:%d/auth/google/callback?state=%%252F&code=abc", cbPort))
	if err != nil {
		t.Fatalf("viewer bridge not listening: %v", err)
	}
	defer resp.Body.Close()
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Location") != "" {
		t.Fatalf("bridge must render a confirmation page, got %d Location=%q", resp.StatusCode, resp.Header.Get("Location"))
	}
	peerName := a.peerList[0].Name
	if !containsAll(body, "billing", peerName, `href="http://billing.vibe/auth/google/callback?state=%252F&amp;code=abc"`) {
		t.Fatalf("confirmation page missing route/peer/target:\n%s", body)
	}
	if resp.Header.Get("X-Frame-Options") != "DENY" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("missing anti-framing / no-store headers: %v", resp.Header)
	}

	// Removing the peer closes its bridge.
	rec := httptest.NewRecorder()
	a.handlePeerRemove(rec, nil, peerName)
	if rec.Code != http.StatusOK {
		t.Fatalf("peer remove: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := noRedirectClient.Get(fmt.Sprintf("http://127.0.0.1:%d/", cbPort)); err == nil {
		t.Fatal("bridge still listening after its peer was removed")
	}
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return sb.String()
}

func peerBridgeRequest(t *testing.T, s *Server, port int, method, target string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	rec := httptest.NewRecorder()
	s.handleOAuthCallbackBridge(rec, req, port)
	return rec
}

func TestPeerOAuthBridgeWorktreeState(t *testing.T) {
	s := newBareServer(t)
	seedPeerRoutes(s,
		peer.RouteSummary{Name: "billing", Type: "managed", OAuthCallbackPort: 8787},
		peer.RouteSummary{Name: "wt.billing", Type: "managed"},
		peer.RouteSummary{Name: "other", Type: "managed"},
	)

	tagged := url.QueryEscape(wrapOAuthState("wt.billing", "orig"))
	body := peerBridgeRequest(t, s, 8787, "GET", "http://localhost:8787/cb?code=c&state="+tagged, nil).Body.String()
	if !strings.Contains(body, `href="http://wt.billing.vibe/cb?code=c&amp;state=orig"`) {
		t.Fatalf("worktree tag not unwrapped to its origin:\n%s", body)
	}

	// A tag naming a route that isn't a worktree of the bridge's owner is
	// left alone: the callback still goes to the owner, state untouched.
	evil := url.QueryEscape(wrapOAuthState("other", "orig"))
	body = peerBridgeRequest(t, s, 8787, "GET", "http://localhost:8787/cb?state="+evil, nil).Body.String()
	if !strings.Contains(body, `href="http://billing.vibe/cb?state=`+evil+`"`) {
		t.Fatalf("foreign tag must not redirect elsewhere:\n%s", body)
	}
}

// form_post callbacks (Apple, some OIDC providers) arrive as a POST; the page
// replays the fields in a form the user submits.
func TestPeerOAuthBridgeFormPost(t *testing.T) {
	s := newBareServer(t)
	seedPeerRoutes(s, peer.RouteSummary{Name: "billing", Type: "managed", OAuthCallbackPort: 8787})
	rec := peerBridgeRequest(t, s, 8787, "POST", "http://localhost:8787/cb", url.Values{"code": {"c<1>"}, "state": {"s"}})
	body := rec.Body.String()
	if !containsAll(body, `method="post"`, `action="http://billing.vibe/cb"`, `name="code" value="c&lt;1&gt;"`, `name="state" value="s"`) {
		t.Fatalf("form_post replay form wrong:\n%s", body)
	}
}

func TestPeerOAuthBridgesLocalWins(t *testing.T) {
	s := newBareServer(t)
	seedPeerRoutes(s,
		peer.RouteSummary{Name: "billing", OAuthCallbackPort: 8787},
		peer.RouteSummary{Name: "shadowed", OAuthCallbackPort: 8788},
		peer.RouteSummary{Name: "clash", OAuthCallbackPort: 3005},
	)
	second := peer.Peer{Name: "second", Fingerprint: "fp2"}
	s.peerList = append(s.peerList, second)
	s.peerStates["second"] = &peerState{routes: []peer.RouteSummary{{Name: "dup", OAuthCallbackPort: 8787}}}
	s.table.Add(&Route{Name: "shadowed", Type: RouteSticky, Port: 4100, RegisteredAt: time.Now()})
	s.table.Add(&Route{Name: "local", Type: RouteSticky, Port: 4101, ReservePorts: map[string]int{"api": 3005}, RegisteredAt: time.Now()})

	got := s.peerOAuthBridges()
	if b, ok := got[8787]; !ok || b.route != "billing" || b.peer.Name != "peerbox" {
		t.Fatalf("first-paired peer must win port ties: %+v", got)
	}
	if _, ok := got[8788]; ok {
		t.Fatal("peer route shadowed by a local route must not get a bridge")
	}
	if _, ok := got[3005]; ok {
		t.Fatal("port reserved by a local route must not be bridged for a peer")
	}

	s.cfg.Daemon.Peers.Enabled = false
	if len(s.peerOAuthBridges()) != 0 {
		t.Fatal("peer bridges must be off when peers are disabled")
	}
}

// preflightPort hands a peer-held port back to a local route, and peers stay
// off it while the local route comes up.
func TestPeerOAuthBridgeYieldsToLocalRoute(t *testing.T) {
	s := newBareServer(t)
	port := freeLoopbackPort(t)
	seedPeerRoutes(s, peer.RouteSummary{Name: "billing", OAuthCallbackPort: port})
	t.Cleanup(s.stopOAuthBridgeListeners)
	if err := s.reconcileOAuthBridgeListeners(); err != nil {
		t.Fatal(err)
	}
	if !s.isPortReady(port) {
		t.Fatal("peer bridge not bound")
	}
	if rec := s.preflightPort(port); rec != nil {
		t.Fatalf("preflight should have freed the port, got recovery %+v", rec)
	}
	if err := s.reconcileOAuthBridgeListeners(); err != nil {
		t.Fatal(err)
	}
	if s.isPortReady(port) {
		t.Fatal("peer bridge re-bound a port just yielded to a local route")
	}
}

func TestSanitizePeerSummariesOAuthPort(t *testing.T) {
	got := sanitizePeerSummaries([]peer.RouteSummary{
		{Name: "a", OAuthCallbackPort: 80},
		{Name: "b", OAuthCallbackPort: 70000},
		{Name: "c", OAuthCallbackPort: 8787},
	})
	if got[0].OAuthCallbackPort != 0 || got[1].OAuthCallbackPort != 0 || got[2].OAuthCallbackPort != 8787 {
		t.Fatalf("oauth ports not sanitized: %+v", got)
	}
}

// The serving side: post-login redirects to the bridge host must be pointed
// at the route's .vibe origin on the relay hop too, as on the local proxy.
func TestPeerServeRouteRewritesBridgeHostRedirect(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://localhost:8787/dashboard", http.StatusFound)
	}))
	t.Cleanup(backend.Close)
	b := newBareServer(t)
	a := newBareServer(t)
	pairAB(t, a, b)
	route := &Route{Name: "billing", Type: RouteSticky, Port: portOf(t, backend.URL), OAuthCallbackPort: 8787, RegisteredAt: time.Now()}
	route.Running.Store(true)
	route.Ready.Store(true)
	b.table.Add(route)

	c := peerHTTPClient(mustClientIdentity(t, a), b.peerFP)
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, _ := http.NewRequest("GET", "https://"+b.peerLn.Addr().String()+"/auth/done", nil)
	req.Host = "billing.vibe"
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "http://billing.vibe/dashboard" {
		t.Fatalf("relay did not rewrite bridge-host redirect: %q", loc)
	}
}

// `vibe peers` must say which localhost ports this machine holds for a peer:
// it's the only place a local server that hits EADDRINUSE on one can learn why.
func TestPeersListReportsOAuthBridges(t *testing.T) {
	s := newBareServer(t)
	port := freeLoopbackPort(t)
	seedPeerRoutes(s, peer.RouteSummary{Name: "billing", OAuthCallbackPort: port})
	t.Cleanup(s.stopOAuthBridgeListeners)
	if err := s.reconcileOAuthBridgeListeners(); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.handlePeersList(rec, httptest.NewRequest("GET", "/_api/peers", nil))
	want := fmt.Sprintf(`"oauth_bridges":[{"port":%d,"route":"billing","bound":true}]`, port)
	if !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("peers list missing %s:\n%s", want, rec.Body.String())
	}
}
