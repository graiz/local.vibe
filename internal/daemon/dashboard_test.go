package daemon

import (
	"strings"
	"testing"
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
