package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestStartingVsReconnectingCopy verifies the auto-start page and the
// port-recovery page share the poll-and-reload mechanism but render distinct
// copy: a fresh cold start must not read like error recovery.
func TestStartingVsReconnectingCopy(t *testing.T) {
	s := testServer()
	route := &Route{Name: "apply", Type: RouteManaged, Port: 3005, RegisteredAt: time.Now()}

	// Starting page (auto-start): start-flavored copy, no "logs" recovery text.
	rec := httptest.NewRecorder()
	s.serveStartingPage(rec, httptest.NewRequest(http.MethodGet, "/", nil), route)
	starting := rec.Body.String()
	if !strings.Contains(starting, "Starting") {
		t.Error("starting page missing 'Starting' status")
	}
	if strings.Contains(starting, "Looking for the app in logs") {
		t.Error("starting page should not show the port-recovery 'looking in logs' copy")
	}

	// Reconnecting page (port went dark): recovery copy.
	rec = httptest.NewRecorder()
	s.serveRepairPage(rec, httptest.NewRequest(http.MethodGet, "/", nil), route)
	reconnecting := rec.Body.String()
	if !strings.Contains(reconnecting, "Reconnecting") {
		t.Error("repair page missing 'Reconnecting' status")
	}
	if !strings.Contains(reconnecting, "Looking for the app in logs") {
		t.Error("repair page missing the port-recovery copy")
	}

	// Both drive the same recovery endpoint (poll-and-reload is shared).
	for _, body := range []string{starting, reconnecting} {
		if !strings.Contains(body, "/_api/routes/apply/repair") {
			t.Error("page is missing the shared /repair poll")
		}
	}
}
