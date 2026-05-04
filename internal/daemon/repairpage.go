package daemon

import (
	"fmt"
	"html/template"
	"net/http"
)

type repairPageData struct {
	Head template.HTML
	CSS  template.CSS
	Name string
	TLD  string
	Port int
}

// serveRepairPage renders a "reconnecting..." page for a route whose
// registered port is no longer answering. The page polls
// /_api/routes/{name}/repair in the background; on a successful hit the
// daemon silently rewrites the route's port and the page reloads into a
// working proxy.
func (s *Server) serveRepairPage(w http.ResponseWriter, _ *http.Request, route *Route) {
	tld := s.cfg.Daemon.TLD
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	data := repairPageData{
		Head: template.HTML(themeHead(route.Name + "." + tld)),
		CSS:  template.CSS(themeCSS),
		Name: route.Name,
		TLD:  tld,
		Port: route.Port,
	}

	if err := tmplRepairPage.Execute(w, data); err != nil {
		fmt.Fprintf(w, "\n<!-- template error: %v -->\n", err)
	}
}
