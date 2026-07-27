package daemon

// Cross-site request protection for the daemon API.
//
// The API has no authentication — it is reachable over the unix socket, over
// 127.0.0.1:<daemon port>, and on every .vibe host. That is fine for the CLI,
// but a browser is a confused deputy: any page the user visits can issue a
// cross-origin POST to http://127.0.0.1:7999/_api/... . The handlers decode
// JSON regardless of Content-Type, so an attacker page can send a `text/plain`
// body (a CORS "simple request", no preflight to block it) and reach
// handleRegister — whose `cmd` is executed through a login shell. That was
// drive-by remote code execution from any website.
//
// The fix is the standard one for an unauthenticated localhost service:
// state-changing requests must not come from a foreign site. Browsers set
// Origin on every non-GET request and Sec-Fetch-Site on everything, and page
// JavaScript cannot forge either; non-browser clients (the vibe CLI, curl)
// send neither, which is why absence is treated as trusted rather than
// rejected. Reads stay open — cross-origin responses are unreadable without
// CORS headers, which the daemon never sends, and the service worker's
// health probe is cross-origin by construction.

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// apiRequestCrossSite reports whether an /_api/ request appears to originate
// from a page on some other site. Only consulted for state-changing requests.
func (s *Server) apiRequestCrossSite(r *http.Request) bool {
	// An explicit cross-site signal from the browser settles it. Note this
	// only ever votes to BLOCK: a same-site value still has to survive the
	// Origin check below, because every proxied app shares the .vibe TLD.
	if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		// No Origin: not a browser-issued state change (the CLI, curl, or a
		// script). Browsers always send it for POST/PUT/DELETE.
		return false
	}
	return !s.originTrusted(origin)
}

// originTrusted reports whether an Origin header value belongs to vibe's own
// UI surfaces: the dashboard, a managed/sticky route's daemon-served pages
// (start page, repair page), or the daemon's loopback listeners.
//
// Bookmark routes are deliberately excluded. They reverse-proxy third-party
// sites (Home Assistant, a Tailscale host) onto a .vibe origin, and those
// sites must never be able to drive the API. Managed and sticky routes are
// allowed because they already run as the user — a managed child is spawned
// by vibe itself, so granting its origin API access escalates nothing.
func (s *Server) originTrusted(origin string) bool {
	if origin == "" || origin == "null" {
		return false // opaque origin: sandboxed iframe, file://, redirects
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	host, port := u.Hostname(), u.Port()
	tld := s.cfg.Daemon.TLD

	// The daemon's own listeners. An explicit port must match the daemon's
	// HTTP or TLS port; an empty port means :80/:443, which the privileged-port
	// redirect forwards to the daemon. Any other local port is some other dev
	// server and is not trusted.
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		switch port {
		case "":
			return true
		case fmt.Sprint(s.cfg.Daemon.Port):
			return true
		case fmt.Sprint(s.cfg.Daemon.TLS.Port):
			return s.cfg.Daemon.TLS.Enabled
		default:
			return false
		}
	}

	if host == "local."+tld {
		return true // the dashboard
	}
	// A registered route's own origin. Matching on the exact host (not a
	// substring) means "local.test.evil.com" can't pass as "local.test".
	if strings.HasSuffix(host, "."+tld) {
		name := strings.TrimSuffix(host, "."+tld)
		if rt, ok := s.table.Get(name); ok && rt.Type != RouteBookmark {
			return true
		}
	}
	return false
}

// apiStateChanging reports whether an /_api/ request can alter daemon state,
// and therefore needs the cross-site guard. Everything except GET qualifies;
// GET /repair is included because it rewrites a route's registered port.
func apiStateChanging(method, path string) bool {
	if method != http.MethodGet {
		return true
	}
	return strings.HasSuffix(path, "/repair")
}
