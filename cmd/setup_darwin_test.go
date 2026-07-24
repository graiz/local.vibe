//go:build darwin

package cmd

import (
	"strings"
	"testing"
)

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

func TestPatchPFConfStockFile(t *testing.T) {
	patched, changed := patchPFConf(stockPFConf)
	if !changed {
		t.Fatal("expected patch to report a change")
	}

	lines := strings.Split(patched, "\n")
	idx := func(want string) int {
		for i, l := range lines {
			if strings.TrimSpace(l) == want {
				return i
			}
		}
		t.Fatalf("line %q not found in patched pf.conf:\n%s", want, patched)
		return -1
	}

	appleRdr := idx(`rdr-anchor "com.apple/*"`)
	vibeRdr := idx(pfRdrAnchorLine)
	appleFilter := idx(`anchor "com.apple/*"`)
	appleLoad := idx(`load anchor "com.apple" from "/etc/pf.anchors/com.apple"`)
	vibeLoad := idx(pfLoadLine)

	// Translation rules must precede filter rules: our rdr-anchor has to sit
	// directly after Apple's, before anchor "com.apple/*".
	if vibeRdr != appleRdr+1 {
		t.Errorf("vibe rdr-anchor at line %d, want %d (right after Apple's)", vibeRdr, appleRdr+1)
	}
	if vibeRdr > appleFilter {
		t.Errorf("vibe rdr-anchor (line %d) must come before filter anchor (line %d)", vibeRdr, appleFilter)
	}
	if vibeLoad != appleLoad+1 {
		t.Errorf("vibe load anchor at line %d, want %d (right after Apple's)", vibeLoad, appleLoad+1)
	}
}

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

func TestPatchPFConfWithoutAppleLines(t *testing.T) {
	conf := "# custom pf.conf\nset skip on lo0\n"
	patched, changed := patchPFConf(conf)
	if !changed {
		t.Fatal("expected patch to report a change")
	}
	if !strings.Contains(patched, pfRdrAnchorLine) || !strings.Contains(patched, pfLoadLine) {
		t.Fatalf("fallback append missing vibe lines:\n%s", patched)
	}
}

func TestStripPFConfRoundTrip(t *testing.T) {
	patched, _ := patchPFConf(stockPFConf)
	stripped, changed := stripPFConf(patched)
	if !changed {
		t.Fatal("expected strip to report a change")
	}
	if stripped != stockPFConf {
		t.Fatalf("strip did not restore original:\n--- got ---\n%s\n--- want ---\n%s", stripped, stockPFConf)
	}

	_, changed = stripPFConf(stockPFConf)
	if changed {
		t.Fatal("strip on unpatched file reported a change")
	}
}
