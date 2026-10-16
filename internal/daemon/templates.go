package daemon

import (
	"embed"
	"fmt"
	"html/template"
	"strings"
)

//go:embed templates/*.html.tmpl
var templateFS embed.FS

var (
	tmplDashboard  = template.Must(parse("dashboard.html.tmpl"))
	tmplStartPage  = template.Must(parse("startpage.html.tmpl"))
	tmplRepairPage = template.Must(parse("repairpage.html.tmpl"))
)

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
