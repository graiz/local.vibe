package daemon

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/graiz/local.vibe/internal/peer"
)

// TestDashboardRedirectBanner verifies the broken-redirect warning renders only
// when RedirectDown is set, and surfaces the fix command.
func TestDashboardRedirectBanner(t *testing.T) {
	render := func(down bool) string {
		var b strings.Builder
		data := dashboardData{TLD: "vibe", RedirectDown: down}
		if err := tmplDashboard.Execute(&b, data); err != nil {
			t.Fatalf("render: %v", err)
		}
		return b.String()
	}

	up := render(false)
	if strings.Contains(up, "HTTPS redirect is down") {
		t.Error("banner shown when redirect is healthy")
	}

	down := render(true)
	if !strings.Contains(down, "HTTPS redirect is down") {
		t.Error("banner missing when redirect is down")
	}
	if !strings.Contains(down, "vibe doctor --fix") {
		t.Error("banner does not surface the fix command")
	}
}

func TestDashboardShowsPeerRoutesReadOnly(t *testing.T) {
	s := newBareServer(t)
	s.peerList = []peer.Peer{{Name: "imac", Host: "127.0.0.1", Port: 7444, Fingerprint: "aa", AddedAt: time.Now()}}
	s.peerStates["imac"] = &peerState{routes: []peer.RouteSummary{
		{Name: "face", Type: "managed", Running: true, Ready: true},
		{Name: "clash", Type: "sticky", Running: true, Ready: true},
	}}
	s.table.Add(&Route{Name: "clash", Type: RouteSticky, Port: 4000, RegisteredAt: time.Now()})

	req := httptest.NewRequest("GET", "http://local.vibe/", nil)
	req.Host = "local.vibe"
	rec := httptest.NewRecorder()
	s.serveDashboard(rec, req)
	body := rec.Body.String()

	if !strings.Contains(body, "imac") {
		t.Fatal("peer group header missing")
	}
	if !strings.Contains(body, `href="http://face.vibe"`) && !strings.Contains(body, `href="https://face.vibe"`) {
		t.Fatalf("peer route face must link to its .vibe URL")
	}
	if !strings.Contains(body, "shadowed by") {
		t.Fatal("shadowed peer route not badged")
	}
	// The shadowed clash row must not be a link to clash.vibe inside the
	// peers section — it renders as plain text. The local clash row's link
	// exists, so count: exactly one clash anchor.
	if n := strings.Count(body, `>clash</a>`); n != 1 {
		t.Fatalf("want exactly 1 clash anchor (the local route), got %d", n)
	}
	// No start/stop controls target peer routes.
	if strings.Contains(body, `routeAction(this,'face'`) {
		t.Fatal("peer route has a start/stop control")
	}
}
