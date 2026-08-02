//go:build darwin

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

const (
	defaultPFHTTPPort = 7999
	defaultPFTLSPort  = 7443
)

// pfHTTPPort/pfTLSPort are the redirect targets. They default to vibe's
// standard ports and are overridden by `pf-apply --http-port/--tls-port`,
// which the com.vibe.pf plist passes from the config that `vibe setup` read.
// pf-apply runs as root, where config.Dir() would resolve to root's home
// rather than the user's, so the ports are handed in rather than re-read.
var (
	pfHTTPPort = defaultPFHTTPPort
	pfTLSPort  = defaultPFTLSPort
)

// pfRDRRules returns vibe's two redirect rules: 80→HTTP port and 443→TLS port.
// They must be present in pf's active ruleset for *.vibe HTTP/HTTPS to reach the
// (unprivileged) daemon, which can't bind low ports itself.
func pfRDRRules() string {
	return fmt.Sprintf("rdr pass on lo0 inet proto tcp from any to 127.0.0.1 port 80 -> 127.0.0.1 port %d\n"+
		"rdr pass on lo0 inet proto tcp from any to 127.0.0.1 port 443 -> 127.0.0.1 port %d\n",
		pfHTTPPort, pfTLSPort)
}

// pfRedirectSentinelHTTP and pfRedirectSentinelHTTPS uniquely mark vibe's two
// redirects in a dumped ruleset. Anchor on the full redirect target (not a bare
// port number) so an unrelated rule that merely mentions the port can't be
// mistaken for ours.
func pfRedirectSentinelHTTP() string {
	return fmt.Sprintf("-> 127.0.0.1 port %d", pfHTTPPort)
}

func pfRedirectSentinelHTTPS() string {
	return fmt.Sprintf("-> 127.0.0.1 port %d", pfTLSPort)
}

// vibeRDRPresent reports whether BOTH of vibe's redirects (:80→7999 and
// :443→7443) are already in the given `pfctl -sn` (translation rules) dump.
// This is the idempotency guard: when the redirect is fully active we do NOT
// reload pf, so we never needlessly disturb a coexisting tool's rules (e.g. a
// VPN's kill-switch anchors).
//
// It requires both sentinels deliberately. If a coexisting tool reloads a
// ruleset that preserved vibe's :443 rdr but dropped the :80 one, anchoring on
// :443 alone would make pf-apply a permanent no-op — the redirect probe (doctor,
// dashboard banner, warnIfRedirectDown) reports it down, but `vibe doctor --fix`
// would see the sentinel and refuse to reload, leaving http://*.vibe broken with
// no way out. Requiring both means a partial ruleset correctly triggers a merge.
func vibeRDRPresent(natRules string) bool {
	return strings.Contains(natRules, pfRedirectSentinelHTTP()) &&
		strings.Contains(natRules, pfRedirectSentinelHTTPS())
}

