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

// managedPortHealthy is a no-op on Windows (always healthy). The unix monitor
// uses this to catch a managed route whose port got recycled to a squatter
// after its child died and its PID was reused — a scenario that hinges on
// children outliving the daemon, which Job Objects prevent here. Without a
// cheap process-group ownership probe on Windows, a real check would risk
// false positives, so the monitor relies on processAlive + the on-demand
// recoverManagedRoute spawn path instead.
func (s *Server) managedPortHealthy(route *Route) bool {
	return true
}
