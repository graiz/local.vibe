package daemon

import (
	"testing"
	"time"

	"github.com/localvibe/vibe/internal/config"
)

func TestSweepMarksDeadManagedNotRunning(t *testing.T) {
	cfg := &config.Config{
		Daemon: config.DaemonConfig{TLD: "test"},
	}
	s := NewServer(cfg)

	// Add a managed route with a fake PID that doesn't exist
	fakePID := 999999
	r := &Route{
		Name: "dead",
		Port: 3000,
		Type: RouteManaged,
		PID:  &fakePID,
	}
	r.Running.Store(true)
	r.Ready.Store(true)
	s.table.Add(r)

	s.sweepRoutes()

	r, _ = s.table.Get("dead")
	if r.Running.Load() {
		t.Error("expected Running=false after sweep for dead PID")
	}
	if r.PID != nil {
		t.Error("expected PID=nil after sweep for dead PID")
	}
}

func TestSweepIdleTimeout(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Daemon: config.DaemonConfig{TLD: "test"},
	}
	s := NewServer(cfg)

	// Start a real process so we can test idle stop
	route := &Route{
		Name:        "idle-test",
		Port:        3000,
		Cmd:         "sleep 60",
		Dir:         t.TempDir(),
		Type:        RouteManaged,
		IdleTimeout: 1, // 1 minute
	}

	pid, err := s.procs.Start(route)
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	route.PID = &pid
	route.Running.Store(true)
	route.Ready.Store(true)
	// Set last activity to 2 minutes ago (past the 1-minute timeout)
	route.LastActivity = time.Now().Add(-2 * time.Minute)
	s.table.Add(route)

	s.sweepRoutes()

	r, _ := s.table.Get("idle-test")
	if r.Running.Load() {
		t.Error("expected Running=false after idle timeout sweep")
	}
	if r.PID != nil {
		t.Error("expected PID=nil after idle timeout sweep")
	}
}

func TestSweepRemovesPIDTracked(t *testing.T) {
	cfg := &config.Config{
		Daemon: config.DaemonConfig{TLD: "test"},
	}
	s := NewServer(cfg)

	fakePID := 999999
	s.table.Add(&Route{
		Name: "tracked",
		Port: 5000,
		Type: RoutePIDTracked,
		PID:  &fakePID,
	})

	s.sweepRoutes()

	if _, ok := s.table.Get("tracked"); ok {
		t.Error("PID-tracked route with dead PID should be removed")
	}
}

func TestSweepKeepsStoppedManaged(t *testing.T) {
	cfg := &config.Config{
		Daemon: config.DaemonConfig{TLD: "test"},
	}
	s := NewServer(cfg)

	// Managed route with no PID (stopped) should not be touched
	s.table.Add(&Route{
		Name: "stopped",
		Port: 3000,
		Type: RouteManaged,
	})

	s.sweepRoutes()

	if _, ok := s.table.Get("stopped"); !ok {
		t.Error("stopped managed route should not be removed by sweep")
	}
}
