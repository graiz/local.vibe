package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/graiz/local.vibe/internal/gitwt"
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

// realpath resolves symlinks so paths from different sources compare equal —
// git reports resolved paths (macOS: /private/var) while callers may hold the
// symlinked form (/var). Falls back to a plain Clean when resolution fails.
func realpath(p string) string {
	if rp, err := filepath.EvalSymlinks(p); err == nil {
		return rp
	}
	return filepath.Clean(p)
}

// discoveredWorktree is a linked git worktree found on disk that has no
// registered vibe route yet — the picker offers to register-and-start it.
type discoveredWorktree struct {
	Path   string
	Branch string
	Slug   string // proposed subdomain label
}

// discoverUnregisteredWorktrees shells out to `git worktree list` in a
// managed route's Dir and returns the linked worktrees that aren't already
// registered as child routes. This is what makes agent-created worktrees show
// up with zero cooperation: the agent only has to `git worktree add`. Runs
// only off the hot path (start-page render and stopped-route recovery).
// Errors (no git, Dir not a repo) read as "no worktrees".
func (s *Server) discoverUnregisteredWorktrees(route *Route) []discoveredWorktree {
	if route.Type != RouteManaged || route.Dir == "" || route.Parent != "" {
		return nil
	}
	wts, err := gitwt.ListLinked(route.Dir)
	if err != nil || len(wts) == 0 {
		return nil
	}
	// Existing children by real path (git resolves symlinks — macOS /var vs
	// /private/var — so compare resolved paths), plus all taken route names.
	registered := make(map[string]bool)
	names := make(map[string]bool)
	for _, r := range s.table.List() {
		names[r.Name] = true
		if r.Parent == route.Name && r.Dir != "" {
			registered[realpath(r.Dir)] = true
		}
	}
	var out []discoveredWorktree
	for _, wt := range wts {
		if registered[realpath(wt.Path)] {
			continue
		}
		slug := gitwt.Slugify(wt.Branch)
		if slug == "" {
			slug = gitwt.Slugify(filepath.Base(wt.Path))
		}
		// A taken name means some other route already claimed the slug —
		// skip rather than guess at a suffix the user never chose.
		if slug == "" || names[slug+"."+route.Name] {
			continue
		}
		out = append(out, discoveredWorktree{Path: wt.Path, Branch: wt.Branch, Slug: slug})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out
}

// hasWorktrees reports whether the app has any worktree at all — a registered
// child route (running or not) or an unregistered one discovered on disk.
// Used to gate on-demand auto-start: a stopped app with worktrees serves the
// picker instead of silently respawning main, because the visitor may well
// have been looking for a worktree.
func (s *Server) hasWorktrees(route *Route) bool {
	for _, r := range s.table.List() {
		if r.Parent == route.Name {
			return true
		}
	}
	return len(s.discoverUnregisteredWorktrees(route)) > 0
}

// worktreeDirGone reports whether a worktree route's source checkout is no
// longer a git worktree. The probe is the `.git` link inside the dir:
// `git worktree add` creates it and `git worktree remove` always deletes it,
// so its absence covers both a fully deleted dir AND the leftover-folder case
// (worktree removed from git, but stray files or a file-sync resurrected the
// directory — seen with SynologyDrive). Only meaningful for routes with a
// Parent. A stat error other than IsNotExist counts as "still there" so a
// transient FS hiccup can't deregister a live route.
func worktreeDirGone(r *Route) bool {
	if r.Parent == "" || r.Dir == "" {
		return false
	}
	return dirIsGoneWorktree(r.Dir)
}

// dirIsGoneWorktree reports whether dir is a worktree that no longer exists,
// as opposed to one that is merely unreachable right now.
//
// "Gone" is a destructive verdict: it deregisters the route and SIGTERMs the
// child. A bare os.IsNotExist on dir/.git can't tell a removed worktree from a
// volume that isn't mounted yet — this repo lives on a file-sync mount and the
// daemon starts at login, so a slow mount would silently deregister every
// worktree route and kill its server on every reboot. The parent directory
// disambiguates: a `git worktree remove` (or a leftover folder resurrected by
// file-sync) leaves the PARENT in place, while an unmounted or still-syncing
// volume takes the parent with it.
func dirIsGoneWorktree(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
		return false // present, or an error we shouldn't act on
	}
	// .git is absent. Only trust that if the containing directory is readable —
	// otherwise the whole path is unavailable and this says nothing.
	if _, err := os.Stat(filepath.Dir(dir)); err != nil {
		return false
	}
	return true
}
