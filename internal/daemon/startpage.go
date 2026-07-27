package daemon

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"
	"strings"
)

// startPageData feeds templates/startpage.html.tmpl. RecoveryInit is
// JSON-serialized JavaScript (or empty) that fires showRecovery(...) on
// load when the route already has a known-bad signal — so users see the
// "Kill PID X and retry" affordance without having to click Start first.
type startPageData struct {
	Head         template.HTML
	CSS          template.CSS
	Name         string
	TLD          string
	Cmd          string
	RecoveryInit template.JS
	Worktrees    []startPageWorktree
	// AutoLaunch makes the page start the app on load (the historical UX for
	// a worktree-less app). In picker mode — any worktree exists — the page
	// must NOT auto-launch: doing so would navigate away from the picker
	// before the visitor can choose, defeating its purpose. They get a
	// manual Start button instead.
	AutoLaunch bool
}

// startPageWorktree is one row in the start page's worktree picker section.
// Registered rows link straight to the route; discovered (unregistered) rows
// carry the on-disk Path and register-and-start on click.
type startPageWorktree struct {
	Name       string // subdomain label only, e.g. "feature-auth"
	URL        string // registered rows only
	Running    bool
	Registered bool
	Path       string // discovered rows only
}

// serveStartPage renders a "not running" page for managed routes whose process
// has stopped. The page offers a Start button and polls /ready until the port
// accepts connections.
func (s *Server) serveStartPage(w http.ResponseWriter, _ *http.Request, route *Route) {
	tld := s.cfg.Daemon.TLD
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// The start page doubles as the worktree picker: when the app itself is
	// stopped, its running worktrees are one click away. List() is
	// name-sorted, so siblings render in stable order.
	var worktrees []startPageWorktree
	for _, rt := range s.table.List() {
		if rt.Parent == route.Name {
			worktrees = append(worktrees, startPageWorktree{
				Name:       strings.TrimSuffix(rt.Name, "."+route.Name),
				URL:        fmt.Sprintf("%s://%s.%s/", s.vibeScheme(), rt.Name, tld),
				Running:    rt.Running.Load(),
				Registered: true,
			})
		}
	}
	// On-disk worktrees nobody registered yet — one click registers them with
	// this route's cmd and starts them.
	for _, d := range s.discoverUnregisteredWorktrees(route) {
		worktrees = append(worktrees, startPageWorktree{
			Name: d.Slug,
			Path: d.Path,
		})
	}

	data := startPageData{
		Head:         template.HTML(themeHead(route.Name + "." + tld)),
		CSS:          template.CSS(themeCSS),
		Name:         route.Name,
		TLD:          tld,
		Cmd:          route.Cmd,
		RecoveryInit: template.JS(s.startPageRecoveryInitJS(route)),
		Worktrees:    worktrees,
		AutoLaunch:   len(worktrees) == 0,
	}

	if err := tmplStartPage.Execute(w, data); err != nil {
		fmt.Fprintf(w, "\n<!-- template error: %v -->\n", err)
	}
}

// startPageRecoveryInitJS returns JavaScript that calls showRecovery(...) on
// load when a known-bad condition already exists for the route (e.g., an
// orphan dev server on another port that the last crash identified).
// Returns an empty string when there's nothing actionable to surface.
func (s *Server) startPageRecoveryInitJS(route *Route) string {
	failure := route.LoadFailure()
	if failure == nil || failure.Recovery == nil {
		// Stored state may be gone (daemon restart). Scan the log as a
		// best-effort fallback so visibility isn't tied to in-memory state.
		logPath := filepath.Join(s.configDir(), route.Name+".log")
		tail := tailLogFile(logPath, 24)
		if tail == "" {
			return ""
		}
		rec := scanLogForRecovery(tail, route.Cmd)
		if rec == nil {
			return ""
		}
		failure = &Failure{Message: "Previous start attempt failed", Log: tail, Recovery: rec}
	}
	recJSON, err := json.Marshal(failure.Recovery)
	if err != nil {
		return ""
	}
	logJSON, err := json.Marshal(failure.Log)
	if err != nil {
		return ""
	}
	// json.Marshal escapes <, >, & to \u003c etc., so the output is safe to
	// inline inside a <script> block without closing the tag prematurely.
	return fmt.Sprintf("showRecovery(%s, %s);", recJSON, logJSON)
}
