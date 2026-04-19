package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestBookmarkProxyForwardsRequest verifies that a bookmark with Proxy=true
// reverse-proxies to the upstream (200 body forwarded, not a 307 redirect).
func TestBookmarkProxyForwardsRequest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", "ok")
		w.Write([]byte("hello from upstream"))
	}))
	defer upstream.Close()

	s := testServer()
	s.table.Add(&Route{
		Name:         "ext",
		Type:         RouteBookmark,
		ExternalURL:  upstream.URL,
		Proxy:        true,
		RegisteredAt: time.Now(),
	})

	req := httptest.NewRequest("GET", "/some/path", nil)
	req.Host = "ext.test"
	w := httptest.NewRecorder()
	s.routeRequest(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 (proxy forwarding); body: %s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != "hello from upstream" {
		t.Errorf("body = %q; want 'hello from upstream'", got)
	}
	if w.Header().Get("X-Upstream") != "ok" {
		t.Error("upstream response header missing; request did not proxy")
	}
}

// TestBookmarkProxySendsUpstreamHost ensures the upstream Host header
// matches the external URL's host, not the incoming .vibe host. Required
// for TLS SNI and virtual-hosted origins.
func TestBookmarkProxySendsUpstreamHost(t *testing.T) {
	var seenHost string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHost = r.Host
	}))
	defer upstream.Close()

	upstreamURL, _ := url.Parse(upstream.URL)

	s := testServer()
	s.table.Add(&Route{
		Name:         "ext",
		Type:         RouteBookmark,
		ExternalURL:  upstream.URL,
		Proxy:        true,
		RegisteredAt: time.Now(),
	})

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "ext.test"
	w := httptest.NewRecorder()
	s.routeRequest(w, req)

	if seenHost != upstreamURL.Host {
		t.Errorf("upstream saw Host = %q; want %q", seenHost, upstreamURL.Host)
	}
}

// TestBookmarkProxyOmitsXForwardedFor ensures the proxy does NOT advertise
// itself via X-Forwarded-For. Strict upstreams like Home Assistant return
// HTTP 400 when they see X-Forwarded-For from an untrusted proxy IP, and
// this proxy is intended to be transparent (make name.vibe feel like the
// real upstream), so the header should not appear on outgoing requests.
func TestBookmarkProxyOmitsXForwardedFor(t *testing.T) {
	var seenXFF string
	var seenXFFPresent bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenXFF, seenXFFPresent = r.Header.Get("X-Forwarded-For"), r.Header.Get("X-Forwarded-For") != ""
	}))
	defer upstream.Close()

	s := testServer()
	s.table.Add(&Route{
		Name:         "ext",
		Type:         RouteBookmark,
		ExternalURL:  upstream.URL,
		Proxy:        true,
		RegisteredAt: time.Now(),
	})

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "ext.test"
	req.RemoteAddr = "192.0.2.1:54321" // non-empty so ReverseProxy's XFF logic would trip
	w := httptest.NewRecorder()
	s.routeRequest(w, req)

	if seenXFFPresent {
		t.Errorf("upstream saw X-Forwarded-For = %q; want absent", seenXFF)
	}
}

// TestBookmarkProxyScrubsIncomingXForwardedFor confirms we don't propagate
// an X-Forwarded-For sent by the browser (defense in depth — browsers
// don't normally send it, but a custom client could).
func TestBookmarkProxyScrubsIncomingXForwardedFor(t *testing.T) {
	var seenXFF string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenXFF = r.Header.Get("X-Forwarded-For")
	}))
	defer upstream.Close()

	s := testServer()
	s.table.Add(&Route{
		Name:         "ext",
		Type:         RouteBookmark,
		ExternalURL:  upstream.URL,
		Proxy:        true,
		RegisteredAt: time.Now(),
	})

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "ext.test"
	req.Header.Set("X-Forwarded-For", "10.0.0.1")
	req.RemoteAddr = "192.0.2.1:54321"
	w := httptest.NewRecorder()
	s.routeRequest(w, req)

	if seenXFF != "" {
		t.Errorf("upstream saw X-Forwarded-For = %q; want absent (client-supplied value should be dropped)", seenXFF)
	}
}

