// Package vibeskill embeds the canonical local.vibe agent skill and installs it
// where coding agents auto-discover it. The skill teaches any agent (Claude
// Code, Codex, etc.) that this machine runs local.vibe and that local dev
// servers must be registered via `vibe` rather than pointed at a guessed port
// or hardcoded localhost URL.
//
// The same SKILL.md content is also checked into the repo's plugin-marketplace
// folder (plugins/local-vibe/skills/local-vibe/SKILL.md); a test guards the two
// copies against drift.
package vibeskill

import (
	_ "embed"
	"os"
	"path/filepath"
)

// Content is the embedded SKILL.md written verbatim at install time.
//
//go:embed SKILL.md
var Content string

// SkillName is the skill's directory name under ~/.claude/skills/.
const SkillName = "local-vibe"

// relDir is the skill directory relative to a home directory.
func relDir() string {
	return filepath.Join(".claude", "skills", SkillName)
}

// Path returns the SKILL.md path that Install writes to for the given home dir.
func Path(home string) string {
	return filepath.Join(home, relDir(), "SKILL.md")
}

// InstallTo writes the embedded skill to <home>/.claude/skills/local-vibe/SKILL.md.
// It is idempotent — an existing file is overwritten with the current content —
// and returns the path written.
func InstallTo(home string) (string, error) {
	dir := filepath.Join(home, relDir())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(Content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// UninstallFrom removes <home>/.claude/skills/local-vibe/. A missing directory
// is not an error, so it is safe to call even when the skill was never installed.
func UninstallFrom(home string) error {
	dir := filepath.Join(home, relDir())
	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
