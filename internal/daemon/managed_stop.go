package daemon

// stopManaged is the one way a managed route's child is stopped on purpose:
// handleStop, handleDeregister, the idle timer, worktree pruning, and the
// lost-access restart all go through it. The order is load-bearing —
// Running=false *before* the kill, so handleManagedExit reads the exit as
// intentional and seeds no failure — and so is the tail: the PID is cleared
// so nothing later mistakes the dead (or soon-dead) child for a live one.
// Five hand-copied variants of this sequence had drifted apart (two skipped
// the killPort fallback, two never cleared the PID).
func (s *Server) stopManaged(route *Route) {
	route.Running.Store(false)
	route.Ready.Store(false)
	// Stop handles both spawned and re-adopted children. Fall back to killing
	// whatever holds the port so a child the daemon couldn't track cleanly
	// still releases it.
	if err := s.procs.Stop(route.Name); err != nil && route.Port > 0 {
		s.killPort(route.Port)
	}
	route.ClearPID()
}