// TestBookmarkProxyLandingPathRedirect verifies that a bookmark URL with a
// path (e.g. the "open this dashboard on load" case) 302-redirects root
// visits to that path while leaving other paths alone. Prevents the
// path-prefix-join bug where every asset request got /dashboard prepended.
func TestBookmarkProxyLandingPathRedirect(t *testing.T) {
	var seenPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
	}))
	defer upstream.Close()

	s := testServer()
	s.table.Add(&Route{
		Name:         "ext",
		Type:         RouteBookmark,
		ExternalURL:  upstream.URL + "/dashboard/office",
		Proxy:        true,
		RegisteredAt: time.Now(),
	})

	// Root → 302 to the landing path (not proxied yet).
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "ext.test"
	w := httptest.NewRecorder()
	s.routeRequest(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("root status = %d; want 302", w.Code)
	}
	if got := w.Header().Get("Location"); got != "/dashboard/office" {
		t.Errorf("root Location = %q; want /dashboard/office", got)
	}

	// Asset path → proxied through to origin with path unchanged.
	req = httptest.NewRequest("GET", "/frontend_latest/core.js", nil)
	req.Host = "ext.test"
	w = httptest.NewRecorder()
	s.routeRequest(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("asset status = %d; want 200", w.Code)
	}
	if seenPath != "/frontend_latest/core.js" {
		t.Errorf("upstream saw path = %q; want /frontend_latest/core.js (no prefix prepended)", seenPath)
	}

	// Landing path itself → proxied, not redirected in a loop.
	req = httptest.NewRequest("GET", "/dashboard/office", nil)
	req.Host = "ext.test"
	w = httptest.NewRecorder()
	s.routeRequest(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("landing path status = %d; want 200 (proxy, not redirect)", w.Code)
	}
	if seenPath != "/dashboard/office" {
		t.Errorf("upstream saw path = %q; want /dashboard/office", seenPath)
	}
}

// TestBookmarkProxyNoLandingPathWhenRoot confirms a bookmark URL without a
// meaningful path proxies root requests normally (no redirect loop).
func TestBookmarkProxyNoLandingPathWhenRoot(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("root ok"))
	}))
	defer upstream.Close()

	s := testServer()
	s.table.Add(&Route{
		Name:         "ext",
		Type:         RouteBookmark,
		ExternalURL:  upstream.URL, // no path
		Proxy:        true,
		RegisteredAt: time.Now(),
	})

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "ext.test"
	w := httptest.NewRecorder()
	s.routeRequest(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d; want 200 (no landing redirect)", w.Code)
	}
	if w.Body.String() != "root ok" {
		t.Errorf("body = %q; want 'root ok'", w.Body.String())
	}
}

// TestBookmarkProxyRewritesLocation verifies that same-origin absolute
// redirects from the upstream are rewritten to the .vibe host so the
// browser stays on the proxied domain.
func TestBookmarkProxyRewritesLocation(t *testing.T) {
	var upstreamURL string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, upstreamURL+"/after-login", http.StatusFound)
	}))
	defer upstream.Close()
	upstreamURL = upstream.URL

	s := testServer()
	s.table.Add(&Route{
		Name:         "ext",
		Type:         RouteBookmark,
		ExternalURL:  upstream.URL,
		Proxy:        true,
		RegisteredAt: time.Now(),
	})

	req := httptest.NewRequest("GET", "/login", nil)
	req.Host = "ext.test"
	w := httptest.NewRecorder()
	s.routeRequest(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d; want 302", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "http://ext.test/") {
		t.Errorf("Location = %q; want http://ext.test/... (rewritten to .vibe host)", loc)
	}
	if !strings.HasSuffix(loc, "/after-login") {
		t.Errorf("Location = %q; want path /after-login preserved", loc)
	}
}

// TestBookmarkProxyPreservesCrossOriginLocation leaves Location alone when
// the upstream redirects to a different origin — we only rewrite same-origin.
func TestBookmarkProxyPreservesCrossOriginLocation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://other.example.com/page", http.StatusFound)
	}))
	defer upstream.Close()

	s := testServer()
	s.table.Add(&Route{
		Name:         "ext",
		Type:         RouteBookmark,
		ExternalURL:  upstream.URL,
		Proxy:        true,
		RegisteredAt: time.Now(),
	})

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "ext.test"
	w := httptest.NewRecorder()
	s.routeRequest(w, req)

	if got := w.Header().Get("Location"); got != "https://other.example.com/page" {
		t.Errorf("Location = %q; cross-origin should be passed through unchanged", got)
	}
}

