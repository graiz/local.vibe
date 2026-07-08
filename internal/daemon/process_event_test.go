//go:build !windows

// Event-based death detection tests. Spawn real processes, so gated to non-Windows
// like the other process-group tests.

package daemon

import (
	"os/exec"
	"testing"
	"time"
)

// TestPIDTrackedRouteRemovedOnExit verifies a PID-tracked route is removed by
// the event-based PID-exit watcher when its external process dies (the
// replacement for the sweep's dead-PID scan).
func TestPIDTrackedRouteRemovedOnExit(t *testing.T) {
	s := testServer()
	s.table.SetHooks(s.onRouteAdded, s.onRouteRemoved)

	cmd := exec.Command("sleep", "10")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	r := &Route{Name: "tracked", Type: RoutePIDTracked, Port: 1, RegisteredAt: time.Now()}
	r.SetPID(cmd.Process.Pid)
	s.table.Add(r) // arms the PID-exit watcher via the add hook
	time.Sleep(150 * time.Millisecond)

	_ = cmd.Process.Kill()

	deadline := time.After(3 * time.Second)
	for {
		if _, ok := s.table.Get("tracked"); !ok {
			break // removed
		}
		select {
		case <-deadline:
			t.Fatal("PID-tracked route was not removed after its process died")
		case <-time.After(20 * time.Millisecond):
		}
	}
	_ = cmd.Wait()
}

// TestPIDTrackedAlreadyDeadRemovedImmediately verifies a PID-tracked route whose
// PID is already gone at registration is removed right away.
func TestPIDTrackedAlreadyDeadRemovedImmediately(t *testing.T) {
	s := testServer()
	s.table.SetHooks(s.onRouteAdded, s.onRouteRemoved)

	r := &Route{Name: "deadtrack", Type: RoutePIDTracked, Port: 1, RegisteredAt: time.Now()}
	r.SetPID(2147483646) // not a live pid
	s.table.Add(r)

	if _, ok := s.table.Get("deadtrack"); ok {
		t.Error("PID-tracked route with an already-dead PID should be removed on add")
	}
}

// TestProcessManagerFiresExitOnDeath verifies a spawned managed child's natural
// exit (after the startup window) fires the exit handler — the event-based
// replacement for sweep polling.
func TestProcessManagerFiresExitOnDeath(t *testing.T) {
	pm := NewProcessManager()
	exited := make(chan int, 1)
	pm.SetExitHandler(func(name string, pid int) { exited <- pid })

	route := &Route{Name: "exitcb", Type: RouteManaged, Cmd: "sleep 2", Dir: t.TempDir(), RegisteredAt: time.Now()}
	pid, err := pm.Start(route) // blocks ~1s for the startup window, then returns
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	select {
	case got := <-exited:
		if got != pid {
			t.Errorf("exit handler pid = %d, want %d", got, pid)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("exit handler never fired after the process died")
	}
}

// TestStartNoOpWhenAdoptedChildAlive verifies ProcessManager.Start is a no-op
// (returns the adopted PID, spawns nothing) when the route has a live adopted
// child. Regression: Start's already-running guard only checked pm.procs, so it
// would spawn a second copy and then delete(pm.adopted) — orphaning the original
// child and crash-looping the new one on EADDRINUSE.
func TestStartNoOpWhenAdoptedChildAlive(t *testing.T) {
	pm := NewProcessManager()

	cmd := exec.Command("sleep", "10")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	adoptedPID := cmd.Process.Pid
	pm.Adopt("app", adoptedPID)

	// A start command that would bind nothing but must NOT run — if Start spawns
	// it, the adopted PID would be dropped from tracking.
	route := &Route{Name: "app", Type: RouteManaged, Cmd: "sleep 30", Dir: t.TempDir(), RegisteredAt: time.Now()}
	pid, err := pm.Start(route)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if pid != adoptedPID {
		t.Errorf("Start returned pid %d, want the live adopted pid %d (should be a no-op)", pid, adoptedPID)
	}
	// The adopted child must still be tracked so Stop/Deregister can kill it.
	if !pm.OwnsPID(adoptedPID) {
		t.Error("adopted child was dropped from tracking by a duplicate Start")
	}
}

// TestWatchPIDExitFires validates the per-OS PID-exit watcher (kqueue on darwin,
// pidfd on linux) used for adopted children: it must fire fn when the watched
// process dies, with no polling.
func TestWatchPIDExitFires(t *testing.T) {
	cmd := exec.Command("sleep", "10")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	pid := cmd.Process.Pid

	fired := make(chan struct{})
	go watchPIDExit(pid, func() { close(fired) })
	time.Sleep(150 * time.Millisecond) // let the watcher register

	_ = cmd.Process.Kill()

	select {
	case <-fired:
	case <-time.After(3 * time.Second):
		t.Fatal("watchPIDExit did not fire after the process was killed")
	}
	_ = cmd.Wait()
}
