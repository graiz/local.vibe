package daemon

import (
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/graiz/local.vibe/internal/netprobe"
)

// vibeScheme returns "https" when TLS is enabled, "http" otherwise. Used
// throughout the dashboard so links to .vibe routes match how the daemon
// is currently listening.
func (s *Server) vibeScheme() string {
	if s.cfg.Daemon.TLS.Enabled {
		return "https"
	}
	return "http"
}

// dashboardData is the view model rendered into templates/dashboard.html.tmpl.
// All user-supplied strings (route names, external URLs, icons) are auto-
// escaped by html/template's contextual escaper; we no longer hand-call
// html.EscapeString on the way out.
type dashboardData struct {
	Head           template.HTML
	CSS            template.CSS
	Scheme         string
	TLD            string
	Port           int
	Uptime         string
	UnknownName    string
	UnknownVisible bool
	ListBtnClass   string
	GridBtnClass   string
	ListDisplay    string
	GridDisplay    string
	RouteCount     int // includes the synthetic "local" daemon row
	RedirectDown   bool // privileged-port redirect (pf/portproxy) isn't forwarding
	Routes         []dashboardRoute
}

// dashboardRoute is a single row in the dashboard, derived from a *Route.
// Resolving the icon and "stopped" flag once here keeps the template free
// of business logic (e.g. the icon-pool fallback hash).
type dashboardRoute struct {
	Name        string
	VibeURL     string
	URLDisplay  string
	Type        RouteType
	Port        int
	Age         string
	IsStopped   bool
	IsRunning   bool
	Icon        string
	UserIcon    string
	AutoIcon    string
	ExternalURL string
	Idle        int
	Proxy       bool
	Insecure    bool
	Parent      string // worktree routes: the app this belongs to
	IsWorktree  bool
	GroupHeader string // non-empty on the first row of a parent-less worktree group
}

// iconPool — visually distinct emoji set. A new route with no user-chosen
// icon and no auto-detected favicon gets a deterministic pick from this
// pool keyed off its name, so the dashboard isn't a wall of identical
// placeholders.
var iconPool = []string{
	"🚀", "🤖", "📦", "⚡", "🔮", "🎯", "🛠️", "💎",
	"🧪", "🌐", "📡", "🎨", "🔒", "🗂️", "📊", "🧩",
}

