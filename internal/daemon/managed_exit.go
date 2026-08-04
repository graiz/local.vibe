package daemon

// handleManagedExit is fired (event-based, via ProcessManager.SetExitHandler)
// the instant a managed child exits — from cmd.Wait for spawned children and
// from the per-OS PID-exit watcher for adopted ones. It flips the route to
// not-running immediately, the push-based replacement for the monitor sweep's
// dead-PID polling.
//
// pid identifies the exited process: if the route has since been restarted under
// a different PID, this is a stale notification from the old process and is
// ignored. A failure is seeded (when none is recorded) so the start page can
// surface a restart, matching the old sweep behavior.
func (s *Server) handleManagedExit(name string, pid int) {
	route, ok := s.table.Get(name)
	if !ok || route.Type != RouteManaged {
		return
	}
	if cur, has := route.PIDValue(); has && pid != 0 && cur != pid {
		return // a newer process owns this route now
	}
	// Swap, not Store: only an *unexpected* death (route still marked running)
	// seeds a failure. Intentional stops (handleStop, idle auto-stop, deregister)
	// set Running=false before killing the process, so this stays quiet for them
	// — matching the pre-event sweep, which only flagged a death when the route
	// still believed it was running.
	wasRunning := route.Running.Swap(false)
	route.Ready.Store(false)
	route.ClearPID()
	if wasRunning && route.LoadFailure() == nil {
		route.SetFailure(failureFromLog(route.Name, "process exited after becoming ready", route.Cmd, route.Dir))
	}
}

// watchTrackedPID removes a PID-tracked route when its (external) process exits,
// via the same per-OS PID-exit watcher used for adopted children — the
// event-based replacement for the sweep's dead-PID scan. Removes immediately if
// the PID is already gone; otherwise watches and removes on exit, guarding
// against a stale removal if the route was re-registered with a different PID.
func (s *Server) watchTrackedPID(name string, pid int) {
	if !processAlive(pid) {
		s.table.Remove(name)
		return
	}
	go watchPIDExit(pid, func() {
		if r, ok := s.table.Get(name); ok {
			if cur, has := r.PIDValue(); has && cur == pid {
				s.table.Remove(name)
			}
		}
	})
}
