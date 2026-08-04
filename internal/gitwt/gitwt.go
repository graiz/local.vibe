// Package gitwt holds the small amount of git-worktree logic shared between
// the CLI (worktree detection at `vibe start` time) and the daemon (on-disk
// worktree discovery for the picker).
package gitwt

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Worktree is one linked git worktree of a repository.
type Worktree struct {
	Path   string
	Branch string // short branch name; "" when detached
}

// ListLinked returns the linked worktrees of the repository containing dir,
// excluding the main checkout itself (git always lists it first).
func ListLinked(dir string) ([]Worktree, error) {
	if !mayHaveLinkedWorktrees(dir) {
		return nil, nil
	}
	out, err := exec.Command("git", "-C", dir, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return nil, err
	}
	var all []Worktree
	for _, block := range strings.Split(strings.TrimSpace(string(out)), "\n\n") {
		var wt Worktree
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				wt.Path = strings.TrimPrefix(line, "worktree ")
			case strings.HasPrefix(line, "branch refs/heads/"):
				wt.Branch = strings.TrimPrefix(line, "branch refs/heads/")
			}
		}
		if wt.Path != "" {
			all = append(all, wt)
		}
	}
	if len(all) <= 1 {
		return nil, nil // only the main checkout
	}
	return all[1:], nil
}

// mayHaveLinkedWorktrees is a cheap gate so callers can ask "any worktrees?"
// on a hot-ish path without paying for a git subprocess per repository. Git
// records every linked worktree as a directory under .git/worktrees, so an
// absent or empty one means there are none.
//
// The gate only applies when dir/.git is a real directory — i.e. dir is the
// main checkout. Anything else (a .git FILE, meaning dir is itself a linked
// worktree; or no .git at all, meaning dir is a subdirectory of the repo)
// falls through to git, which resolves the real repository root. Skipping
// there would silently stop discovering worktrees for a route whose Dir is
// nested inside its repo.
func mayHaveLinkedWorktrees(dir string) bool {
	gitPath := filepath.Join(dir, ".git")
	fi, err := os.Stat(gitPath)
	if err != nil || !fi.IsDir() {
		return true // not a main checkout — let git decide
	}
	entries, err := os.ReadDir(filepath.Join(gitPath, "worktrees"))
	if err != nil {
		return false // no .git/worktrees → no linked worktrees
	}
	return len(entries) > 0
}

var slugStrip = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify turns an arbitrary branch or directory name into a DNS-label-safe
// route name fragment: lowercase, illegal runs collapse to a single hyphen,
// leading/trailing hyphens trimmed. A leading "worktree-" prefix (the agent
// harness's branch convention) is stripped so URLs stay short. May return "".
func Slugify(s string) string {
	s = strings.ToLower(s)
	s = slugStrip.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	s = strings.TrimPrefix(s, "worktree-")
	return strings.Trim(s, "-")
}
