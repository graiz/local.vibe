package daemon

import (
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"strings"
)

//go:embed templates/*.html.tmpl
var templateFS embed.FS

var (
	tmplDashboard   = template.Must(parse("dashboard.html.tmpl"))
	tmplStartPage   = template.Must(parse("startpage.html.tmpl"))
	tmplRepairPage  = template.Must(parse("repairpage.html.tmpl"))
	tmplMessagePage = template.Must(parse("message.html.tmpl"))
	tmplOAuthPeer   = template.Must(parse("oauth_peer.html.tmpl"))
)

// pageShell is the head/style/script preamble every page template renders.
// Page view models embed it; renderPage fills it.
type pageShell struct {
	Head template.HTML
	CSS  template.CSS
	JS   template.JS
}

func (p *pageShell) setShell(sh pageShell) { *p = sh }

// page is any view model that embeds pageShell.
type page interface{ setShell(pageShell) }

// renderPage is the one place a daemon-served HTML page is written: it sets
// the content type and status, fills the shared shell (fonts, the theme
// stylesheet, the helper script) and executes the template. Headers are
// already out if Execute fails, so the error can only be logged inline.
func renderPage(w http.ResponseWriter, status int, tmpl *template.Template, title string, data page) {
	data.setShell(pageShell{
		Head: template.HTML(themeHead(title)),
		CSS:  template.CSS(themeCSS),
		JS:   template.JS(pagesJS),
	})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := tmpl.Execute(w, data); err != nil {
		fmt.Fprintf(w, "\n<!-- template error: %v -->\n", err)
	}
}

// messagePageData feeds templates/message.html.tmpl.
type messagePageData struct {
	pageShell
	Heading        string
	Lines          []string
	RefreshSeconds int // 0 = no auto-refresh
}

// templateFuncs are the helpers exposed to all dashboard/startpage/repair templates.
// iconHTML decides whether to inline an emoji or emit an <img> for data:/http(s)
// URI icons; both branches return template.HTML so html/template won't double-escape.
var templateFuncs = template.FuncMap{
	"iconHTML": iconHTMLFn,
	"editSVG": func() template.HTML {
		return template.HTML(`<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M11.5 1.5l3 3L5 14H2v-3L11.5 1.5z"/></svg>`)
	},
}

func parse(name string) (*template.Template, error) {
	return template.New(name).Funcs(templateFuncs).ParseFS(templateFS, "templates/"+name)
}

// iconHTMLFn returns the dashboard icon as either a plain (escaped) emoji or
// an <img> tag for data:/http(s) URIs. The img branch is wrapped in
// template.HTML so the surrounding template renders it as markup, not text.
func iconHTMLFn(icon string) template.HTML {
	if strings.HasPrefix(icon, "data:") || strings.HasPrefix(icon, "http://") || strings.HasPrefix(icon, "https://") {
		safe := template.HTMLEscapeString(icon)
		return template.HTML(fmt.Sprintf(`<img src="%s" style="width:100%%;height:100%%;object-fit:contain;border-radius:inherit" onerror="this.style.display='none';this.parentElement.textContent='🔲'">`, safe))
	}
	// Emoji or plain text — return escaped, wrapped as HTML so iteration
	// inside the template doesn't re-escape unicode (a no-op, but keeps
	// the type signature uniform with the data:/URL branch).
	return template.HTML(template.HTMLEscapeString(icon))
}
