package daemon

import (
	"net/http"
)

type repairPageData struct {
	pageShell
	Name     string
	TLD      string
	Port     int
	Starting bool // true → "Starting" copy (fresh auto-start); false → "Reconnecting" (port went dark)
}

// serveRepairPage renders a "reconnecting..." page for a route whose registered
// port stopped answering under a still-tracked process — the daemon polls to
// rediscover the port. The page polls /_api/routes/{name}/repair; on a
// successful hit the daemon silently rewrites the route's port and the page
// reloads into a working proxy.
func (s *Server) serveRepairPage(w http.ResponseWriter, r *http.Request, route *Route) {
	s.serveReconnectPage(w, r, route, false)
}

// serveStartingPage renders the same poll-and-reload page as serveRepairPage but
// with "Starting" copy, for a stopped route the daemon just auto-started on
// demand. The mechanism is identical — poll until the port answers, then reload
// — but a fresh cold start shouldn't read like error recovery ("Reconnecting /
// looking for the app in logs"), so only the wording differs.
func (s *Server) serveStartingPage(w http.ResponseWriter, r *http.Request, route *Route) {
	s.serveReconnectPage(w, r, route, true)
}

func (s *Server) serveReconnectPage(w http.ResponseWriter, _ *http.Request, route *Route, starting bool) {
	data := repairPageData{
		Name:     route.Name,
		TLD:      s.cfg.Daemon.TLD,
		Port:     route.Port,
		Starting: starting,
	}
	renderPage(w, http.StatusOK, tmplRepairPage, s.routeHost(route.Name), &data)
}
