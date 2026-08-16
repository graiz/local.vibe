//go:build darwin

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// vibe's redirect rules live in their own pf anchor, referenced from
// /etc/pf.conf — never loaded as a replacement main ruleset.
//
// The old approach (`pfctl -ef -` with a hand-built ruleset) had two defects
// that an anchor fixes structurally. It detached Apple's `com.apple/*` anchors
// for as long as vibe's ruleset was loaded, silently disabling the Application
// Firewall, AirDrop and Internet Sharing. And anything that reloaded
// /etc/pf.conf — a VPN client, Internet Sharing / vmnet backing Docker Desktop
// or a VM, a macOS update, the boot race with com.apple.pfctl — replaced the
// whole ruleset and took vibe's redirect with it, with no way back short of an
// obscure launchctl incantation.
//
// With the anchor, those same reloads *re-install* vibe's rules (pf.conf names
// the anchor and loads it), and Apple's anchors are never touched. It also
// removes the ruleset-merging machinery this file used to carry: there is no
// longer any need to capture, bucket by section, and re-emit the live ruleset,
// because vibe no longer owns the main ruleset at all.
const (
	pfAnchorName = "com.vibe"
	pfAnchorFile = "/etc/pf.anchors/com.vibe"
	pfConfPath   = "/etc/pf.conf"

	pfRdrAnchorLine = `rdr-anchor "com.vibe"`
	pfLoadLine      = `load anchor "com.vibe" from "/etc/pf.anchors/com.vibe"`
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

// pfAnchorRules returns the anchor's contents: vibe's two redirects, 80→HTTP
// port and 443→TLS port. They must be active for *.vibe HTTP/HTTPS to reach
// the (unprivileged) daemon, which can't bind low ports itself.
//
// Note there is no `pass all` here. The previous replacement ruleset carried
// one — necessary only because replacing the main ruleset also replaced
// everyone else's filter rules. An anchor adds rules without removing any, so
// the blanket pass is both unnecessary and undesirable.
func pfAnchorRules() string {
	return fmt.Sprintf("rdr pass on lo0 inet proto tcp from any to 127.0.0.1 port 80 -> 127.0.0.1 port %d\n"+
		"rdr pass on lo0 inet proto tcp from any to 127.0.0.1 port 443 -> 127.0.0.1 port %d\n",
		pfHTTPPort, pfTLSPort)
}

// writePFAnchorFile writes the anchor's rules with the currently configured
// ports. Rewritten on every pf-apply so a port change in config.json takes
// effect without re-running setup.
func writePFAnchorFile() error {
	if err := os.MkdirAll(filepath.Dir(pfAnchorFile), 0755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(pfAnchorFile), err)
	}
	if err := os.WriteFile(pfAnchorFile, []byte(pfAnchorRules()), 0644); err != nil {
		return fmt.Errorf("write %s: %w", pfAnchorFile, err)
	}
	return nil
}

// containsPFLine reports whether conf has want as a whole (trimmed) line.
func containsPFLine(conf, want string) bool {
	for _, l := range strings.Split(conf, "\n") {
		if strings.TrimSpace(l) == want {
			return true
		}
	}
	return false
}

// pfLineIsFilter reports whether a pf.conf line begins the filter section.
// Once a filter rule appears, no translation rule (or rdr-anchor) may follow —
// pfctl rejects the file with "Rules must be in order".
func pfLineIsFilter(line string) bool {
	t := strings.TrimSpace(line)
	for _, p := range []string{"anchor", "pass", "block", "match", "antispoof"} {
		if t == p || strings.HasPrefix(t, p+" ") {
			return true
		}
	}
	return false
}

// pfLineIsTranslation reports whether a line belongs to the translation
// section, which is where our rdr-anchor has to live.
func pfLineIsTranslation(line string) bool {
	t := strings.TrimSpace(line)
	for _, p := range []string{"rdr-anchor", "nat-anchor", "rdr", "nat", "binat", "binat-anchor", "no rdr", "no nat"} {
		if t == p || strings.HasPrefix(t, p+" ") {
			return true
		}
	}
	return false
}

