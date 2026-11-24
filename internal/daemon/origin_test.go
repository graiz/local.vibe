package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The daemon API is unauthenticated and reachable at 127.0.0.1:<port> and on
// every .vibe host, so any web page the user visits can issue a cross-origin
// POST. POST /_api/routes takes a `cmd` that is executed through a login
// shell, which made drive-by RCE possible. These tests pin the origin guard
// that closes it.
func TestAPIRejectsCrossSiteStateChange(t *testing.T) {
	s := testServer(t)
	s.ConfigDir = t.TempDir()

	post := func(body string, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/_api/routes", strings.NewReader(body))
		req.Host = "local.test"
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		s.apiHandler(w, req)
		return w
	}

	// The attack: a page on evil.com POSTs a route whose cmd is arbitrary.
	// A `text/plain` body avoids a CORS preflight, so the browser sends it.
	w := post(`{"name":"pwned","cmd":"touch /tmp/pwned"}`, map[string]string{
		"Origin":       "https://evil.com",
		"Content-Type": "text/plain",
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("cross-origin register = %d; want 403", w.Code)
	}
	if _, ok := s.table.Get("pwned"); ok {
		t.Fatalf("cross-origin request created a route — RCE surface still open")
	}

	// Same, signalled via Sec-Fetch-Site instead of Origin.
	w = post(`{"name":"pwned2","cmd":"true"}`, map[string]string{"Sec-Fetch-Site": "cross-site"})
	if w.Code != http.StatusForbidden {
		t.Errorf("Sec-Fetch-Site cross-site = %d; want 403", w.Code)
	}
	if _, ok := s.table.Get("pwned2"); ok {
		t.Errorf("cross-site request created a route")
	}

	// Opaque origin (sandboxed iframe, file://) is not trusted.
	w = post(`{"name":"pwned3","cmd":"true"}`, map[string]string{"Origin": "null"})
	if w.Code != http.StatusForbidden {
		t.Errorf("null origin = %d; want 403", w.Code)
	}

	// A bookmark route proxies a third-party site; its origin must not be
	// able to drive the API even though it ends in the vibe TLD.
	bm := &Route{Name: "ha", Type: RouteBookmark, ExternalURL: "https://example.com", Proxy: true, RegisteredAt: time.Now()}
	s.table.Add(bm)
	w = post(`{"name":"pwned4","cmd":"true"}`, map[string]string{"Origin": "https://ha.test"})
	if w.Code != http.StatusForbidden {
		t.Errorf("bookmark-host origin = %d; want 403", w.Code)
	}
}

func TestAPIAllowsFirstPartyAndCLI(t *testing.T) {
	s := testServer(t)
	s.ConfigDir = t.TempDir()

	// A managed route whose start page legitimately calls the API from its
	// own .vibe origin.
	s.table.Add(&Route{Name: "app", Type: RouteManaged, Port: 4100, Cmd: "x", RegisteredAt: time.Now()})

	put := func(headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/_api/preferences", strings.NewReader(`{"view":"grid"}`))
		req.Host = "local.test"
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		s.apiHandler(w, req)
		return w
	}

	for _, tc := range []struct {
		name    string
		headers map[string]string
	}{
		{"CLI over socket/TCP sends no Origin", nil},
		{"dashboard", map[string]string{"Origin": "https://local.test", "Sec-Fetch-Site": "same-origin"}},
		{"user-initiated navigation", map[string]string{"Sec-Fetch-Site": "none"}},
	} {
		if w := put(tc.headers); w.Code != http.StatusOK {
			t.Errorf("%s: code = %d; want 200 (body %s)", tc.name, w.Code, w.Body.String())
		}
	}
}

// The service worker on local.vibe probes the daemon's HTTP port to tell
// "redirect down" from "daemon down". That fetch is cross-origin by
// construction, so read-only GETs must stay reachable.
func TestAPIAllowsCrossSiteReads(t *testing.T) {
	s := testServer(t)
	s.ConfigDir = t.TempDir()

	req := httptest.NewRequest(http.MethodGet, "/_api/health", nil)
	req.Host = "local.test"
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	s.apiHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("cross-site health probe = %d; want 200 (service-worker fallback depends on it)", w.Code)
	}
}

