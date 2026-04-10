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
			if r.PID != nil && !processAlive(*r.PID) {
				toRemove = append(toRemove, r.Name)
			}
		case RouteTTL:
			if r.ExpiresAt != nil && now.After(*r.ExpiresAt) {
				toRemove = append(toRemove, r.Name)
			}
		case RouteManaged:
			// Mark managed routes as unhealthy if their process has died.
			if r.PID != nil && !processAlive(*r.PID) {
				r.Healthy = false
				r.PID = nil
			}
			// Auto-stop idle managed routes.
			if r.IdleTimeout > 0 && r.PID != nil && !r.LastActivity.IsZero() {
				if now.Sub(r.LastActivity) > time.Duration(r.IdleTimeout)*time.Minute {
					_ = s.procs.Stop(r.Name)
					r.Healthy = false
					r.PID = nil
				}
			}
		}
	}
	for _, name := range toRemove {
		s.table.Remove(name)
	}
}