// patchPFConf inserts vibe's two lines into pf.conf content, preserving pf's
// required section order. Returns the possibly-modified content and whether
// anything changed.
//
// Placement rules, in order of preference:
//   - after the last existing translation line (Apple's `rdr-anchor
//     "com.apple/*"` on a stock file) — always correct, since translation
//     rules are contiguous;
//   - otherwise immediately before the first filter line, so we still land in
//     the translation section of a custom pf.conf;
//   - otherwise at the end, for a file with neither.
//
// The naive version of this appended at EOF whenever Apple's lines were
// absent, which silently produced an invalid ordering on any custom pf.conf
// that had filter rules. `load anchor` is a directive rather than a rule and
// is safe at the end of the file, which is where Apple puts its own.
func patchPFConf(conf string) (string, bool) {
	hasRdr := containsPFLine(conf, pfRdrAnchorLine)
	hasLoad := containsPFLine(conf, pfLoadLine)
	if hasRdr && hasLoad {
		return conf, false
	}

	lines := strings.Split(conf, "\n")
	out := make([]string, 0, len(lines)+2)

	if !hasRdr {
		insertAt := -1
		for i, l := range lines {
			if pfLineIsTranslation(l) {
				insertAt = i + 1 // keep scanning: we want the LAST one
			}
		}
		if insertAt == -1 {
			for i, l := range lines {
				if pfLineIsFilter(l) {
					insertAt = i
					break
				}
			}
		}
		if insertAt == -1 {
			insertAt = len(lines)
		}
		for i, l := range lines {
			if i == insertAt {
				out = append(out, pfRdrAnchorLine)
			}
			out = append(out, l)
		}
		if insertAt >= len(lines) {
			out = append(out, pfRdrAnchorLine)
		}
		lines = out
	}

	if !hasLoad {
		// Trailing blank line is conventional; keep the directive above it.
		if n := len(lines); n > 0 && strings.TrimSpace(lines[n-1]) == "" {
			lines = append(lines[:n-1], pfLoadLine, "")
		} else {
			lines = append(lines, pfLoadLine)
		}
	}
	return strings.Join(lines, "\n"), true
}

// stripPFConf removes vibe's lines from pf.conf content.
func stripPFConf(conf string) (string, bool) {
	lines := strings.Split(conf, "\n")
	out := make([]string, 0, len(lines))
	changed := false
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == pfRdrAnchorLine || t == pfLoadLine {
			changed = true
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n"), changed
}

// ensurePFConfReferencesAnchor patches /etc/pf.conf to name and load vibe's
// anchor, validating the result before it replaces the live file. Reports
// whether pf.conf was modified.
//
// This runs on every pf-apply, not just at setup, because /etc/pf.conf is a
// system file: a macOS update can replace it wholesale and drop our two lines,
// after which the anchor exists but nothing loads it. Re-patching here is what
// makes `vibe doctor --fix` able to recover from that.
func ensurePFConfReferencesAnchor() (changed bool, err error) {
	conf, err := os.ReadFile(pfConfPath)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", pfConfPath, err)
	}
	patched, changed := patchPFConf(string(conf))
	if !changed {
		return false, nil
	}
	// Vet before swapping: a broken pf.conf would fail every future reload,
	// including Apple's at boot. Write the candidate beside the real file so
	// the rename is atomic (same filesystem).
	tmp := pfConfPath + ".vibe.tmp"
	if err := os.WriteFile(tmp, []byte(patched), 0644); err != nil {
		return false, fmt.Errorf("write %s: %w", tmp, err)
	}
	defer os.Remove(tmp) // no-op once renamed
	if out, err := exec.Command("/sbin/pfctl", "-n", "-f", tmp).CombinedOutput(); err != nil {
		return false, fmt.Errorf("patched %s failed validation (left unchanged): %w — %s",
			pfConfPath, err, strings.TrimSpace(string(out)))
	}
	if err := os.Rename(tmp, pfConfPath); err != nil {
		return false, fmt.Errorf("replace %s: %w", pfConfPath, err)
	}
	return true, nil
}

// pfEnabled reports whether pf is currently enabled.
func pfEnabled() bool {
	out, err := exec.Command("/sbin/pfctl", "-s", "info").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "Status: Enabled")
}

// anchorRulesLoaded reports whether the live anchor already holds both of
// vibe's current redirects — the idempotency check that keeps the
// network-change trigger from reloading pf when nothing is wrong.
func anchorRulesLoaded() bool {
	out, err := exec.Command("/sbin/pfctl", "-a", pfAnchorName, "-s", "nat").Output()
	if err != nil {
		return false
	}
	live := string(out)
	return strings.Contains(live, fmt.Sprintf("-> 127.0.0.1 port %d", pfHTTPPort)) &&
		strings.Contains(live, fmt.Sprintf("-> 127.0.0.1 port %d", pfTLSPort))
}

// natRulesetReferencesAnchor reports whether `pfctl -s nat` output contains a
// call to vibe's anchor. Split from the exec so the parsing is testable: this
// is the one live-state check in this file whose answer decides whether we
// touch the *main* ruleset, so it's worth pinning to a fixture.
//
// Matched per line rather than as a substring of the whole output. pfctl
// prints anchor calls as `rdr-anchor "com.vibe" all`, so the whole line is
// never an exact match — but a bare Contains would also accept the string
// buried in some future prefixed form.
func natRulesetReferencesAnchor(nat string) bool {
	for _, l := range strings.Split(nat, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), pfRdrAnchorLine) {
			return true
		}
	}
	return false
}

