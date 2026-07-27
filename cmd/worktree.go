package cmd

import (
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

func gitOut(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
