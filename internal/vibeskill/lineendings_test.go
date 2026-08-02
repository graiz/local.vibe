package vibeskill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// SKILL.md is //go:embed-ed and written verbatim to
// ~/.claude/skills/local-vibe/SKILL.md, so the embedded bytes ARE the shipped
// artifact. A CRLF checkout (git's default on Windows with core.autocrlf=true)
// would therefore install a different file on Windows than on macOS/Linux, and
// make the embedded copy differ from the marketplace copy that
// TestMarketplaceSkillMatchesEmbedded compares byte-for-byte.
//
// .gitattributes pins these files to eol=lf so every checkout produces the
// same bytes on every platform. This test is that guarantee's canary: it fails
// loudly if the normalization is removed or bypassed, rather than letting the
// frontmatter assertions fail with a confusing message about YAML.
func TestEmbeddedSkillUsesLFLineEndings(t *testing.T) {
	if strings.Contains(Content, "\r\n") {
		t.Error("embedded SKILL.md contains CRLF line endings; the installed skill " +
			"must be byte-identical across platforms.\n" +
			"Check .gitattributes (`* text=auto eol=lf`) and re-checkout the file:\n" +
			"  git rm --cached -r . && git reset --hard")
	}
}

// The on-disk sources must match, not just the embedded copy — the marketplace
// file is installed by `/plugin install`, never passing through the binary.
func TestSkillSourcesUseLFLineEndings(t *testing.T) {
	paths := []string{
		filepath.Join(repoRoot(), "internal", "vibeskill", "SKILL.md"),
		filepath.Join(repoRoot(), "plugins", "local-vibe", "skills", "local-vibe", "SKILL.md"),
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("read %s: %v", p, err)
			continue
		}
		if strings.Contains(string(data), "\r\n") {
			t.Errorf("%s has CRLF line endings; expected LF (see .gitattributes)", p)
		}
	}
}
