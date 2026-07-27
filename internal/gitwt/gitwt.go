// Package gitwt holds the small amount of git-worktree logic shared between
// the CLI (worktree detection at `vibe start` time) and the daemon (on-disk
// worktree discovery for the picker).
package gitwt

import (
	"os/exec"
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
