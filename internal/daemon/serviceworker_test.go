package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServeServiceWorker(t *testing.T) {
	s := testServer() // TLD "test"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sw.js", nil)

	s.serveServiceWorker(rec, req)

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("Content-Type = %q, want text/javascript", ct)
	}
	if rec.Header().Get("Service-Worker-Allowed") != "/" {
		t.Error("missing Service-Worker-Allowed: / header (worker couldn't claim origin scope)")
	}
	body := rec.Body.String()
	if strings.Contains(body, "__TLD__") {
		t.Error("TLD placeholder not substituted")
	}
	if !strings.Contains(body, "local.test") {
		t.Error("TLD not injected into worker")
	}
	for _, want := range []string{"addEventListener", "FALLBACK_HTML", "vibe doctor --fix", "vibe daemon start"} {
		if !strings.Contains(body, want) {
			t.Errorf("worker body missing %q", want)
		}
	}
}
