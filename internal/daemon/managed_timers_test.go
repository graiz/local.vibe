package daemon

import (
	"testing"
	"time"
)

// TestTTLTimerRemovesRoute verifies a TTL route is removed by its own timer at
// expiry — no sweep involved.
func TestTTLTimerRemovesRoute(t *testing.T) {
	s := testServer()
	s.table.SetHooks(s.onRouteAdded, s.onRouteRemoved)

	exp := time.Now().Add(120 * time.Millisecond)
	r := &Route{Name: "ttl", Type: RouteTTL, Port: 1, RegisteredAt: time.Now(), ExpiresAt: &exp}
	s.table.Add(r) // arms the TTL timer via the add hook

	if _, ok := s.table.Get("ttl"); !ok {
		t.Fatal("route not added")
	}
	deadline := time.After(2 * time.Second)
	for {
		if _, ok := s.table.Get("ttl"); !ok {
			return // removed by the timer
		}
		select {
		case <-deadline:
			t.Fatal("TTL route was not removed by its timer")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestIdleCheckStopsIdleRoute verifies idleCheck stops a managed route that has
// been idle past its timeout.
func TestIdleCheckStopsIdleRoute(t *testing.T) {
	s := testServer()
	s.table.SetHooks(s.onRouteAdded, s.onRouteRemoved)

	r := &Route{Name: "idle", Type: RouteManaged, Port: 1, IdleTimeout: 1, RegisteredAt: time.Now().Add(-10 * time.Minute)}
	r.Running.Store(true)
	r.SetLastActivity(time.Now().Add(-5 * time.Minute)) // idle 5m > 1m timeout
	s.table.Add(r)

	s.idleCheck("idle")

	if r.Running.Load() {
		t.Error("idle route should have been stopped")
	}
}

// TestIdleCheckKeepsActiveRoute verifies idleCheck leaves a recently-active
// route running.
func TestIdleCheckKeepsActiveRoute(t *testing.T) {
	s := testServer()
	s.table.SetHooks(s.onRouteAdded, s.onRouteRemoved)

	r := &Route{Name: "busy", Type: RouteManaged, Port: 1, IdleTimeout: 5, RegisteredAt: time.Now()}
	r.Running.Store(true)
	r.SetLastActivity(time.Now())
	s.table.Add(r)

	s.idleCheck("busy")

	if !r.Running.Load() {
		t.Error("recently-active route should not have been stopped")
	}
}

// TestIdleCheckNeverVisitedFallsBackToRegisteredAt covers a managed route that
// started but never received a request: with no LastActivity, idleness is
// measured from RegisteredAt so it still auto-stops instead of running forever.
func TestIdleCheckNeverVisitedFallsBackToRegisteredAt(t *testing.T) {
	s := testServer()
	s.table.SetHooks(s.onRouteAdded, s.onRouteRemoved)

	r := &Route{Name: "never", Type: RouteManaged, Port: 1, IdleTimeout: 1, RegisteredAt: time.Now().Add(-5 * time.Minute)}
	r.Running.Store(true)
	// deliberately no SetLastActivity
	s.table.Add(r)

	s.idleCheck("never")

	if r.Running.Load() {
		t.Error("never-visited route past its idle timeout should have been stopped")
	}
}

// TestReregisterCancelsStaleTTLTimer verifies that re-registering a TTL route
// under the same name as a non-TTL route cancels the old TTL timer, so it can't
// later fire and silently delete the healthy replacement route. Regression: Add
// used to overwrite the map entry without firing the remove hook, leaving the
// orphaned timer live.
func TestReregisterCancelsStaleTTLTimer(t *testing.T) {
	s := testServer()
	s.table.SetHooks(s.onRouteAdded, s.onRouteRemoved)

	exp := time.Now().Add(80 * time.Millisecond)
	ttl := &Route{Name: "api", Type: RouteTTL, Port: 1, RegisteredAt: time.Now(), ExpiresAt: &exp}
	s.table.Add(ttl)

	// Re-register the same name as a sticky route before the TTL expires.
	sticky := &Route{Name: "api", Type: RouteSticky, Port: 2, RegisteredAt: time.Now()}
	s.table.Add(sticky)

	// The old TTL timer must not be armed anymore.
	s.timersMu.Lock()
	_, armed := s.ttlTimers["api"]
	s.timersMu.Unlock()
	if armed {
		t.Error("stale TTL timer still armed after re-registration as sticky")
	}

	// Wait past the original expiry: the sticky route must survive.
	time.Sleep(200 * time.Millisecond)
	if got, ok := s.table.Get("api"); !ok {
		t.Fatal("sticky route was deleted by the stale TTL timer")
	} else if got.Type != RouteSticky {
		t.Errorf("route type = %v, want sticky", got.Type)
	}
}

// TestRemoveCancelsTimers verifies removing a route cancels its timers (no
// leaked firing against a gone route).
func TestRemoveCancelsTimers(t *testing.T) {
	s := testServer()
	s.table.SetHooks(s.onRouteAdded, s.onRouteRemoved)

	exp := time.Now().Add(time.Hour)
	r := &Route{Name: "gone", Type: RouteTTL, Port: 1, RegisteredAt: time.Now(), ExpiresAt: &exp}
	s.table.Add(r)
	s.timersMu.Lock()
	_, armed := s.ttlTimers["gone"]
	s.timersMu.Unlock()
	if !armed {
		t.Fatal("TTL timer not armed on add")
	}

	s.table.Remove("gone")
	s.timersMu.Lock()
	_, stillThere := s.ttlTimers["gone"]
	s.timersMu.Unlock()
	if stillThere {
		t.Error("TTL timer not cancelled on remove")
	}
}
