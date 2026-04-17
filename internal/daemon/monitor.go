package daemon

import (
	"os"
	"syscall"
	"time"
)

// processAlive checks if a process with the given PID is still running.
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

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