func TestOriginTrusted(t *testing.T) {
	s := testServer(t) // TLD "test", daemon port 0
	s.table.Add(&Route{Name: "app", Type: RouteManaged, Port: 4200, Cmd: "x", RegisteredAt: time.Now()})
	s.table.Add(&Route{Name: "bm", Type: RouteBookmark, ExternalURL: "https://x.example", RegisteredAt: time.Now()})

	cases := map[string]bool{
		"https://local.test":          true,  // dashboard
		"http://local.test":           true,  // dashboard over plain HTTP
		"https://app.test":            true,  // managed route's own pages
		"https://bm.test":             false, // bookmark proxies a third party
		"https://unknown.test":        false, // not a registered route
		"https://evil.com":            false,
		"https://local.test.evil.com": false, // suffix-confusion attempt
		"null":                        false,
		"":                            false,
		"http://127.0.0.1:0":          true,  // daemon's own listener (port 0 in tests)
		"http://127.0.0.1:3000":       false, // some other local dev server
		"http://localhost":            true,  // :80 is redirected to the daemon
	}
	for origin, want := range cases {
		if got := s.originTrusted(origin); got != want {
			t.Errorf("originTrusted(%q) = %v; want %v", origin, got, want)
		}
	}
}

// A managed/sticky route's pages run on its own .vibe origin and legitimately
// drive that route (start, stop, repair, the edit_cmd recovery's PUT, the
// worktree picker). That origin is also every third-party script the app
// loads, so it gets exactly those endpoints for exactly that route — never
// register (shell execution), another route, preferences, or peers.
func TestRouteOriginScopedToOwnRoute(t *testing.T) {
	s := testServer(t)
	s.ConfigDir = t.TempDir()
	s.table.Add(&Route{Name: "app", Type: RouteManaged, Port: 4100, Cmd: "x", RegisteredAt: time.Now()})
	s.table.Add(&Route{Name: "other", Type: RouteManaged, Port: 4200, Cmd: "x", RegisteredAt: time.Now()})

	call := func(method, path, body string) int {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Host = "app.test"
		req.Header.Set("Origin", "https://app.test")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		s.apiHandler(w, req)
		return w.Code
	}

	allowed := []struct{ method, path, body string }{
		{http.MethodPost, "/_api/routes/app/stop", ""},
		{http.MethodDelete, "/_api/routes/app/stop", ""},
		{http.MethodPost, "/_api/routes/app/repair", ""},
		{http.MethodPut, "/_api/routes/app", `{"idle_timeout":5}`},
		{http.MethodPost, "/_api/routes/app/worktrees", `{"path":"/nowhere"}`},
	}
	for _, c := range allowed {
		if code := call(c.method, c.path, c.body); code == http.StatusForbidden {
			t.Errorf("%s %s from the route's own origin = 403; want allowed", c.method, c.path)
		}
	}
	denied := []struct{ method, path, body string }{
		{http.MethodPost, "/_api/routes", `{"name":"pwned","cmd":"true"}`},
		{http.MethodPost, "/_api/routes/other/start", ""},
		{http.MethodPost, "/_api/routes/other/stop", ""},
		{http.MethodPut, "/_api/routes/other", `{"idle_timeout":5}`},
		{http.MethodDelete, "/_api/routes/other", ""},
		{http.MethodDelete, "/_api/routes/app", ""},
		{http.MethodPut, "/_api/preferences", `{"view":"grid"}`},
		{http.MethodPost, "/_api/peers/invite", ""},
	}
	for _, c := range denied {
		if code := call(c.method, c.path, c.body); code != http.StatusForbidden {
			t.Errorf("%s %s from another route's origin = %d; want 403", c.method, c.path, code)
		}
	}
	if _, ok := s.table.Get("pwned"); ok {
		t.Fatal("route origin registered a route")
	}
	if _, ok := s.table.Get("other"); !ok {
		t.Fatal("route origin deregistered another route")
	}
}

// Host parsing is shared by the request router, the API passthrough, the
// dashboard, the peer listener and the origin gate; it must handle an IPv6
// literal, which the old strings.Index(host, ":") strip mangled into "[".
func TestRouteNameFromHost(t *testing.T) {
	s := testServer(t) // TLD "test"
	cases := map[string]struct {
		name string
		ok   bool
	}{
		"app.test":          {"app", true},
		"app.test:7999":     {"app", true},
		"feat.app.test":     {"feat.app", true},
		"local.test":        {"local", true},
		"[::1]:7999":        {"", false},
		"127.0.0.1:7999":    {"", false},
		"evil.com":          {"", false},
		"app.test.evil.com": {"", false},
		"APP.TEST":          {"app", true},
	}
	for host, want := range cases {
		name, ok := s.routeNameFromHost(host)
		if ok != want.ok || name != want.name {
			t.Errorf("routeNameFromHost(%q) = (%q, %v); want (%q, %v)", host, name, ok, want.name, want.ok)
		}
	}
}