// anchorReferencedInMainRuleset reports whether the main pf ruleset contains
// an rdr-anchor reference to com.vibe. The anchor can hold correct rules
// while the main ruleset has no reference to it — pf never evaluates those
// rules in that state, so the redirect is dead even though every other check
// passes.
//
// `pfctl -F all` produces exactly this: it flushes anchor *calls* out of the
// main ruleset while the anchor's *contents* survive. So does anything that
// loads a ruleset built from a pf.conf predating vibe's patch (a macOS update
// that replaced /etc/pf.conf, a VPN loading its own file).
func anchorReferencedInMainRuleset() bool {
	out, err := exec.Command("/sbin/pfctl", "-s", "nat").Output()
	if err != nil {
		// Fail closed: assume the reference is missing. The caller only acts
		// on that when pf.conf actually carries our lines, so the worst case
		// is a redundant reload of the canonical system file.
		return false
	}
	return natRulesetReferencesAnchor(string(out))
}

// reassertPFRules makes vibe's redirect active: anchor file current, pf.conf
// referencing it, rules loaded, pf enabled. Idempotent and safe to run
// repeatedly — it is invoked at boot and on every network change.
//
// Two of the three repairs below touch only vibe's own anchor and cannot
// disturb a coexisting pf user. The third — reloading /etc/pf.conf — replaces
// the whole main ruleset, and is unavoidable: an anchor *call* can only be
// added by reloading the ruleset that contains it, so there is no way to
// repair a flushed reference without it. That means a VPN which loaded its
// own ruleset (rather than /etc/pf.conf) will have its rules replaced by the
// system file the next time this runs. We accept that — the user asked for the
// redirect, /etc/pf.conf is the canonical ruleset, and this is the same reload
// macOS itself performs at boot — but it is reported on stderr so it is
// visible in the com.vibe.pf log when someone debugs VPN interaction.
func reassertPFRules() error {
	if err := writePFAnchorFile(); err != nil {
		return err
	}

	confChanged, err := ensurePFConfReferencesAnchor()
	if err != nil {
		// A custom pf.conf we can't safely patch shouldn't be a hard failure:
		// say exactly what to add, and still try to load the anchor so the
		// redirect works for this boot.
		fmt.Fprintf(os.Stderr,
			"vibe pf-apply: could not update %s (%v)\n"+
				"  add these two lines manually — the first in the translation section, the second at the end:\n"+
				"    %s\n    %s\n",
			pfConfPath, err, pfRdrAnchorLine, pfLoadLine)
	}
	// err == nil is exactly "pf.conf on disk carries our two lines" — either it
	// already did (changed=false) or we just patched it (changed=true).
	confHasOurLines := err == nil

	// Two independent concerns, checked independently: (1) the main ruleset
	// must *call* our anchor, and (2) the anchor must *contain* our current
	// rdr rules. Either can be broken without the other — `pfctl -F all`
	// breaks only the first, a port change in config.json only the second —
	// so neither check may be the else-branch of the other.
	//
	// (1) Reload the whole file, which is what attaches the anchor. Gated on
	// the file actually having our lines: when pf.conf can't be patched, a
	// reload cannot install the reference, so it would be a pure loss —
	// replacing the live main ruleset on every network change, forever, and
	// never reaching the state that would stop it.
	if confHasOurLines && (confChanged || !anchorReferencedInMainRuleset()) {
		if err := pfctlRun("-f", pfConfPath); err != nil {
			// Not fatal: fall through to the anchor load below so a stale but
			// still-referenced anchor gets refreshed, and so pf still gets
			// enabled. Returning here would skip both.
			fmt.Fprintf(os.Stderr, "vibe pf-apply: reload %s failed: %v\n", pfConfPath, err)
		} else {
			fmt.Fprintf(os.Stderr, "vibe pf-apply: reloaded %s to reattach the %s anchor\n", pfConfPath, pfAnchorName)
		}
	}

	// (2) Load just our anchor. Never disturbs anyone else's rules. Runs even
	// after a reload above, which catches a reload that silently failed to
	// populate the anchor — and is the only repair available when pf.conf
	// can't be patched, so the rules are ready the moment the user adds the
	// reference by hand.
	if !anchorRulesLoaded() {
		if err := pfctlRun("-a", pfAnchorName, "-f", pfAnchorFile); err != nil {
			return fmt.Errorf("load anchor %s: %w", pfAnchorName, err)
		}
	}

	// Enable only when disabled. `pfctl -E` increments an enable reference
	// count each time it runs; on a trigger that fires per network change that
	// would climb indefinitely, so check first and use plain -e.
	if !pfEnabled() {
		_ = exec.Command("/sbin/pfctl", "-e").Run()
	}
	return nil
}

func pfctlRun(args ...string) error {
	cmd := exec.Command("/sbin/pfctl", args...)
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
