package daemon

import (
	"fmt"
	"net/http"
	"os"
)

// recoverManagedRoute handles a request to a managed route that the daemon
// believes is stopped. It implements on-demand recovery so a stopped route
// comes back up when visited, instead of forcing a manual `vibe start`:
//
//  1. Adopt — a child from a prior daemon may still be running (managed
//     children live in their own process group and outlive a `vibe daemon
//     restart`). If the persisted PID is alive and its process group is still
//     listening, re-adopt it and proxy through. Adoption never spawns anything
//     and is therefore always allowed, even when auto-start is disabled.
//  2. Foreign listener — if some unrelated process holds the route's port,
//     show the start page: never proxy to a stranger and never spawn on top
//     of an occupied port.
//  3. Spawn — otherwise (the port is free), if auto-start is enabled and the
//     route has a launch command (and isn't in a crash loop), spawn it and
//     show the reconnecting page while it boots.
//  4. Start page — when there's nothing to adopt or spawn (no command,
//     auto-start disabled, or a recent failure), fall back to the manual
//     Start page with its recovery affordances.
//
// It returns served=true when it has already written a response and the
// caller should return. It returns served=false only after a successful
// adoption, signalling the caller to continue to the normal readiness check
// and proxy path with the route now marked Running+Ready.
func (s *Server) recoverManagedRoute(w http.ResponseWriter, r *http.Request, route *Route) (served bool) {
	// A worktree that no longer exists is dead — deregister it and send the
	// visitor to the parent app in the same response.
	if s.pruneGoneWorktree(w, r, route) {
		return true
	}

	// A concurrent request may already be spawning this route — and may have
	// finished, having just set Running=true and a fresh PID. Blowing that away
	// below would restart the whole recovery dance against a healthy child:
	// re-entering beginAutoStart, spawning a second time, and (once the first
	// child binds) letting preflightPort → killPort SIGTERM the server we just
	// started. Let the in-flight starter own the route and show the polling
	// page instead.
	if s.isAutoStarting(route.Name) {
		s.serveStartingPage(w, r, route)
		return true
	}

	// A child the daemon still tracks and that is still alive is not dead —
	// its port flapped (an HMR full reload closes the socket for a moment).
	// Resetting Running here would send a healthy app through adoption and
	// spawn, and with any stale Failure on the route (a slow first compile
	// that outlasted the readiness window) leave it on the start page for
	// good while the child served fine. Keep it running and let the
	// reconnecting page retry.
	if pid, ok := route.PIDValue(); ok && route.Running.Load() && processAlive(pid) && s.procs.IsRunning(route.Name) {
		route.Ready.Store(false)
		s.serveRepairPage(w, r, route)
		return true
	}

	// Normalize stale state: a dead PID shouldn't linger on the route.
	if pid, ok := route.PIDValue(); ok && !processAlive(pid) {
		route.ClearPID()
	}
	route.Running.Store(false)
	route.Ready.Store(false)

	// (1) Adopt a surviving orphan, if any. adoptOrphan only succeeds when the
	// route's registered port is still served by a member of its process
	// group, so the registration needs no rewrite.
	if pid, _, ok := s.adoptOrphan(route); ok {
		s.procs.Adopt(route.Name, pid)
		route.SetPID(pid)
		route.Running.Store(true)
		route.Ready.Store(true)
		route.SetFailure(nil)
		route.TouchActivity()
		fmt.Fprintf(os.Stderr, "vibe: re-adopted managed route %s (pid %d, port %d) after daemon restart\n", route.Name, pid, route.Port)
		return false // proceed to proxy
	}

	// (2) A foreign process holds the route's port (adoption above already
	// ruled out our own child). Never proxy to a stranger on a reused port,
	// and never spawn on top of it — show the start page so the user decides
	// explicitly (the manual start path has the kill-and-retry recovery UX).
	if route.Port != 0 && s.isPortReady(route.Port) {
		s.serveStartPage(w, r, route)
		return true
	}

	// (3) On-demand spawn — the port is free. Only when auto-start is enabled,
	// the route has a command, and it isn't crash-looping. A recent failure
	// routes the user to the start page (with its "Kill PID X and retry" UX)
	// instead of silently respawning on every asset request. Existing
	// worktrees — registered routes or ones discovered on disk — also
	// suppress the spawn: the visitor gets the picker (start page with the
	// worktree list) and chooses — starting main is one click.
	if s.cfg.Daemon.AutoStartEnabled() && route.Cmd != "" && route.LoadFailure() == nil && !s.hasWorktrees(route) {
		if s.beginAutoStart(route.Name) {
			// Deferred release: net/http recovers handler panics, so a panic
			// anywhere in startManagedNow would otherwise leave the flag set
			// for the daemon's lifetime — beginAutoStart never succeeds again
			// (no respawn) and isAutoStarting keeps /repair from ever offering
			// "restartable", wedging the route on a polling page forever.
			err := func() error {
				defer s.endAutoStart(route.Name)
				return s.startManagedNow(route)
			}()
			if err != nil {
				// Failure already recorded on the route; show the start page so
				// the user sees the tailed log + recovery hint.
				s.serveStartPage(w, r, route)
				return true
			}
		}
		// Either we just kicked it off or another request is already starting
		// it — show the "Starting" page, which polls until the port answers and
		// then reloads into a working proxy. This is a fresh cold start, not a
		// port-recovery, so it uses start-flavored copy rather than the
		// "Reconnecting / looking in logs" repair wording.
		s.serveStartingPage(w, r, route)
		return true
	}

	// (4) Nothing to recover automatically (auto-start disabled, no command,
	// or a recent crash) — fall back to the manual start page.
	s.serveStartPage(w, r, route)
	return true
}

