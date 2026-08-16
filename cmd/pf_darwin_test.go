//go:build darwin

package cmd

import (
	"strings"
	"testing"
)

// stockPFConf is macOS's shipped /etc/pf.conf, the file we patch.
const stockPFConf = `#
# Default PF configuration file.
#
# See pf.conf(5) for syntax.
#

#
# com.apple anchor point
#
scrub-anchor "com.apple/*"
nat-anchor "com.apple/*"
rdr-anchor "com.apple/*"
dummynet-anchor "com.apple/*"
anchor "com.apple/*"
load anchor "com.apple" from "/etc/pf.anchors/com.apple"
`

// lineIndex returns the index of the first line equal (trimmed) to want.
func lineIndex(t *testing.T, content, want string) int {
	t.Helper()
	for i, l := range strings.Split(content, "\n") {
		if strings.TrimSpace(l) == want {
			return i
		}
	}
	t.Fatalf("line %q not found in:\n%s", want, content)
	return -1
}

// pf enforces a section order — translation rules must precede filter rules —
// and rejects the whole file otherwise ("Rules must be in order"). Our
// rdr-anchor therefore has to land in the translation section.
func TestPatchPFConfStockFile(t *testing.T) {
	patched, changed := patchPFConf(stockPFConf)
	if !changed {
		t.Fatal("expected patch to report a change")
	}

	appleRdr := lineIndex(t, patched, `rdr-anchor "com.apple/*"`)
	vibeRdr := lineIndex(t, patched, pfRdrAnchorLine)
	appleFilter := lineIndex(t, patched, `anchor "com.apple/*"`)
	vibeLoad := lineIndex(t, patched, pfLoadLine)
	appleLoad := lineIndex(t, patched, `load anchor "com.apple" from "/etc/pf.anchors/com.apple"`)

	if vibeRdr <= appleRdr {
		t.Errorf("vibe rdr-anchor (line %d) should follow Apple's (line %d)", vibeRdr, appleRdr)
	}
	if vibeRdr > appleFilter {
		t.Errorf("vibe rdr-anchor (line %d) must precede the filter anchor (line %d)", vibeRdr, appleFilter)
	}
	if vibeLoad <= appleLoad {
		t.Errorf("vibe load anchor (line %d) should follow Apple's (line %d)", vibeLoad, appleLoad)
	}
}

// Setup and pf-apply both patch, and pf-apply runs on every network change —
// so a second pass must be a no-op rather than appending duplicates.
func TestPatchPFConfIdempotent(t *testing.T) {
	patched, _ := patchPFConf(stockPFConf)
	again, changed := patchPFConf(patched)
	if changed {
		t.Fatal("second patch reported a change")
	}
	if again != patched {
		t.Fatal("second patch modified content")
	}
	if strings.Count(again, pfRdrAnchorLine) != 1 || strings.Count(again, pfLoadLine) != 1 {
		t.Fatal("vibe lines duplicated")
	}
}

// A half-patched file must gain only what's missing.
func TestPatchPFConfAddsOnlyMissingLine(t *testing.T) {
	conf := strings.Replace(stockPFConf,
		`rdr-anchor "com.apple/*"`,
		`rdr-anchor "com.apple/*"`+"\n"+pfRdrAnchorLine, 1)
	patched, changed := patchPFConf(conf)
	if !changed {
		t.Fatal("expected the missing load line to be added")
	}
	if strings.Count(patched, pfRdrAnchorLine) != 1 {
		t.Errorf("rdr-anchor duplicated:\n%s", patched)
	}
	if strings.Count(patched, pfLoadLine) != 1 {
		t.Errorf("load line not added exactly once:\n%s", patched)
	}
}

// A custom pf.conf with no com.apple lines but with filter rules is the case
// an "append at EOF" fallback gets wrong: the rdr-anchor lands after the
// filter rules and pfctl rejects the file.
func TestPatchPFConfCustomFileKeepsSectionOrder(t *testing.T) {
	conf := "# custom\nset skip on lo0\nblock in all\npass out all\n"
	patched, changed := patchPFConf(conf)
	if !changed {
		t.Fatal("expected a change")
	}
	vibeRdr := lineIndex(t, patched, pfRdrAnchorLine)
	firstFilter := lineIndex(t, patched, "block in all")
	if vibeRdr > firstFilter {
		t.Errorf("rdr-anchor (line %d) must precede the first filter rule (line %d) — pfctl would reject this file:\n%s",
			vibeRdr, firstFilter, patched)
	}
}

// A file with translation rules but no anchors: insert after the last one.
func TestPatchPFConfAfterLastTranslationRule(t *testing.T) {
	conf := "nat on en0 from any to any -> (en0)\nrdr on en0 proto tcp to port 80 -> 127.0.0.1 port 8080\nblock in all\n"
	patched, _ := patchPFConf(conf)
	vibeRdr := lineIndex(t, patched, pfRdrAnchorLine)
	lastXlate := lineIndex(t, patched, "rdr on en0 proto tcp to port 80 -> 127.0.0.1 port 8080")
	firstFilter := lineIndex(t, patched, "block in all")
	if vibeRdr < lastXlate || vibeRdr > firstFilter {
		t.Errorf("rdr-anchor at %d should sit between the last translation rule (%d) and the first filter rule (%d):\n%s",
			vibeRdr, lastXlate, firstFilter, patched)
	}
}

