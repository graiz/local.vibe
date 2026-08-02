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
	route.Running.Store(false) // intentional stop: no failure seeded by the exit handler
	_ = s.procs.Stop(route.Name)
	s.table.Remove(route.Name)
	if err := s.saveStickyRoutes(); err != nil {
		fmt.Fprintf(os.Stderr, "vibe: failed to persist worktree prune of %s: %v\n", route.Name, err)
	}
	fmt.Fprintf(os.Stderr, "vibe: pruned worktree route %s (checkout %s is gone)\n", route.Name, route.Dir)
	s.redirectToParent(w, r, route.Parent)
	return true
}

// startManagedNow spawns a managed route's process synchronously (mirroring
// handleStart's success path) and kicks off the background readiness poll.
// It blocks briefly while ProcessManager.Start watches for an immediate crash,
// so the caller can distinguish "started, booting" from "failed to start"
// before choosing which page to render. The PID is persisted so a subsequent
// daemon restart can re-adopt this process.
func (s *Server) startManagedNow(route *Route) error {
	if route.Port == 0 {
		port, err := findFreePort(s.table)
		if err != nil {
			f := &Failure{Message: "could not find a free port"}
			route.SetFailure(f)
			return fmt.Errorf("%s", f.Message)
		}
		route.Port = port
	}

	// Fail fast with a clear message on a vibe-internal port collision (daemon
	// listeners, or another route's primary/oauth/reserve port) — mirrors
	// handleStart, so the start page shows the real cause instead of a generic
	// EADDRINUSE the user can't act on.
	if msg := s.checkVibePortCollisions(route); msg != "" {
		route.SetFailure(&Failure{Message: msg})
		return fmt.Errorf("%s", msg)
	}

	// Clear any stale listener still holding a port the command will bind
	// (e.g. a half-dead child from a prior run), mirroring handleStart. If the
	// port can't be freed it's held by an unrelated process — record the
	// conflict and bail so the start page surfaces a kill-and-retry hint
	// instead of the child crash-looping on EADDRINUSE.
	for _, kv := range reservePortValuesSorted(route.ReservePorts) {
		if rec := s.preflightPort(kv.Port); rec != nil {
			f := &Failure{Message: fmt.Sprintf("reserve_ports[%q] = %d is already in use by another process", kv.Name, kv.Port), Recovery: rec}
			route.SetFailure(f)
			return fmt.Errorf("%s", f.Message)
		}
	}
	if rec := s.preflightPort(route.Port); rec != nil {
		f := &Failure{Message: fmt.Sprintf("port %d is already in use by another process", route.Port), Recovery: rec}
		route.SetFailure(f)
		return fmt.Errorf("%s", f.Message)
	}

	s.prepareWorktreeEnv(route)
	pid, err := s.procs.Start(route)
	if err != nil {
		route.Running.Store(false)
		route.Ready.Store(false)
		route.SetFailure(failureFromError(err, route.Cmd))
		return err
	}
	route.SetPID(pid)
	route.Running.Store(true)
	route.Ready.Store(false)
	route.SetFailure(nil)
	route.TouchActivity()
	if err := s.saveStickyRoutes(); err != nil {
		fmt.Fprintf(os.Stderr, "vibe: failed to persist pid for %s: %v\n", route.Name, err)
	}
	go s.waitForReady(route)
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
