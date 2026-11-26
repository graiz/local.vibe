package daemon

import (
	"fmt"
	"net"
	"strings"
)

// routeNameFromHost extracts the route label from a request Host that names a
// .<tld> site: "app.vibe" → "app", "feat.app.vibe" → "feat.app", and the
// dashboard's "local.vibe" → "local" (callers test for that). Anything else —
// loopback addresses, IPv6 literals, foreign names, suffix-confusion like
// "app.vibe.evil.com" — returns ok=false.
//
// One parser for the request router, the API passthrough, the dashboard, the
// peer listener and the origin gate: four of them used to strip the port with
// strings.Index(host, ":"), which turns "[::1]:7999" into "[".
func (s *Server) routeNameFromHost(host string) (string, bool) {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	suffix := "." + s.cfg.Daemon.TLD
	if !strings.HasSuffix(h, suffix) {
		return "", false
	}
	name := strings.TrimSuffix(h, suffix)
	if name == "" {
		return "", false
	}
	return name, true
}

// routeHost is the .vibe host a route answers on.
func (s *Server) routeHost(name string) string { return name + "." + s.cfg.Daemon.TLD }

// routeURL is the browser URL for a route: https when TLS is on, else http.
func (s *Server) routeURL(name string) string {
	return fmt.Sprintf("%s://%s", s.vibeScheme(), s.routeHost(name))
}
