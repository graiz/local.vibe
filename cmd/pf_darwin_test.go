//go:build darwin

package cmd

import (
	"strings"
	"testing"
)

func TestPFVibeRDRPresent(t *testing.T) {
	withRules := "rdr pass on lo0 inet proto tcp from any to 127.0.0.1 port = 443 -> 127.0.0.1 port 7443"
	if !vibeRDRPresent(withRules) {
		t.Error("expected vibe rdr to be detected as present")
	}
	if vibeRDRPresent("rdr-anchor \"com.apple/*\" all\nnat-anchor \"xvpn\" all") {
		t.Error("expected absence when only foreign anchors are present")
	}
	if vibeRDRPresent("") {
		t.Error("empty ruleset should not report vibe rdr present")
	}
}

func TestPFBuildMergedRulesetPreservesForeignRules(t *testing.T) {
	// Simulate the real macOS post-flush dumps: -sn carries translation anchors,
	// -sr interleaves normalization (scrub-anchor), dummynet, and filter rules.
	// This is the exact shape that made a naïve concatenation fail with
	// "Rules must be in order…".
	currentNAT := "nat-anchor \"com.apple/*\" all\nrdr-anchor \"com.apple/*\" all"
	currentFilter := "scrub-anchor \"com.apple/*\" all\ndummynet-anchor \"com.apple/*\" all\nanchor \"xvpn\" all\nanchor \"com.apple/*\" all\npass all flags S/SA keep state"

	out := buildMergedRuleset(currentNAT, currentFilter)

	// vibe's redirect must be added...
	if !strings.Contains(out, "port 7443") || !strings.Contains(out, "port 7999") {
		t.Error("merged ruleset is missing vibe's rdr rules")
	}
	// ...without dropping the foreign anchors.
	for _, want := range []string{"com.apple", "xvpn"} {
		if !strings.Contains(out, want) {
			t.Errorf("merged ruleset dropped foreign rule %q", want)
		}
	}
	// Sections must be emitted in pfctl's required order:
	//   normalization (scrub) < translation (rdr) < dummynet < filtering (pass/anchor)
	scrubIdx := strings.Index(out, "scrub-anchor")
	rdrIdx := strings.Index(out, "rdr pass on lo0")
	dummyIdx := strings.Index(out, "dummynet-anchor")
	passIdx := strings.Index(out, "pass all")
	if !(scrubIdx >= 0 && scrubIdx < rdrIdx) {
		t.Errorf("normalization (scrub) must precede translation (scrub=%d, rdr=%d)", scrubIdx, rdrIdx)
	}
	if !(rdrIdx >= 0 && rdrIdx < dummyIdx) {
		t.Errorf("translation must precede dummynet (rdr=%d, dummynet=%d)", rdrIdx, dummyIdx)
	}
	if !(dummyIdx >= 0 && dummyIdx < passIdx) {
		t.Errorf("dummynet must precede filtering (dummynet=%d, pass=%d)", dummyIdx, passIdx)
	}
}

func TestPFBuildMergedRulesetHandlesEmptyCurrent(t *testing.T) {
	out := buildMergedRuleset("", "")
	if !strings.Contains(out, "port 7443") || !strings.Contains(out, "port 7999") {
		t.Error("merged ruleset must contain vibe's rdr even with no current rules")
	}
}
