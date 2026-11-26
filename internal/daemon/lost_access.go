package daemon

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/graiz/local.vibe/internal/config"
)

// A long-running managed child can lose access to its own working tree while
// staying alive: when a file-sync provider (Synology Drive, Dropbox, iCloud)
// restarts under ~/Library/CloudStorage, macOS can refuse the *existing*
// process (EPERM on open/scandir) while a freshly spawned one reads the same
// paths fine. Every lifecycle signal vibe has reads healthy — the PID is
// alive, the port answers — and the app returns 500 on every request, its log
// filling with `EPERM: operation not permitted, scandir '<its own dir>/app'`.
// Observed on a Next.js route that served 500s for days. The only fix is a
// restart, so the proxy does that when a page load hits the signature.

// errLostDirAccess is returned from ModifyResponse to divert a 5xx from a
// child in this state into the ErrorHandler, which restarts it.
var errLostDirAccess = errors.New("managed child lost access to its working directory")

// lostAccessRestartInterval bounds restarts per route: if the fresh child
// hits the same wall, the 5xx passes through rather than restart-looping.
const lostAccessRestartInterval = 2 * time.Minute

var dirEPERMRe = regexp.MustCompile(`EPERM: operation not permitted, \w+ '([^']+)'`)

// deniedPathInDir returns a path from the log tail that the child was refused
// (EPERM) inside dir and that the daemon itself can open right now. Both halves
// matter: a path outside dir is the app's business, and a path the daemon
// can't open either is a real permission problem a restart won't fix.
//
// Paths are compared resolved: routes are commonly registered through a
// symlink (~/dev/app → a CloudStorage dir) while Node prints the realpath in
// its error, and a raw prefix test never matched on exactly that layout.
func deniedPathInDir(tail, dir string) (string, bool) {
	if dir == "" {
		return "", false
	}
	prefix := realpath(dir) + string(filepath.Separator)
	for _, m := range dirEPERMRe.FindAllStringSubmatch(tail, -1) {
		p := m[1]
		if !strings.HasPrefix(realpath(p), prefix) {
			continue
		}
		if f, err := os.Open(p); err == nil {
			f.Close()
			return p, true
		}
	}
	return "", false
}

// lostDirAccess reports whether resp is a page-load 5xx from a managed child
// that has lost access to its working directory. Page loads only: the restart
// answers with the starting page, which must never replace an XHR or asset
// response. The cheap checks — method, config, the per-route cooldown — run
// before the log is read, so an app that 500s on every navigation during
// normal debugging doesn't pay a file read per page load.
//
// Detection does not claim the restart cooldown; restartLostAccess does, and
// only when it actually runs a restart.
func (s *Server) lostDirAccess(route *Route, resp *http.Response) bool {
	if resp.StatusCode < 500 || resp.Request == nil || !isPageLoad(resp.Request) {
		return false
	}
	if !s.cfg.Daemon.AutoStartEnabled() || route.Cmd == "" {
		return false
	}
	if s.lostAccessCoolingDown(route.Name) {
		return false
	}
	tail := tailLogFile(filepath.Join(config.Dir(), route.Name+".log"), 12)
	path, ok := deniedPathInDir(tail, route.Dir)
	if !ok {
		return false
	}
	fmt.Fprintf(os.Stderr, "vibe: %s was denied %s (EPERM) but the path is readable; restarting it\n", route.Name, path)
	return true
}

func (s *Server) lostAccessCoolingDown(name string) bool {
	prev, loaded := s.lostAccessRestarts.Load(name)
	return loaded && time.Since(prev.(time.Time)) < lostAccessRestartInterval
}

func isPageLoad(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	if mode := r.Header.Get("Sec-Fetch-Mode"); mode != "" {
		return mode == "navigate"
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// restartLostAccess stops the stale child and spawns a fresh one in the
// background, answering with the starting page — which polls until the new
// child binds, or reloads into the start page's log + hint if it fails. When
// another start already owns the route, only the page is served: the cooldown
// is claimed by a restart that runs, never by one that yields.
//
// The respawn goes through startManagedNow (which re-reads vibe.json) and
// honors the same worktree gate as on-demand recovery: an app with worktrees
// gets the picker instead of main silently respawning.
func (s *Server) restartLostAccess(w http.ResponseWriter, r *http.Request, route *Route) {
	if s.beginAutoStart(route.Name) {
		s.lostAccessRestarts.Store(route.Name, time.Now())
		go func() {
			defer s.endAutoStart(route.Name)
			s.stopManaged(route)
			for i := 0; i < 50 && s.isPortReady(route.Port); i++ {
				time.Sleep(100 * time.Millisecond)
			}
			if s.hasWorktrees(route) {
				return
			}
			if err := s.startManagedNow(route); err != nil {
				fmt.Fprintf(os.Stderr, "vibe: restart of %s failed: %v\n", route.Name, err)
			}
		}()
	}
	s.serveStartingPage(w, r, route)
}
