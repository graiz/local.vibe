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

		redirectOK, allOK := runDoctor(cfg)

		if allOK {
			fmt.Println("\nAll checks passed.")
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
			if _, allOK = runDoctor(cfg); allOK {
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

// runDoctor runs every check and prints a pass/fail line for each. It returns
// (redirectOK, allOK): redirectOK gates whether --fix has anything to do; allOK
// is true only when every critical check passed.
func runDoctor(cfg *config.Config) (redirectOK, allOK bool) {
	tld := cfg.Daemon.TLD
	allOK = true
	redirectOK = true

	report := func(name string, ok, critical bool, detail string) {
		mark := "✓"
		if !ok {
			mark = "✗"
		}
		fmt.Printf("  %s %-28s %s\n", mark, name, detail)
		if !ok && critical {
			allOK = false
		}
	}

	// Daemon reachable (over the unix socket / TCP the CLI uses).
	h, herr := client.New().Health()
	if herr == nil {
		report("daemon", true, true, fmt.Sprintf("ok (%d routes, up %s)", h.Routes,
			(time.Duration(h.Uptime)*time.Second).Round(time.Second)))
	} else {
		report("daemon", false, true, "not running — start it or run `vibe setup`")
	}

	// HTTP listener.
	report("http listener", dialOK(cfg.Daemon.Port), true, fmt.Sprintf("127.0.0.1:%d", cfg.Daemon.Port))

	// TLS listener (only when TLS is on).
	if cfg.Daemon.TLS.Enabled {
		report("tls listener", dialOK(cfg.Daemon.TLS.Port), true, fmt.Sprintf("127.0.0.1:%d", cfg.Daemon.TLS.Port))
	}

	// DNS: a .vibe name must resolve to loopback.
	host := "local." + tld
	addrs, derr := net.LookupHost(host)
	report("dns", derr == nil && hasLoopback(addrs), true, host+" → 127.0.0.1")

	// Redirect path — the privileged-port layer. A plain dial to :80/:443 only
	// succeeds if the redirect is forwarding to the daemon, so it catches a
	// flushed pf ruleset without needing root.
	rOK := dialOK(80)
	report(redirectMechanismName()+" redirect :80", rOK, true, "127.0.0.1:80 → 7999")
	if cfg.Daemon.TLS.Enabled {
		r443 := dialOK(443)
		rOK = rOK && r443
		report(redirectMechanismName()+" redirect :443", r443, true, "127.0.0.1:443 → 7443")
	}
	redirectOK = rOK

	// Certs (best-effort, non-critical).
	if cfg.Daemon.TLS.Enabled {
		report("tls cert", caCertExists(), false, caCertPath())
	}

	return redirectOK, allOK
}

// dialOK reports whether a TCP connection to 127.0.0.1:port succeeds quickly.
func dialOK(port int) bool {
	return netprobe.PortOpen(port, 1500*time.Millisecond)
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
