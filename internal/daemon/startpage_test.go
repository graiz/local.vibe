package daemon

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/graiz/local.vibe/internal/config"
)

// TestServeStartPageEscapesCmd ensures route.Cmd (free-form user input)
// is HTML-escaped before being injected into the start page, so a
// registered route with a crafted cmd can't execute script in the browser.
func TestServeStartPageEscapesCmd(t *testing.T) {
	cfg := &config.Config{Daemon: config.DaemonConfig{TLD: "test"}}
	s := NewServer(cfg)

	route := &Route{
		Name: "xss",
		Port: 3000,
		Type: RouteManaged,
		Cmd:  `<script>alert("pwned")</script>`,
	}

	w := httptest.NewRecorder()
	s.serveStartPage(w, httptest.NewRequest("GET", "/", nil), route)

	body := w.Body.String()
	if strings.Contains(body, "<script>alert") {
		t.Error("start page contains unescaped <script> from route.Cmd")
	}
	if !strings.Contains(body, "&lt;script&gt;alert") {
		t.Error("start page should contain HTML-escaped cmd; got:\n" + body)
	}
}