// TestBookmarkProxyStripsCookieDomain verifies that Domain= attributes in
// Set-Cookie headers are removed so the browser binds cookies to the
// .vibe host instead of dropping them.
func TestBookmarkProxyStripsCookieDomain(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "sid=abc; Domain=upstream.example.com; Path=/; HttpOnly")
		w.Header().Add("Set-Cookie", "pref=dark; Path=/")
	}))
	defer upstream.Close()

	s := testServer()
	s.table.Add(&Route{
		Name:         "ext",
		Type:         RouteBookmark,
		ExternalURL:  upstream.URL,
		Proxy:        true,
		RegisteredAt: time.Now(),
	})

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "ext.test"
	w := httptest.NewRecorder()
	s.routeRequest(w, req)

	cookies := w.Result().Header.Values("Set-Cookie")
	if len(cookies) != 2 {
		t.Fatalf("Set-Cookie count = %d; want 2", len(cookies))
	}
	for _, c := range cookies {
		if strings.Contains(strings.ToLower(c), "domain=") {
			t.Errorf("Set-Cookie = %q; Domain attribute should be stripped", c)
		}
	}
	// Attributes other than Domain are preserved.
	joined := strings.Join(cookies, " | ")
	if !strings.Contains(joined, "HttpOnly") {
		t.Errorf("HttpOnly attribute lost: %s", joined)
	}
	if !strings.Contains(joined, "sid=abc") || !strings.Contains(joined, "pref=dark") {
		t.Errorf("cookie values lost: %s", joined)
	}
}

// TestBookmarkNoProxyStillRedirects is the regression guard — bookmarks
// without Proxy set continue to 307-redirect to their ExternalURL.
func TestBookmarkNoProxyStillRedirects(t *testing.T) {
	s := testServer()
	s.table.Add(&Route{
		Name:         "ext",
		Type:         RouteBookmark,
		ExternalURL:  "https://example.com/docs",
		RegisteredAt: time.Now(),
	})

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "ext.test"
	w := httptest.NewRecorder()
	s.routeRequest(w, req)

	if w.Code != http.StatusTemporaryRedirect {
		t.Errorf("status = %d; want 307", w.Code)
	}
	if got := w.Header().Get("Location"); got != "https://example.com/docs" {
		t.Errorf("Location = %q; want https://example.com/docs", got)
	}
}

// TestAPIProxyRequiresURL — API rejects proxy=true without a url because
// the proxy target comes from ExternalURL.
func TestAPIProxyRequiresURL(t *testing.T) {
	s := testServer()
	body, _ := json.Marshal(map[string]any{"name": "bad", "port": 3000, "proxy": true})
	req := httptest.NewRequest("POST", "/_api/routes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.apiHandler(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 when proxy=true without url", w.Code)
	}
}

// TestAPIProxyBookmarkRoundtrip registers a proxy bookmark and verifies the
// response surfaces Proxy and InsecureSkipVerify.
func TestAPIProxyBookmarkRoundtrip(t *testing.T) {
	s := testServer()
	body, _ := json.Marshal(map[string]any{
		"name":                 "mirror",
		"url":                  "https://app.example.com",
		"proxy":                true,
		"insecure_skip_verify": true,
	})
	req := httptest.NewRequest("POST", "/_api/routes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.apiHandler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("register status = %d; body: %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest("GET", "/_api/routes", nil)
	w = httptest.NewRecorder()
	s.apiHandler(w, req)

	var routes []routeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &routes); err != nil {
		t.Fatalf("list unmarshal: %v", err)
	}
	if len(routes) != 1 {
		t.Fatalf("got %d routes; want 1", len(routes))
	}
	if !routes[0].Proxy {
		t.Error("Proxy = false; want true")
	}
	if !routes[0].InsecureSkipVerify {
		t.Error("InsecureSkipVerify = false; want true")
	}
}

func TestRemoveDomainAttr(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"sid=abc", "sid=abc"},
		{"sid=abc; Path=/", "sid=abc; Path=/"},
		{"sid=abc; Domain=example.com; Path=/", "sid=abc; Path=/"},
		{"sid=abc; domain=example.com", "sid=abc"},
		{"sid=abc; DOMAIN=example.com; HttpOnly", "sid=abc; HttpOnly"},
		{"sid=abc; Path=/; Domain=a.b.c; Secure", "sid=abc; Path=/; Secure"},
	}
	for _, tc := range cases {
		got := removeDomainAttr(tc.in)
		if got != tc.want {
			t.Errorf("removeDomainAttr(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}
