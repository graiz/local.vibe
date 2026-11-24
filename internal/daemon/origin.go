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
	"net"
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
		return s.daemonPortTrusted(port)
	}

	// The .vibe hosts below must pass the SAME port test. Every *.<tld> name
	// resolves to 127.0.0.1, so "http://local.<tld>:31337" reaches whatever
	// unrelated service holds port 31337 — and that page is schemefully
	// same-site with the dashboard, so Sec-Fetch-Site won't catch it either.
	// Without this check, any loopback port that can be made to render
	// attacker-controlled HTML becomes a trusted API origin, which is the
	// drive-by RCE this file exists to prevent.
	if !s.daemonPortTrusted(port) {
		return false
	}

	if host == "local."+tld {
		return true // the dashboard
	}
	// A registered route's own origin. Matching on the exact host (not a
	// substring) means "local.test.evil.com" can't pass as "local.test".
	_, isRoute := s.originRouteName(origin)
	return isRoute
}

// originRouteName reports whether origin is a registered non-bookmark route's
// own .<tld> origin on a daemon port, and which route. apiHandler uses it to
// confine such an origin to the route's own endpoints.
func (s *Server) originRouteName(origin string) (string, bool) {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || !s.daemonPortTrusted(u.Port()) {
		return "", false
	}
	name, ok := s.routeNameFromHost(u.Host)
	if !ok || name == "local" {
		return "", false
	}
	if rt, ok := s.table.Get(name); ok && rt.Type != RouteBookmark {
		return name, true
	}
	return "", false
}

// daemonPortTrusted reports whether an origin's port is one the daemon itself
// answers on. An empty port means :80/:443, which the privileged-port redirect
// forwards to the daemon. Any other port is some other local server.
func (s *Server) daemonPortTrusted(port string) bool {
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

// apiStateChanging reports whether an /_api/ request can alter daemon state,
// and therefore needs the cross-site guard. Everything except GET qualifies.
// Repair — which rewrites a route's registered port — is a POST for exactly
// this reason: a GET arrives with no Origin from a same-origin fetch or an
// <img src>, and Sec-Fetch-Site reads same-site for every .vibe page, so a
// GET could never be guarded.
func apiStateChanging(method string) bool {
	return method != http.MethodGet
}

// routeDetailsAllowed reports whether a read about one route may include that
// route's failure diagnostics — its log tail and recovery hints, which name
// the command line and working directory. Allowed wherever localDetailsAllowed
// is, plus the route's own host on a daemon port: that is where its start and
// repair pages run. Another route's host (a proxy bookmark's third-party JS
// reads /_api/ same-origin there) and DNS-rebound names get nothing.
func (s *Server) routeDetailsAllowed(r *http.Request, route *Route) bool {
	if s.localDetailsAllowed(r) {
		return true
	}
	if route.Type == RouteBookmark {
		return false
	}
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		host, port = r.Host, ""
	}
	return host == route.Name+"."+s.cfg.Daemon.TLD && s.daemonPortTrusted(port)
}

// localDetailsAllowed reports whether a read may include machine-local detail
// (a route's directory and command line) that route names and ports don't
// reveal. GET /_api/routes is answered on every .vibe host, so without this a
// proxy bookmark's third-party JavaScript could fetch it same-origin, and a
// DNS-rebound name could read it from the TCP listener. Allowed: the unix
// socket (the CLI), and Hosts that are the daemon itself — loopback or
// local.<tld>, on a daemon port. Route hosts are refused too: one app's page
// has no business reading every other app's paths.
func (s *Server) localDetailsAllowed(r *http.Request) bool {
	// TCP connections always carry "ip:port"; a unix-socket peer never does.
	if _, _, err := net.SplitHostPort(r.RemoteAddr); err != nil {
		return true
	}
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		host, port = r.Host, ""
	}
	switch host {
	case "localhost", "127.0.0.1", "::1", "local." + s.cfg.Daemon.TLD:
		return s.daemonPortTrusted(port)
	}
	return false
}
