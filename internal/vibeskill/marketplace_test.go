package vibeskill

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// repoRoot is two levels up from this package (internal/vibeskill).
func repoRoot() string {
	return filepath.Join("..", "..")
}

// The plugin-marketplace copy of SKILL.md must stay byte-identical to the
// embedded canonical content — the binary installs one and users install the
// other, and they must teach the same thing. This test is the drift guard: if
// someone edits internal/vibeskill/SKILL.md without re-copying it into the
// marketplace plugin (or vice versa), CI fails here.
func TestMarketplaceSkillMatchesEmbedded(t *testing.T) {
	mktPath := filepath.Join(repoRoot(), "plugins", "local-vibe", "skills", "local-vibe", "SKILL.md")
	got, err := os.ReadFile(mktPath)
	if err != nil {
		t.Fatalf("read marketplace SKILL.md: %v", err)
	}
	if string(got) != Content {
		t.Errorf("marketplace SKILL.md drifted from embedded content.\n"+
			"Re-sync with:\n  cp internal/vibeskill/SKILL.md %s", mktPath)
	}
}

func TestMarketplaceManifestsAreValid(t *testing.T) {
	cases := []struct {
		path string
		keys []string
	}{
		{filepath.Join(repoRoot(), ".claude-plugin", "marketplace.json"), []string{"name", "owner", "plugins"}},
		{filepath.Join(repoRoot(), "plugins", "local-vibe", ".claude-plugin", "plugin.json"), []string{"name", "description", "version"}},
	}
	for _, c := range cases {
		data, err := os.ReadFile(c.path)
		if err != nil {
			t.Errorf("read %s: %v", c.path, err)
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			t.Errorf("%s is not valid JSON: %v", c.path, err)
			continue
		}
		for _, k := range c.keys {
			if _, ok := m[k]; !ok {
				t.Errorf("%s missing required key %q", c.path, k)
			}
		}
	}
}