// pruneGoneWorktree deregisters a worktree route whose checkout was removed
// (see worktreeDirGone), kills any still-running child, and answers with a
// 307 to the parent app. Returns true when it wrote the response. Called from
// both the recovery path and the healthy proxy path — a removed worktree's
// child process happily outlives the checkout and would otherwise keep
// serving stale content forever.
func (s *Server) pruneGoneWorktree(w http.ResponseWriter, r *http.Request, route *Route) bool {
	if !worktreeDirGone(route) {
		return false
	}
	s.stopManaged(route)
	s.table.Remove(route.Name)
	if err := s.saveStickyRoutes(); err != nil {
		fmt.Fprintf(os.Stderr, "vibe: failed to persist worktree prune of %s: %v\n", route.Name, err)
	}
	fmt.Fprintf(os.Stderr, "vibe: pruned worktree route %s (checkout %s is gone)\n", route.Name, route.Dir)
	s.redirectToParent(w, r, route.Parent)
	return true
}

// startFailure is why startManaged could not bring a route up, typed so the
// HTTP handlers can answer with the same shape they always have (a 409 with a
// recovery hint for a held port, a 500 with the log tail for a crashed spawn,
// a plain error otherwise) while every spawn path shares one engine. The same
// Failure is recorded on the route so the start page can render it.
type startFailure struct {
	status   int
	message  string
	port     int       // the held port, for conflicts
	recovery *Recovery // kill-and-retry hint, for conflicts
	err      error     // the spawn error (a *StartError when the child died)
}

func (f *startFailure) Error() string {
	if f.err != nil {
		return f.err.Error()
	}
	return f.message
}

func (f *startFailure) Unwrap() error { return f.err }

// write answers an API request with the failure in its established shape.
func (f *startFailure) write(w http.ResponseWriter, route *Route) {
	switch {
	case f.err != nil:
		writeStartFailure(w, f.status, f.err, route.Cmd, route.Dir)
	case f.port > 0:
		writePortConflict(w, f.port, f.recovery)
	default:
		writeJSONError(w, f.status, f.message)
	}
}

// startManagedNow spawns a managed route's process after re-reading its
// vibe.json, so config edits take effect on every kind of start: CLI, dashboard,
// on-demand recovery, and the lost-access restart all come through here.
func (s *Server) startManagedNow(route *Route) error {
	return s.startManaged(route, true)
}

