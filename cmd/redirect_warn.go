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
	// Probe BOTH privileged ports. A coexisting pf user can reload a ruleset
	// that preserves one of vibe's two rdr rules and drops the other — the
	// same partial-flush case vibeRDRPresent guards against — so checking :80
	// alone stays silent while every https://*.vibe is broken.
	http80 := netprobe.PortOpen(80, 400*time.Millisecond)
	https443 := netprobe.PortOpen(443, 400*time.Millisecond)
	if http80 && https443 {
		return
	}
	// A dead daemon makes both dials fail too (the rdr forwards to a closed
	// port), and "redirect is down" would be a misdiagnosis. Only claim the
	// redirect is at fault when the daemon itself answers.
	if !netprobe.DaemonAnswers("http", cfg.Daemon.Port, 400*time.Millisecond) {
		return
	}
	switch {
	case !http80 && !https443:
		fmt.Fprintf(os.Stderr,
			"⚠ Privileged-port redirect is down — http://*.%s and https://*.%s won't connect. Run `vibe doctor --fix`.\n\n",
			cfg.Daemon.TLD, cfg.Daemon.TLD)
	case !https443:
		fmt.Fprintf(os.Stderr,
			"⚠ HTTPS redirect is down — https://*.%s won't connect. Run `vibe doctor --fix`.\n\n",
			cfg.Daemon.TLD)
	default:
		fmt.Fprintf(os.Stderr,
			"⚠ HTTP redirect is down — http://*.%s won't connect. Run `vibe doctor --fix`.\n\n",
			cfg.Daemon.TLD)
	}
}
