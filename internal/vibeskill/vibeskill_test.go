package vibeskill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContentHasValidFrontmatter(t *testing.T) {
	if !strings.HasPrefix(Content, "---\n") {
		t.Fatal("SKILL.md must start with YAML frontmatter delimiter")
	}
	// Frontmatter closes with a second "---" line.
	if strings.Count(Content, "\n---") < 1 {
		t.Fatal("SKILL.md frontmatter is not closed")
	}
	for _, want := range []string{"name: local-vibe", "description:"} {
		if !strings.Contains(Content, want) {
			t.Errorf("SKILL.md frontmatter missing %q", want)
		}
	}
}

// The skill's whole point is steering agents away from guessed ports/URLs and
// toward the vibe CLI + the authoritative setup guide. Guard those keywords so
// a future edit can't quietly gut the intent.
func TestContentSteersAwayFromGuessing(t *testing.T) {
	for _, want := range []string{"vibe start", "http://localhost:7999/setup.md", "port", "localhost"} {
		if !strings.Contains(Content, want) {
			t.Errorf("SKILL.md missing expected keyword %q", want)
		}
	}
}

func TestPath(t *testing.T) {
	got := Path("/home/alice")
	want := filepath.Join("/home/alice", ".claude", "skills", "local-vibe", "SKILL.md")
	if got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}

func TestInstallToWritesContent(t *testing.T) {
	home := t.TempDir()

	path, err := InstallTo(home)
	if err != nil {
		t.Fatalf("InstallTo: %v", err)
	}
	if path != Path(home) {
		t.Errorf("InstallTo returned %q, want %q", path, Path(home))
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read installed skill: %v", err)
	}
	if string(got) != Content {
		t.Error("installed SKILL.md does not match embedded content")
	}
}

func TestInstallToIsIdempotent(t *testing.T) {
	home := t.TempDir()

	// Pre-seed with stale content to prove Install overwrites.
	dir := filepath.Join(home, ".claude", "skills", "local-vibe")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := InstallTo(home); err != nil {
		t.Fatalf("InstallTo: %v", err)
	}
	got, _ := os.ReadFile(Path(home))
	if string(got) != Content {
		t.Error("InstallTo did not overwrite stale content")
	}
}

func TestUninstallFrom(t *testing.T) {
	home := t.TempDir()
	if _, err := InstallTo(home); err != nil {
		t.Fatal(err)
	}

	if err := UninstallFrom(home); err != nil {
		t.Fatalf("UninstallFrom: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "local-vibe")); !os.IsNotExist(err) {
		t.Error("skill directory still present after UninstallFrom")
	}

	// Second call on an absent dir must be a no-op, not an error.
	if err := UninstallFrom(home); err != nil {
		t.Errorf("UninstallFrom on missing dir: %v", err)
	}
}
