package daemon

import (
	_ "embed"
	"net/http"
	"strconv"
	"strings"
)

//go:embed sw.js
var swJS string

// serveServiceWorker serves the local.<tld> service worker. The worker is
// browser-resident, so it can show a helpful fallback page even when the
// redirect (pf/portproxy) is flushed and the daemon is otherwise unreachable —
// the case where the inline dashboard banner can't be seen.
func (s *Server) serveServiceWorker(w http.ResponseWriter, r *http.Request) {
	body := strings.ReplaceAll(swJS, "__TLD__", s.cfg.Daemon.TLD)
	// The daemon's plain-HTTP port is bound directly (independent of the
	// redirect), so the fallback page probes it to tell "redirect down" from
	// "daemon down". It's configurable, so substitute it rather than hardcoding
	// 7999 — otherwise a non-default port always fails the probe and the page
	// misdiagnoses a flushed redirect as a dead daemon.
	body = strings.ReplaceAll(body, "__PORT__", strconv.Itoa(s.cfg.Daemon.Port))
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	// Allow the worker (served from /sw.js) to control the whole origin.
	w.Header().Set("Service-Worker-Allowed", "/")
	_, _ = w.Write([]byte(body))
}
