package daemon

import (
	"fmt"
	"os"
	"strings"
)

// defaultWorktreeIdleMinutes is the idle_timeout applied to worktree routes
// that don't specify one. Agents abandon worktree servers far more often than
// humans abandon main, so they stop themselves instead of holding ports and
// CPU forever; the route survives the stop and the next visit auto-starts it.
const defaultWorktreeIdleMinutes = 60

// parseRouteName validates a route name and returns the parent app for
// worktree names. Single-label names ("myapp") return parent "". A dotted
// name must be exactly <worktree>.<app> with both labels validName-conforming
// and neither label "local"; deeper nesting is rejected.
func parseRouteName(name string) (parent string, err error) {
	parts := strings.Split(name, ".")
	switch len(parts) {
	case 1:
		if !validName.MatchString(name) {
			return "", fmt.Errorf("name must be lowercase letters, digits, or hyphens")
		}
		return "", nil
	case 2:
		for _, p := range parts {
			if !validName.MatchString(p) {
				return "", fmt.Errorf("each label of %q must be lowercase letters, digits, or hyphens", name)
			}
			if p == "local" {
				return "", fmt.Errorf("'local' is reserved for the dashboard")
			}
		}
		return parts[1], nil
	default:
		return "", fmt.Errorf("route names allow at most one dot: <worktree>.<app>")
	}
}

// worktreeParent returns the parent label of a two-label host name
// ("feature-auth.myapp" → "myapp") and whether the name has that shape.
// Purely syntactic — no table lookup.
func worktreeParent(name string) (string, bool) {
	parts := strings.Split(name, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

// worktreeDirGone reports whether a worktree route's source directory has
// been deleted (git worktree remove, merged branch). Only meaningful for
// routes with a Parent. A stat error other than IsNotExist counts as "still
// there" so a transient FS hiccup can't deregister a live route.
func worktreeDirGone(r *Route) bool {
	if r.Parent == "" || r.Dir == "" {
		return false
	}
	_, err := os.Stat(r.Dir)
	return os.IsNotExist(err)
}