func (s *Server) serveDashboard(w http.ResponseWriter, r *http.Request) {
	routes := s.table.List()
	tld := s.cfg.Daemon.TLD
	port := s.cfg.Daemon.Port
	scheme := s.vibeScheme()

	host := r.Host
	if idx := strings.Index(host, ":"); idx != -1 {
		host = host[:idx]
	}

	unknownName := ""
	if strings.HasSuffix(host, "."+tld) {
		name := strings.TrimSuffix(host, "."+tld)
		if name != "local" {
			if _, ok := s.table.Get(name); !ok {
				unknownName = name
			}
		}
	}

	viewMode := s.cfg.Dashboard.View
	if viewMode == "" {
		viewMode = "list"
	}
	listBtnClass, gridBtnClass := "active", ""
	listDisplay, gridDisplay := "block", "none"
	if viewMode == "grid" {
		listBtnClass, gridBtnClass = "", "active"
		listDisplay, gridDisplay = "none", "block"
	}

	uptime := time.Since(s.startedAt).Round(time.Second)

	// On-request (not polled) check of the privileged-port redirect: dialing :80
	// succeeds only if the redirect forwards to us. pf-apply adds/removes the :80
	// and :443 rules together, so :80 is a faithful proxy for the whole redirect
	// layer — and unlike dialing :443, a bare TCP connect to the plain-HTTP port
	// doesn't trigger a half-handshake that would spam the TLS error log. A
	// failure means a VPN/firewall flushed the redirect; surface a banner so the
	// user isn't left guessing why https://*.vibe "refuses to connect". Only
	// meaningful once TLS/redirect is set up. Short timeout so a down redirect
	// doesn't stall page render.
	redirectDown := false
	if s.cfg.Daemon.TLS.Enabled {
		redirectDown = !netprobe.PortOpen(80, 250*time.Millisecond)
	}

	data := dashboardData{
		Head:           template.HTML(themeHead("local.vibe")),
		CSS:            template.CSS(themeCSS),
		Scheme:         scheme,
		TLD:            tld,
		Port:           port,
		Uptime:         fmtDuration(uptime),
		UnknownName:    unknownName,
		UnknownVisible: unknownName != "",
		ListBtnClass:   listBtnClass,
		GridBtnClass:   gridBtnClass,
		ListDisplay:    listDisplay,
		GridDisplay:    gridDisplay,
		RouteCount:     len(routes) + 1,
		RedirectDown:   redirectDown,
		Routes:         make([]dashboardRoute, 0, len(routes)),
	}

	for _, rt := range routes {
		isStopped := rt.Type == RouteManaged && !rt.Running.Load()

		urlDisplay := fmt.Sprintf("%s.%s", rt.Name, tld)
		if rt.Type == RouteBookmark {
			urlDisplay = rt.ExternalURL
		}

		// Icon priority: user-chosen > auto-detected favicon > deterministic pool pick.
		icon := iconPool[nameHash(rt.Name)%len(iconPool)]
		if rt.AutoIcon != "" {
			icon = rt.AutoIcon
		}
		if rt.Icon != "" {
			icon = rt.Icon
		}

		data.Routes = append(data.Routes, dashboardRoute{
			Name:        rt.Name,
			VibeURL:     fmt.Sprintf("%s://%s.%s", scheme, rt.Name, tld),
			URLDisplay:  urlDisplay,
			Type:        rt.Type,
			Port:        rt.Port,
			Age:         fmtDuration(time.Since(rt.RegisteredAt).Round(time.Second)),
			IsStopped:   isStopped,
			IsRunning:   rt.Running.Load(),
			Icon:        icon,
			UserIcon:    rt.Icon,
			AutoIcon:    rt.AutoIcon,
			ExternalURL: rt.ExternalURL,
			Idle:        rt.IdleTimeout,
			Proxy:       rt.Proxy,
			Insecure:    rt.InsecureSkipVerify,
			Parent:      rt.Parent,
			IsWorktree:  rt.Parent != "",
		})
	}

	// Group worktrees under their parent app: sort by group (the parent's
	// name, or the route's own name), parents before their worktrees, then
	// by name within each tier. Parent-less groups (the app was never
	// registered — Parent is just a string) get a header label on their
	// first row so the cluster is still visibly an app.
	groupKey := func(r dashboardRoute) string {
		if r.Parent != "" {
			return r.Parent
		}
		return r.Name
	}
	sort.SliceStable(data.Routes, func(i, j int) bool {
		gi, gj := groupKey(data.Routes[i]), groupKey(data.Routes[j])
		if gi != gj {
			return gi < gj
		}
		if data.Routes[i].IsWorktree != data.Routes[j].IsWorktree {
			return !data.Routes[i].IsWorktree
		}
		return data.Routes[i].Name < data.Routes[j].Name
	})
	registered := make(map[string]bool, len(data.Routes))
	for _, rt := range data.Routes {
		if !rt.IsWorktree {
			registered[rt.Name] = true
		}
	}
	prevGroup := ""
	for i := range data.Routes {
		rt := &data.Routes[i]
		if rt.IsWorktree && !registered[rt.Parent] && groupKey(*rt) != prevGroup {
			rt.GroupHeader = rt.Parent
		}
		prevGroup = groupKey(*rt)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmplDashboard.Execute(w, data); err != nil {
		// Headers are already written, so the response is half-rendered
		// at best. Logging is the only remaining recourse.
		fmt.Fprintf(w, "\n<!-- template error: %v -->\n", err)
	}
}

// nameHash returns a stable hash for a route name, used to pick a default icon.
func nameHash(name string) int {
	h := 0
	for _, c := range name {
		h = h*31 + int(c)
	}
	if h < 0 {
		h = -h
	}
	return h
}

func fmtDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	}
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	if h == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd%dh", days, h)
}
