package daemon

import (
	"time"
)

// processAlive lives in process_alive_{unix,windows}.go — the implementation
// differs by GOOS, but the signature is shared.

// monitorRoutes runs a periodic sweep that removes stale routes:
// PID-tracked routes whose process has exited, and TTL routes that have expired.
func (s *Server) monitorRoutes(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.sweepRoutes()
		case <-s.quit:
			return
		}
	}
}

func (s *Server) sweepRoutes() {
	now := time.Now()
	var toRemove []string
	for _, r := range s.table.List() {
		switch r.Type {
		case RoutePIDTracked:
			if pid, ok := r.PIDValue(); ok && !processAlive(pid) {
				toRemove = append(toRemove, r.Name)
			}
		case RouteTTL:
			if r.ExpiresAt != nil && now.After(*r.ExpiresAt) {
				toRemove = append(toRemove, r.Name)
			}
		case RouteManaged:
			// Mark managed routes as not running/ready if their process has died.
			pid, running := r.PIDValue()
			if running && !processAlive(pid) {
				r.Running.Store(false)
				r.Ready.Store(false)
				r.ClearPID()
				// Stash a diagnostic so the start page (and polling clients)
				// can surface "Kill PID X and retry" when the process bound
				// its port, then crashed moments later — a race where
				// waitForReady's success path already cleared any failure.
				if r.LoadFailure() == nil {
					r.SetFailure(failureFromLog(r.Name, "process exited after becoming ready", r.Cmd))
				}
				running = false
			}
			// Catch silent rot: a route that became Ready but whose registered
			// port is no longer served by its own process group. processAlive
			// above is fooled by PID reuse (a dead child's PID recycled to an
			// unrelated live process), and our readiness probe is fooled by a
			// squatter that grabs the freed port and answers TCP without
			// speaking HTTP — so the route looks healthy while proxying garbage.
			// Anchoring on port ownership (managedPortHealthy, unix-only; a
			// no-op on Windows) catches both. Only check once a route has
			// actually become Ready, to avoid racing the startup window where
			// the child is alive but not yet listening.
			if running && r.Ready.Load() && r.Port > 0 && !s.managedPortHealthy(r) {
				r.Running.Store(false)
				r.Ready.Store(false)
				r.ClearPID()
				// Seed a failure so the start page offers a one-click restart
				// (and, with autostart on, the next request re-spawns the route
				// on a clean port rather than proxying to the squatter).
				if r.LoadFailure() == nil {
					r.SetFailure(failureFromLog(r.Name, "registered port no longer served by this route — process gone or port taken over", r.Cmd))
				}
				running = false
			}
			// Auto-stop idle managed routes. If LastActivity is unset (e.g.
			// registered but never received traffic), fall back to RegisteredAt
			// so the timer still elapses instead of running forever.
			if r.IdleTimeout > 0 && running {
				lastActive := r.LastActivityOr(r.RegisteredAt)
				if now.Sub(lastActive) > time.Duration(r.IdleTimeout)*time.Minute {
					_ = s.procs.Stop(r.Name)
					r.Running.Store(false)
					r.Ready.Store(false)
					r.ClearPID()
				}
			}
		}
	}
	for _, name := range toRemove {
		s.table.Remove(name)
	}
}