// startManaged is the one spawn engine. In order: optionally sync vibe.json,
// assign a port if none, refuse vibe-internal port collisions, clear or report
// strangers on every port the command binds, spawn through the ProcessManager
// (which watches the first second for an immediate crash), then record the
// running state, persist the PID for re-adoption after a daemon restart, and
// start the readiness poll. Every failure is recorded on the route as its
// Failure and returned as a *startFailure. Three hand-copied variants of this
// sequence (register, start, recovery) had drifted; one of them is how a
// register whose port another route held reached killPort against that route.
func (s *Server) startManaged(route *Route, syncConfig bool) error {
	fail := func(f *startFailure, failure *Failure) error {
		route.SetFailure(failure)
		return f
	}
	if syncConfig {
		if err := s.syncRouteFromVibeJSON(route); err != nil {
			return fail(&startFailure{status: http.StatusConflict, message: err.Error()}, &Failure{Message: err.Error()})
		}
	}
	if route.Cmd == "" {
		msg := "route has no launch command"
		return fail(&startFailure{status: http.StatusBadRequest, message: msg}, &Failure{Message: msg})
	}
	if route.Port == 0 {
		port, err := findFreePort(s.table)
		if err != nil {
			msg := "could not find a free port"
			return fail(&startFailure{status: http.StatusInternalServerError, message: msg}, &Failure{Message: msg})
		}
		route.Port = port
	}

	// Fail fast with a clear message on a vibe-internal port collision (daemon
	// listeners, or another route's primary/oauth/reserve port): a
	// misconfiguration no amount of killing or retrying can fix, distinct from
	// an external process squatting the port.
	if msg := s.checkVibePortCollisions(route); msg != "" {
		return fail(&startFailure{status: http.StatusConflict, message: msg}, &Failure{Message: msg})
	}

	// Pre-flight every port the cmd will bind: each reserve_port first, so a
	// multi-port app surfaces the right collision, then the primary. A port
	// that can't be cleared is held by an unrelated process — record the
	// conflict so the start page offers kill-and-retry instead of the child
	// crash-looping on EADDRINUSE.
	for _, kv := range reservePortValuesSorted(route.ReservePorts) {
		if rec := s.preflightPort(kv.Port); rec != nil {
			msg := fmt.Sprintf("reserve_ports[%q] = %d is already in use by another process", kv.Name, kv.Port)
			return fail(&startFailure{status: http.StatusConflict, message: msg, port: kv.Port, recovery: rec},
				&Failure{Message: msg, Recovery: rec})
		}
	}
	if rec := s.preflightPort(route.Port); rec != nil {
		msg := fmt.Sprintf("port %d is already in use by another process", route.Port)
		return fail(&startFailure{status: http.StatusConflict, message: msg, port: route.Port, recovery: rec},
			&Failure{Message: msg, Recovery: rec})
	}

	s.prepareWorktreeEnv(route)
	pid, err := s.procs.Start(route)
	if err != nil {
		route.Running.Store(false)
		route.Ready.Store(false)
		return fail(&startFailure{status: http.StatusInternalServerError, err: err}, failureFromError(err, route.Cmd, route.Dir))
	}
	route.SetPID(pid)
	route.Running.Store(true)
	route.Ready.Store(false)
	route.SetFailure(nil)
	// Seed LastActivity so the idle timer has a baseline even if the process
	// never receives a proxy request.
	route.TouchActivity()
	if err := s.saveStickyRoutes(); err != nil {
		fmt.Fprintf(os.Stderr, "vibe: failed to persist pid for %s: %v\n", route.Name, err)
	}
	go s.waitForReady(route, pid)
	return nil
}

// beginAutoStart marks an on-demand start as in flight for the named route.
// It returns false when one is already running, so only a single request
// spawns the process while siblings fall through to the reconnecting page.
func (s *Server) beginAutoStart(name string) bool {
	_, loaded := s.autoStarting.LoadOrStore(name, true)
	return !loaded
}

// endAutoStart clears the in-flight marker for the named route.
func (s *Server) endAutoStart(name string) { s.autoStarting.Delete(name) }

// isAutoStarting reports whether an on-demand start is in flight for the route.
// handleRepair consults this so the reconnecting page keeps polling during the
// brief window before the spawned process has bound its port, rather than
// declaring the route dead.
func (s *Server) isAutoStarting(name string) bool {
	_, ok := s.autoStarting.Load(name)
	return ok
}

// adoptManagedOrphansOnStartup re-adopts managed children that survived a
// daemon restart, so `vibe list` reflects reality immediately instead of
// showing every managed route as stopped until its first request. It never
// spawns a process — it only re-attaches to children that are already running
// and listening (a no-op on platforms where children die with the daemon).
func (s *Server) adoptManagedOrphansOnStartup() {
	for _, route := range s.table.List() {
		if route.Type != RouteManaged {
			continue
		}
		if pid, _, ok := s.adoptOrphan(route); ok {
			s.procs.Adopt(route.Name, pid)
			route.SetPID(pid)
			route.Running.Store(true)
			route.Ready.Store(true)
			route.SetFailure(nil)
			fmt.Fprintf(os.Stderr, "vibe: re-adopted managed route %s (pid %d, port %d) on startup\n", route.Name, pid, route.Port)
		}
	}
}
