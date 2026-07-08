//go:build windows

package daemon

// adoptOrphan is a no-op on Windows. Managed children are wrapped in a Job
// Object created with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE, so when the daemon
// process exits (including the rename-aside dance during `vibe dev`) the job
// handle closes and every child in it is terminated. Nothing survives a
// daemon restart to adopt, so the on-demand spawn path in recoverManagedRoute
// is what brings a stopped managed route back up on Windows.
func (s *Server) adoptOrphan(route *Route) (pid int, port int, ok bool) {
	return 0, 0, false
}

// portForeignToRoute is conservative on Windows: it always reports "not
// foreign" so a healthy route is never misclassified and forced into recovery.
// Job-Object-based ownership detection (the analogue of the unix process-group
// check) is Phase 2 — see port_discover_windows.go.
func (s *Server) portForeignToRoute(route *Route) bool { return false }
