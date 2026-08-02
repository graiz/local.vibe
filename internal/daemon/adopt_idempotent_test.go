//go:build !windows

package daemon

import (
	"runtime"
	"testing"
	"time"
)

// TestAdoptIsIdempotent covers the watcher/fd leak. Recovery runs on every
// failed proxy round-trip, so a transient upstream error against a perfectly
// healthy child re-adopts the pid it already has. Each Adopt used to spawn
// another watchPIDExit goroutine holding another kqueue/pidfd that only closes
// when the process dies — unbounded growth against RLIMIT_NOFILE on a
// long-lived daemon with a flaky dev server.
func TestAdoptIsIdempotent(t *testing.T) {
	pm := NewProcessManager()

	// A long-lived process to watch. Its own pid works: it outlives the test,
	// so no watcher will fire and exit on its own and skew the count.
	pid := 1

	settle := func() {
		// Let any spawned goroutines reach their blocking syscall.
		for i := 0; i < 10; i++ {
			runtime.Gosched()
			time.Sleep(10 * time.Millisecond)
		}
	}

	before := runtime.NumGoroutine()
	pm.Adopt("app", pid)
	settle()
	afterFirst := runtime.NumGoroutine()

	for i := 0; i < 25; i++ {
		pm.Adopt("app", pid)
	}
	settle()
	afterMany := runtime.NumGoroutine()

	if afterMany > afterFirst {
		t.Errorf("re-adopting the same (name,pid) leaked goroutines: %d after 1 adopt, "+
			"%d after 26 — each leak also holds a kqueue/pidfd", afterFirst, afterMany)
	}
	_ = before

	// Adopting a DIFFERENT pid under the same name must still install a watcher
	// for the new process — the dedupe must not swallow a real re-adoption.
	pm.mu.Lock()
	got := pm.adopted["app"]
	pm.mu.Unlock()
	if got != pid {
		t.Fatalf("adopted pid = %d, want %d", got, pid)
	}
	pm.Adopt("app", pid+1)
	pm.mu.Lock()
	got = pm.adopted["app"]
	pm.mu.Unlock()
	if got != pid+1 {
		t.Errorf("re-adopting a new pid did not take effect: got %d, want %d", got, pid+1)
	}
}
