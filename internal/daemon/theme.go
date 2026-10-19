package daemon

import (
	_ "embed"
	"fmt"
)

// themeHead returns the shared <head> preamble: charset, viewport, title, and fonts.
func themeHead(title string) string {
	return fmt.Sprintf(`<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>%s</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Instrument+Sans:wght@400;500;600&family=IBM+Plex+Mono:wght@400;500&display=swap" rel="stylesheet">`, title)
}

// themeCSS is the single stylesheet for every daemon-served page — tokens,
// shared components, and per-page sections all live in templates/theme.css so
// the design stays consistent from one file. It is inlined into each page via
// template.CSS, keeping the no-external-assets rule.
//
//go:embed templates/theme.css
var themeCSS string
