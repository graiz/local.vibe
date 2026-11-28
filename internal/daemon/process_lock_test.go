//go:build !windows

package daemon

import (
	"sync"
	"testing"
	"time"
)

// TestStartDoesNotHoldLockDuringSpawn pins issue #13: Start used to hold pm.mu
// across the whole spawn — log open, env hook, cmd.Start() and, on Windows,
// the Job Object syscalls in afterSpawn. Anything slow there blocked every
// other ProcessManager caller for every route. The env hook runs in that same
// window, so a hook that blocks stands in for a slow spawn syscall.
func TestStartDoesNotHoldLockDuringSpawn(t *testing.T) {
	pm := NewProcessManager()
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	pm.SetEnvHook(func(r *Route) []string {
		if r.Name == "slow" {
			entered <- struct{}{}
			<-release
		}
		return nil
	})

	startErr := make(chan error, 1)
	go func() {
		_, err := pm.Start(&Route{Name: "slow", Cmd: "sleep 60", Dir: t.TempDir()})
		startErr <- err
	}()
	defer func() { _ = pm.Stop("slow") }()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Start never reached the env hook")
	}

	// Other routes must stay reachable while "slow" is mid-spawn.
	done := make(chan struct{})
	go func() {
		_ = pm.IsRunning("other")
		_ = pm.Stop("other")
		_ = pm.OwnsPID(1)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("ProcessManager calls for another route blocked behind an in-flight spawn")
	}

	close(release)
	if err := <-startErr; err != nil {
		t.Fatalf("Start: %v", err)
	}
}

// TestConcurrentStartSameRouteSpawnsOnce: with the lock released during the
// spawn, a second Start for the same route must wait for the first and return
// its pid rather than spawning a duplicate (which would then crash-loop on
// EADDRINUSE and strand the first child). Stop and IsRunning must likewise see
// the finished spawn, not a gap.
func TestConcurrentStartSameRouteSpawnsOnce(t *testing.T) {
	pm := NewProcessManager()
	release := make(chan struct{})
	var hookCalls int
	var mu sync.Mutex
	pm.SetEnvHook(func(r *Route) []string {
		mu.Lock()
		hookCalls++
		first := hookCalls == 1
		mu.Unlock()
		if first {
			<-release
		}
		return nil
	})

	route := &Route{Name: "dup", Cmd: "sleep 60", Dir: t.TempDir()}
	pids := make(chan int, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			pid, err := pm.Start(route)
			pids <- pid
			errs <- err
		}()
	}
	// Give the second caller time to arrive while the first is mid-spawn.
	time.Sleep(200 * time.Millisecond)
	running := make(chan bool, 1)
	go func() { running <- pm.IsRunning("dup") }()
	time.Sleep(50 * time.Millisecond)
	close(release)

	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("Start: %v", err)
		}
	}
	a, b := <-pids, <-pids
	if a != b || a == 0 {
		t.Errorf("concurrent Starts returned pids %d and %d; want the same nonzero pid", a, b)
	}
	mu.Lock()
	if hookCalls != 1 {
		t.Errorf("spawned %d times; want 1", hookCalls)
	}
	mu.Unlock()
	if !<-running {
		t.Error("IsRunning during the spawn returned false; want it to wait for the spawn and see it running")
	}
	if err := pm.Stop("dup"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// A Stop that arrives while the same route is mid-spawn must wait for the
// spawn and then kill the new child — not report "not running" and leave a
// freshly started process alive and untracked by the caller's intent.
func TestStopDuringSpawnKillsNewChild(t *testing.T) {
	pm := NewProcessManager()
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	pm.SetEnvHook(func(r *Route) []string {
		entered <- struct{}{}
		<-release
		return nil
	})

	type res struct {
		pid int
		err error
	}
	started := make(chan res, 1)
	go func() {
		pid, err := pm.Start(&Route{Name: "mid", Cmd: "sleep 60", Dir: t.TempDir()})
		started <- res{pid, err}
	}()
	<-entered

	stopErr := make(chan error, 1)
	go func() { stopErr <- pm.Stop("mid") }()
	time.Sleep(100 * time.Millisecond) // let Stop arrive while the spawn is held
	close(release)

	// Start may succeed or report an early exit — Stop killing the child
	// inside Start's 1s startup window is a legitimate outcome of the race
	// (and the same one the old fully-locked Start produced). What matters is
	// that Stop saw the child and it is gone.
	sr := <-started
	if sr.err == nil && sr.pid == 0 {
		t.Fatal("Start returned pid 0 without an error")
	}
	select {
	case err := <-stopErr:
		if err != nil {
			t.Fatalf("Stop during spawn = %v; want nil (it should wait and kill the new child)", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Stop never returned")
	}
	if pm.IsRunning("mid") {
		t.Error("route still running after a Stop issued mid-spawn")
	}
	if sr.pid != 0 && processAlive(sr.pid) {
		t.Errorf("new child pid %d survived a Stop issued mid-spawn", sr.pid)
	}
}
