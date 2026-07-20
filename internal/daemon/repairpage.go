package daemon

import (
	"fmt"
	"html/template"
	"net/http"
)

type repairPageData struct {
	Head     template.HTML
	CSS      template.CSS
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
	tld := s.cfg.Daemon.TLD
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	data := repairPageData{
		Head:     template.HTML(themeHead(route.Name + "." + tld)),
		CSS:      template.CSS(themeCSS),
		Name:     route.Name,
		TLD:      tld,
		Port:     route.Port,
		Starting: starting,
	}

	if err := tmplRepairPage.Execute(w, data); err != nil {
		fmt.Fprintf(w, "\n<!-- template error: %v -->\n", err)
	}
}