// buildMergedRuleset assembles a complete pf ruleset that re-adds vibe's rdr
// rules WITHOUT dropping whatever else is currently active, so a coexisting
// tool's anchors (e.g. a VPN's, plus com.apple's) survive the reload instead of
// being clobbered.
//
// pfctl enforces a fixed section order — tables, options, normalization (scrub),
// queueing, translation (nat/rdr), dummynet, filtering — but the `-sn`
// (translation) and `-sr` (normalization + filtering) dumps interleave sections:
// macOS notably emits `scrub-anchor` (normalization) and `dummynet-anchor` in
// the filter dump. A naïve concatenation lands `scrub-anchor` after our rdr and
// pfctl rejects the whole ruleset ("Rules must be in order…"). So we bucket every
// captured line by section and re-emit in the order pfctl accepts, with vibe's
// rdr leading the translation section.
//
// Only ever called when vibeRDRPresent is false, so the captured translation
// rules won't already contain vibe's rdr (no duplication).
func buildMergedRuleset(currentNAT, currentFilter string) string {
	var tables, opts, norm, xlate, dummy, filt []string
	classify := func(block string) {
		for _, raw := range strings.Split(block, "\n") {
			ln := strings.TrimSpace(raw)
			if ln == "" {
				continue
			}
			switch {
			case strings.HasPrefix(ln, "table"):
				tables = append(tables, ln)
			case strings.HasPrefix(ln, "set "):
				opts = append(opts, ln)
			case strings.HasPrefix(ln, "scrub"), strings.HasPrefix(ln, "no scrub"):
				norm = append(norm, ln)
			case strings.HasPrefix(ln, "nat"), strings.HasPrefix(ln, "no nat"),
				strings.HasPrefix(ln, "rdr"), strings.HasPrefix(ln, "no rdr"),
				strings.HasPrefix(ln, "binat"), strings.HasPrefix(ln, "no binat"):
				xlate = append(xlate, ln)
			case strings.HasPrefix(ln, "dummynet"):
				dummy = append(dummy, ln)
			default:
				// pass / block / match / anchor / antispoof / load anchor
				filt = append(filt, ln)
			}
		}
	}
	classify(currentNAT)
	classify(currentFilter)

	var b strings.Builder
	emit := func(lines []string) {
		for _, ln := range lines {
			b.WriteString(ln)
			b.WriteString("\n")
		}
	}
	emit(tables)
	emit(opts)
	emit(norm)
	b.WriteString(pfRDRRules()) // vibe's rdr leads the translation section
	emit(xlate)
	emit(dummy)
	emit(filt)
	return b.String()
}

// reassertPFRules ensures vibe's redirect is active in pf, merging it into the
// live ruleset rather than replacing the ruleset wholesale. Safe to run
// repeatedly (idempotent) and designed to coexist with other pf users.
func reassertPFRules() error {
	// Idempotent fast path: redirect already active → touch nothing. This is
	// what keeps the network-change trigger from fighting a VPN: we only
	// reload pf when our rules have actually gone missing.
	nat, _ := pfctlShow("-sn")
	if vibeRDRPresent(nat) {
		return nil
	}

	filter, _ := pfctlShow("-sr")
	merged := buildMergedRuleset(nat, filter)

	if err := pfctlLoad(merged); err != nil {
		// The captured ruleset didn't round-trip (e.g. an unexpected section
		// ordering). Fall back to a minimal ruleset so the redirect is at least
		// restored — better a working .vibe than a broken one. This last resort
		// DOES replace the main ruleset (the old clobbering behavior), so log it
		// loudly: a recurring fallback means the merge needs attention.
		fmt.Fprintf(os.Stderr, "vibe pf-apply: merge reload failed (%v); falling back to minimal ruleset — this may drop other tools' pf rules\n", err)
		if ferr := pfctlLoad(pfRDRRules() + "pass all\n"); ferr != nil {
			return fmt.Errorf("pf reassert failed (merge: %v; fallback: %v)", err, ferr)
		}
	}

	// Ensure pf is enabled; ignore "already enabled".
	_ = exec.Command("/sbin/pfctl", "-e").Run()
	return nil
}

func pfctlShow(flag string) (string, error) {
	out, err := exec.Command("/sbin/pfctl", flag).Output()
	return string(out), err
}

func pfctlLoad(ruleset string) error {
	cmd := exec.Command("/sbin/pfctl", "-f", "-")
	cmd.Stdin = strings.NewReader(ruleset)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// pfApplyCmd re-applies vibe's pf redirect. It's invoked as root by the
// com.vibe.pf LaunchDaemon at boot and on every network change. Hidden: not part
// of the normal user-facing surface.
var pfApplyCmd = &cobra.Command{
	Use:    "pf-apply",
	Short:  "Re-apply vibe's pf redirect rules (internal)",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return reassertPFRules()
	},
}

func init() {
	pfApplyCmd.Flags().IntVar(&pfHTTPPort, "http-port", defaultPFHTTPPort, "daemon HTTP port to redirect :80 to")
	pfApplyCmd.Flags().IntVar(&pfTLSPort, "tls-port", defaultPFTLSPort, "daemon TLS port to redirect :443 to")
	rootCmd.AddCommand(pfApplyCmd)
}
