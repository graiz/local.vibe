package cmd

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/graiz/local.vibe/internal/client"
	"github.com/graiz/local.vibe/internal/config"
	"github.com/graiz/local.vibe/internal/netprobe"
	"github.com/spf13/cobra"
)

var doctorFix bool

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Diagnose (and optionally repair) the .vibe request path",
	Long: `Check each layer of the .vibe request path — daemon, listeners, DNS, and the
privileged-port redirect — and report what's broken.

The redirect is the layer a VPN's firewall reload tends to silently break, which
makes every https://*.vibe route "refuse to connect" while the daemon itself is
fine. Run with --fix to re-apply the redirect.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			cfg = config.DefaultConfig()
		}

		redirectOK, allOK, warned := runDoctor(cfg)

		if allOK {
			if warned {
				fmt.Println("\nNo faults found — see the note above for the degraded check.")
			} else {
				fmt.Println("\nAll checks passed.")
			}
			return nil
		}

		// --fix repairs the redirect layer specifically (the part we can fix
		// automatically). Other failures (daemon down, DNS) need their own steps.
		if doctorFix && !redirectOK {
			fmt.Printf("\nRepairing %s redirect...\n", redirectMechanismName())
			if err := platformRepairRedirect(); err != nil {
				return fmt.Errorf("repair failed: %w", err)
			}
			fmt.Println("\nRe-checking...")
			if _, allOK, _ = runDoctor(cfg); allOK {
				fmt.Println("\nFixed.")
				return nil
			}
		} else if !redirectOK {
			fmt.Printf("\nThe %s redirect is down — run `vibe doctor --fix` to restore it.\n", redirectMechanismName())
		}

		os.Exit(1)
		return nil
	},
}

func init() {
	doctorCmd.Flags().BoolVar(&doctorFix, "fix", false, "attempt to repair the redirect layer")
	rootCmd.AddCommand(doctorCmd)
}

// checkStatus is the outcome of one doctor check. The middle state exists
// because "the port doesn't answer a direct dial" is not the same finding as
// "the service is down" — see classifyListener.
type checkStatus int

const (
	statusOK checkStatus = iota
	statusWarn
	statusFail
)

func (s checkStatus) mark() string {
	switch s {
	case statusOK:
		return "✓"
	case statusWarn:
		return "⚠"
	default:
		return "✗"
	}
}

// classifyListener decides how to report one of the daemon's listeners.
//
// A plain dial is the obvious probe, but it misdiagnoses a common setup: a VPN
// kill-switch (or any firewall) that filters direct loopback connections makes
// the dial time out while the daemon serves normally. The redirect settles it.
// Dialing :80/:443 succeeds only if the packet was translated to the daemon's
// port AND something accepted it there — so a working redirect proves the
// listener is up, and a failing direct dial then means the port is filtered,
// not dead. Browsers (which use the redirect) and the CLI (unix socket) are
// unaffected in that state, so it is a warning, not a failure.
//
// daemonUp guards the inference: without it, an unrelated process squatting
// :80 would make a dead listener look merely filtered.
func classifyListener(direct, redirectOpen, daemonUp bool) checkStatus {
	switch {
	case direct:
		return statusOK
	case redirectOpen && daemonUp:
		return statusWarn
	default:
		return statusFail
	}
}

// runDoctor runs every check and prints a status line for each. It returns
// (redirectOK, allOK, warned): redirectOK gates whether --fix has anything to
// do; allOK is true only when every critical check passed; warned reports
// whether any check came back degraded-but-working.
func runDoctor(cfg *config.Config) (redirectOK, allOK, warned bool) {
	tld := cfg.Daemon.TLD
	allOK = true
	redirectOK = true

	report := func(name string, status checkStatus, critical bool, detail string) {
		fmt.Printf("  %s %-28s %s\n", status.mark(), name, detail)
		switch status {
		case statusWarn:
			warned = true
		case statusFail:
			if critical {
				allOK = false
			}
		}
	}
	reportBool := func(name string, ok, critical bool, detail string) {
		status := statusFail
		if ok {
			status = statusOK
		}
		report(name, status, critical, detail)
	}

	// Daemon reachable (over the unix socket / TCP the CLI uses).
	h, herr := client.New().Health()
	daemonUp := herr == nil
	if daemonUp {
		reportBool("daemon", true, true, fmt.Sprintf("ok (%d routes, up %s)", h.Routes,
			(time.Duration(h.Uptime)*time.Second).Round(time.Second)))
	} else {
		reportBool("daemon", false, true, "not running — start it or run `vibe setup`")
	}

	// Probe the redirect first: its result is what tells a filtered port apart
	// from a dead listener below. Reported further down, in request-path order.
	r80 := answersOK("http", 80)
	r443 := false
	if cfg.Daemon.TLS.Enabled {
		r443 = answersOK("https", 443)
	}

	// Listeners. A failed direct probe is only a failure if the redirect can't
	// reach the daemon either (see classifyListener).
	httpStatus := classifyListener(answersOK("http", cfg.Daemon.Port), r80, daemonUp)
	report("http listener", httpStatus, true, listenerDetail(cfg.Daemon.Port, 80, httpStatus))

	tlsStatus := statusOK
	if cfg.Daemon.TLS.Enabled {
		tlsStatus = classifyListener(answersOK("https", cfg.Daemon.TLS.Port), r443, daemonUp)
		report("tls listener", tlsStatus, true, listenerDetail(cfg.Daemon.TLS.Port, 443, tlsStatus))
	}

	// DNS: a .vibe name must resolve to loopback.
	host := "local." + tld
	addrs, derr := net.LookupHost(host)
	reportBool("dns", derr == nil && hasLoopback(addrs), true, host+" → 127.0.0.1")

	// Redirect path — the privileged-port layer. A plain dial to :80/:443 only
	// succeeds if the redirect is forwarding to the daemon, so it catches a
	// flushed pf ruleset without needing root.
	reportBool(redirectMechanismName()+" redirect :80", r80, true,
		fmt.Sprintf("127.0.0.1:80 → %d", cfg.Daemon.Port))
	rOK := r80
	if cfg.Daemon.TLS.Enabled {
		rOK = rOK && r443
		reportBool(redirectMechanismName()+" redirect :443", r443, true,
			fmt.Sprintf("127.0.0.1:443 → %d", cfg.Daemon.TLS.Port))
	}
	redirectOK = rOK

	// Certs (best-effort, non-critical).
	if cfg.Daemon.TLS.Enabled {
		reportBool("tls cert", caCertExists(), false, caCertPath())
	}

	if httpStatus == statusWarn || tlsStatus == statusWarn {
		fmt.Print(filteredPortNote)
	}

	return redirectOK, allOK, warned
}

// filteredPortNote explains the warn state, which is otherwise baffling: the
// daemon is fine, everything the user touches works, yet a port "fails".
const filteredPortNote = `
  Note: the daemon is serving normally — it answers through the redirect — but
  a direct connection to its port is being dropped. That is a VPN kill-switch
  or firewall filtering loopback traffic (in most VPN clients, look for an
  "allow LAN access" setting). Browsers and the vibe CLI are unaffected; only
  tools that dial 127.0.0.1:<port> directly will hang.
`

// listenerDetail describes a listener check, naming the redirect that proved
// the service reachable when a direct dial was filtered.
func listenerDetail(port, viaPort int, status checkStatus) string {
	detail := fmt.Sprintf("127.0.0.1:%d", port)
	if status == statusWarn {
		return fmt.Sprintf("%s — direct dial blocked; serving via the :%d redirect", detail, viaPort)
	}
	return detail
}

// answersOK reports whether the daemon actually answers on 127.0.0.1:port.
//
// Doctor deliberately does not settle for a TCP dial here. A VPN kill-switch
// can accept a direct loopback connection and then forward nothing, which a
// dial reports as healthy while every real request hangs — the exact failure
// this command exists to explain.
func answersOK(scheme string, port int) bool {
	return netprobe.DaemonAnswers(scheme, port, 2*time.Second)
}

// hasLoopback reports whether the resolved addresses include 127.0.0.1.
func hasLoopback(addrs []string) bool {
	for _, a := range addrs {
		if a == "127.0.0.1" {
			return true
		}
	}
	return false
}

func caCertPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".vibe", "certs", "ca.pem")
}

func caCertExists() bool {
	_, err := os.Stat(caCertPath())
	return err == nil
}
