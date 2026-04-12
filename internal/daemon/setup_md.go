package daemon

import (
	_ "embed"
	"net/http"
	"strings"
)

//go:embed setup.md
var setupMD string

// serveSetupMD serves a Markdown guide at /setup.md explaining how to
// configure projects to work with vibe. The template uses {{TLD}} as a
// placeholder which is replaced with the configured TLD at serve time.
func (s *Server) serveSetupMD(w http.ResponseWriter, r *http.Request) {
	content := strings.ReplaceAll(setupMD, "{{TLD}}", s.cfg.Daemon.TLD)
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Write([]byte(content))
}
