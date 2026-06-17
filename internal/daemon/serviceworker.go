package daemon

import (
	_ "embed"
	"net/http"
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
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	// Allow the worker (served from /sw.js) to control the whole origin.
	w.Header().Set("Service-Worker-Allowed", "/")
	_, _ = w.Write([]byte(body))
}
