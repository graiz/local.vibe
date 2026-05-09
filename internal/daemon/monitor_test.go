//go:build !windows

// Phase 1 of Windows support: these tests assume processAlive returns false
// for dead PIDs and use unix shell commands ("sleep 60"). The Windows stub
// in process_alive_windows.go returns true unconditionally, so the sweep
// tests can't distinguish dead routes. Phase 2 will rework these.

package daemon

import (
	"testing"
	"time"

	"github.com/graiz/local.vibe/internal/config"
)

func TestSweepMarksDeadManagedNotRunning(t *testing.T) {
	cfg := &config.Config{
		Daemon: config.DaemonConfig{TLD: "test"},
	}
	s := NewServer(cfg)

	// Add a managed route with a fake PID that doesn't exist
	r := &Route{
		Name: "dead",
		Port: 3000,
		Type: RouteManaged,
	}
	r.SetPID(999999)
	r.Running.Store(true)
	r.Ready.Store(true)
	s.table.Add(r)

	s.sweepRoutes()

	r, _ = s.table.Get("dead")
	if r.Running.Load() {
		t.Error("expected Running=false after sweep for dead PID")
	}
	if _, ok := r.PIDValue(); ok {
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
	route.SetPID(pid)
	route.Running.Store(true)
	route.Ready.Store(true)
	// Set last activity to 2 minutes ago (past the 1-minute timeout)
	route.SetLastActivity(time.Now().Add(-2 * time.Minute))
	s.table.Add(route)

	s.sweepRoutes()

	r, _ := s.table.Get("idle-test")
	if r.Running.Load() {
		t.Error("expected Running=false after idle timeout sweep")
	}
	if _, ok := r.PIDValue(); ok {
		t.Error("expected PID=nil after idle timeout sweep")
	}
}

func TestSweepRemovesPIDTracked(t *testing.T) {
	cfg := &config.Config{
		Daemon: config.DaemonConfig{TLD: "test"},
	}
	s := NewServer(cfg)

	r := &Route{
		Name: "tracked",
		Port: 5000,
		Type: RoutePIDTracked,
	}
	r.SetPID(999999)
	s.table.Add(r)

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

// TestSweepIdleTimeoutFallsBackToRegisteredAt covers the bug where a managed
// route started but never visited had LastActivity=zero, and the old sweep
// skipped the check entirely — so the route ran forever despite IdleTimeout.
// The new behavior falls back to RegisteredAt when LastActivity is unset.
func TestSweepIdleTimeoutFallsBackToRegisteredAt(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Daemon: config.DaemonConfig{TLD: "test"}}
	s := NewServer(cfg)

	route := &Route{
		Name:         "never-visited",
		Port:         3000,
		Cmd:          "sleep 60",
		Dir:          t.TempDir(),
		Type:         RouteManaged,
		IdleTimeout:  1, // 1 minute
		RegisteredAt: time.Now().Add(-2 * time.Minute),
	}

	pid, err := s.procs.Start(route)
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	route.SetPID(pid)
	route.Running.Store(true)
	route.Ready.Store(true)
	// Deliberately do NOT call SetLastActivity — this is the exact scenario
	// where the bug manifested.
	s.table.Add(route)

	s.sweepRoutes()

	r, _ := s.table.Get("never-visited")
	if r.Running.Load() {
		t.Error("expected Running=false: RegisteredAt is past the idle timeout")
	}
	if _, ok := r.PIDValue(); ok {
		t.Error("expected PID cleared after idle timeout sweep")
	}
}