// Uninstall must leave pf.conf exactly as it found it.
func TestStripPFConfRoundTrip(t *testing.T) {
	patched, _ := patchPFConf(stockPFConf)
	stripped, changed := stripPFConf(patched)
	if !changed {
		t.Fatal("expected strip to report a change")
	}
	if stripped != stockPFConf {
		t.Fatalf("strip did not restore the original:\n--- got ---\n%s\n--- want ---\n%s", stripped, stockPFConf)
	}
	if _, changed := stripPFConf(stockPFConf); changed {
		t.Error("strip on an unpatched file reported a change")
	}
}

// The anchor carries the CONFIGURED ports, not hardcoded ones — a
// non-default daemon port otherwise yields a redirect to a dead port that
// `vibe doctor --fix` can never repair.
func TestPFAnchorRulesUseConfiguredPorts(t *testing.T) {
	origHTTP, origTLS := pfHTTPPort, pfTLSPort
	t.Cleanup(func() { pfHTTPPort, pfTLSPort = origHTTP, origTLS })

	pfHTTPPort, pfTLSPort = 9100, 9143
	rules := pfAnchorRules()
	if !strings.Contains(rules, "port 80 -> 127.0.0.1 port 9100") {
		t.Errorf("HTTP redirect does not use the configured port:\n%s", rules)
	}
	if !strings.Contains(rules, "port 443 -> 127.0.0.1 port 9143") {
		t.Errorf("TLS redirect does not use the configured port:\n%s", rules)
	}
	// An anchor adds rules without replacing anyone's, so the blanket pass the
	// old replacement ruleset needed must not reappear here.
	if strings.Contains(rules, "pass all") {
		t.Errorf("anchor must not contain a blanket `pass all`:\n%s", rules)
	}
}

func TestPFLineClassification(t *testing.T) {
	xlate := []string{`rdr-anchor "com.apple/*"`, `nat-anchor "com.apple/*"`,
		"rdr on en0 proto tcp to port 80 -> 127.0.0.1 port 8080", "nat on en0 from any to any -> (en0)"}
	for _, l := range xlate {
		if !pfLineIsTranslation(l) {
			t.Errorf("pfLineIsTranslation(%q) = false", l)
		}
		if pfLineIsFilter(l) {
			t.Errorf("pfLineIsFilter(%q) = true", l)
		}
	}
	filter := []string{`anchor "com.apple/*"`, "block in all", "pass out all", "antispoof for lo0"}
	for _, l := range filter {
		if !pfLineIsFilter(l) {
			t.Errorf("pfLineIsFilter(%q) = false", l)
		}
		if pfLineIsTranslation(l) {
			t.Errorf("pfLineIsTranslation(%q) = true", l)
		}
	}
	// `load anchor` is a directive, neither section.
	if pfLineIsFilter(pfLoadLine) || pfLineIsTranslation(pfLoadLine) {
		t.Errorf("load anchor should classify as neither section")
	}
}

// Real `sudo pfctl -s nat` output on a stock macOS ruleset. The anchor is
// *called* here — this is the healthy state.
const natWithVibeAnchor = `nat-anchor "com.apple/*" all
rdr-anchor "com.apple/*" all
rdr-anchor "com.vibe" all
`

// The same ruleset after `pfctl -F all`, or after anything loads a ruleset
// built from a pf.conf predating vibe's patch. Note this is indistinguishable
// from healthy by every *other* check in this file: the anchor's own contents
// survive a flush, so `pfctl -a com.vibe -s nat` still lists both rdr rules
// and anchorRulesLoaded() still returns true. Only the missing call here says
// the redirect is dead.
const natWithoutVibeAnchor = `nat-anchor "com.apple/*" all
rdr-anchor "com.apple/*" all
`

func TestNATRulesetReferencesAnchor(t *testing.T) {
	if !natRulesetReferencesAnchor(natWithVibeAnchor) {
		t.Error("healthy ruleset reported as missing the anchor call")
	}
	if natRulesetReferencesAnchor(natWithoutVibeAnchor) {
		t.Error("flushed ruleset reported as still calling the anchor")
	}
	// pfctl emits an ALTQ preamble on stderr, but -s nat output can also carry
	// leading whitespace; a trimmed prefix match must survive it.
	if !natRulesetReferencesAnchor("   " + pfRdrAnchorLine + " all") {
		t.Error("indented anchor call not recognized")
	}
	// Empty output (pf never loaded, or pfctl failed) is not a reference.
	if natRulesetReferencesAnchor("") {
		t.Error("empty ruleset reported as calling the anchor")
	}
	// A prefix match must not accept a *different* anchor whose name merely
	// starts with ours — pf treats com.vibe and com.vibe.test as unrelated.
	if natRulesetReferencesAnchor(`rdr-anchor "com.vibe.test" all`) {
		t.Error("a differently-named anchor was accepted as ours")
	}
}
