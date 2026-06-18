package daemon

import "time"

// Per-route lifecycle timers — the event-based replacement for the monitor
// sweep's TTL and idle scanning. Armed/canceled from the RouteTable add/remove
// hooks (see SetHooks), so every register/start/stop/deregister path is covered
// from one place.

// onRouteAdded arms a route's lifecycle watcher when it enters the table.
func (s *Server) onRouteAdded(r *Route) {
	switch {
	case r.Type == RouteTTL && r.ExpiresAt != nil:
		s.armTTL(r.Name, *r.ExpiresAt)
	case r.Type == RouteManaged && r.IdleTimeout > 0:
		s.armIdle(r.Name)
	case r.Type == RoutePIDTracked:
		if pid, ok := r.PIDValue(); ok {
			s.watchTrackedPID(r.Name, pid)
		}
	}
}

// onRouteRemoved cancels any timers for a route leaving the table.
func (s *Server) onRouteRemoved(name string) {
	s.timersMu.Lock()
	if t, ok := s.ttlTimers[name]; ok {
		t.Stop()
		delete(s.ttlTimers, name)
	}
	if t, ok := s.idleTimers[name]; ok {
		t.Stop()
		delete(s.idleTimers, name)
	}
	s.timersMu.Unlock()
}

// armTTL schedules removal of a TTL route exactly at its expiry — one timer that
// fires once at the deadline, instead of a sweep polling every route's clock.
func (s *Server) armTTL(name string, expires time.Time) {
	d := time.Until(expires)
	if d < 0 {
		d = 0
	}
	s.timersMu.Lock()
	if old, ok := s.ttlTimers[name]; ok {
		old.Stop()
	}
	s.ttlTimers[name] = time.AfterFunc(d, func() {
		// Remove() fires onRouteRemoved, which cleans up this timer entry.
		s.table.Remove(name)
	})
	s.timersMu.Unlock()
}

// armIdle starts a self-rescheduling idle timer for a managed route. On each
// fire it reads the route's last activity — recorded cheaply via TouchActivity
// on the request path, so the hot path is untouched and there's no global poll —
// and stops the process once it's been idle past the timeout. The timer re-arms
// itself for the route's lifetime so it keeps governing across stop/restart
// cycles. Only routes that opt into idle_timeout get a timer at all.
func (s *Server) armIdle(name string) {
	route, ok := s.table.Get(name)
	if !ok || route.Type != RouteManaged || route.IdleTimeout <= 0 {
		return
	}
	timeout := time.Duration(route.IdleTimeout) * time.Minute
	s.timersMu.Lock()
	if old, ok := s.idleTimers[name]; ok {
		old.Stop()
	}
	s.idleTimers[name] = time.AfterFunc(timeout, func() { s.idleCheck(name) })
	s.timersMu.Unlock()
}

func (s *Server) idleCheck(name string) {
	route, ok := s.table.Get(name)
	if !ok || route.Type != RouteManaged || route.IdleTimeout <= 0 {
		return // route gone or idle disabled — let the timer lapse
	}
	timeout := time.Duration(route.IdleTimeout) * time.Minute

	if route.Running.Load() {
		idleFor := time.Since(route.LastActivityOr(route.RegisteredAt))
		if idleFor >= timeout {
			// Mark not-running before killing so the exit handler treats this as
			// an intentional stop (no failure seeded).
			route.Running.Store(false)
			_ = s.procs.Stop(name)
			route.Ready.Store(false)
			route.ClearPID()
			s.rearmIdle(name, timeout) // keep governing a future restart
			return
		}
		s.rearmIdle(name, timeout-idleFor)
		return
	}
	// Not running: re-check after a full interval (cheap; only opt-in routes).
	s.rearmIdle(name, timeout)
}

// rearmIdle reschedules the idle timer, but only if one still exists for the
// route (a concurrent removal clears the entry, ending the cycle).
func (s *Server) rearmIdle(name string, d time.Duration) {
	if d < time.Second {
		d = time.Second
	}
	s.timersMu.Lock()
	if _, ok := s.idleTimers[name]; ok {
		s.idleTimers[name] = time.AfterFunc(d, func() { s.idleCheck(name) })
	}
	s.timersMu.Unlock()
}
