package cmd

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/graiz/local.vibe/internal/gitwt"
)

// detectWorktree reports whether dir is inside a *linked* git worktree (not
// the main checkout). In a linked worktree, --git-dir resolves under the main
// repo's .git/worktrees/<name> and therefore differs from --git-common-dir.
// Any git failure (not a repo, git missing) reads as "not a worktree".
func detectWorktree(dir string) bool {
	gitDir, err1 := gitOut(dir, "rev-parse", "--git-dir")
	commonDir, err2 := gitOut(dir, "rev-parse", "--git-common-dir")
	if err1 != nil || err2 != nil {
		return false
	}
	abs := func(p string) string {
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		return filepath.Clean(p)
	}
	return abs(gitDir) != abs(commonDir)
}

// worktreeSlug derives the subdomain label for a worktree route: the current
// branch name slugified, falling back to the directory basename (detached
// HEAD, or a branch that slugs to nothing).
func worktreeSlug(dir string) string {
	if branch, err := gitOut(dir, "rev-parse", "--abbrev-ref", "HEAD"); err == nil && branch != "" && branch != "HEAD" {
		if s := gitwt.Slugify(branch); s != "" {
			return s
		}
	}
	return gitwt.Slugify(filepath.Base(dir))
}

// mainCheckoutDir returns the root of the repository's main working tree, or
// "" when dir isn't in a repo. --git-common-dir points at the main checkout's
// .git directory, so its parent is that checkout's root.
func mainCheckoutDir(dir string) string {
	common, err := gitOut(dir, "rev-parse", "--git-common-dir")
	if err != nil || common == "" {
		return ""
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	return filepath.Dir(filepath.Clean(common))
}

// vibeJSONName reads the `name` field of dir/vibe.json, or "" if the file is
// missing, unreadable, or malformed.
func vibeJSONName(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "vibe.json"))
	if err != nil {
		return ""
	}
	var cfg vibeConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return ""
	}
	return cfg.Name
}

func gitOut(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
