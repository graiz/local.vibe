package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/graiz/local.vibe/internal/config"
	"github.com/graiz/local.vibe/internal/netprobe"
)

// warnIfRedirectDown prints a one-line warning to stderr when TLS is configured
// but the privileged-port redirect isn't forwarding — the failure mode where
// every https://*.vibe "refuses to connect" even though the daemon is healthy.
//
// This is surfaced on the CLI (list/status) precisely because the dashboard is
// itself unreachable when the redirect is down (it lives behind the same
// redirect), so a dashboard-only banner can't be seen when it matters most.
// Uses the same loopback probe as `vibe doctor`.
func warnIfRedirectDown() {
	cfg, err := config.Load()
	if err != nil || !cfg.Daemon.TLS.Enabled {
		return
	}
	if !netprobe.PortOpen(80, 400*time.Millisecond) {
		fmt.Fprintf(os.Stderr,
			"⚠ HTTPS redirect is down — https://*.%s won't connect. Run `vibe doctor --fix`.\n\n",
			cfg.Daemon.TLD)
	}
}
