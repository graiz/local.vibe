//go:build !windows

package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// stubbornCmd ignores SIGTERM for real. It must run under a non-interactive
// sh: vibe wraps commands in an interactive login shell, which does not pass
// a `trap ” TERM` on to the command it runs — the old inline form died on the
// first SIGTERM in ~20ms, so these tests never reached the SIGKILL path.
const stubbornCmd = `exec sh -c 'trap "" TERM; sleep 60'`

// A child that ignores SIGTERM must still die: Stop escalates to SIGKILL on
// the process group after a grace period, and only then reports success.
// Without this the route read "stopped" while the process kept the port, and
// the next start's killPort SIGTERMed it again to no effect.
func TestStopEscalatesToSIGKILL(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	pm := NewProcessManager()
	pm.stopGrace = 300 * time.Millisecond
	route := &Route{Name: "stubborn", Type: RouteManaged, Port: 1, Dir: home,
		Cmd: stubbornCmd}
	pid, err := pm.Start(route)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := pm.Stop("stubborn"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if processAlive(pid) {
		t.Fatalf("pid %d still alive after Stop", pid)
	}
}

// The common real shape: the leader (login shell / npm) exits promptly on
// SIGTERM, but the grandchild actually serving the port traps TERM. Waiting
// on the leader alone reported success with the grandchild still alive, so
// stopManaged skipped its killPort fallback and the port stayed held.
func TestStopWaitsForWholeProcessGroup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	pidFile := filepath.Join(home, "grandchild.pid")
	pm := NewProcessManager()
	pm.stopGrace = 300 * time.Millisecond
	route := &Route{Name: "grand", Type: RouteManaged, Port: 1, Dir: home,
		// exec a non-interactive sh as the leader: the interactive login
		// shell vibe spawns ignores SIGTERM itself, which would mask the bug
		// by forcing the SIGKILL escalation anyway.
		Cmd: "exec sh -c '(trap \"\" TERM; exec sleep 60) & echo $! > " + pidFile + "; wait'"}
	if _, err := pm.Start(route); err != nil {
		t.Fatalf("start: %v", err)
	}
	var gpid int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(pidFile); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n > 0 {
				gpid = n
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if gpid == 0 {
		t.Fatal("grandchild never wrote its pid")
	}
	if err := pm.Stop("grand"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	// Stop's contract: it returns only once the tree is gone. No grace loop
	// here — a surviving grandchild at this instant is the bug.
	if processAlive(gpid) {
		syscall.Kill(gpid, syscall.SIGKILL)
		t.Fatalf("grandchild %d survived Stop (TERM-trapping descendant not escalated)", gpid)
	}
}

// A Start that arrives while Stop is still tearing the old tree down must
// wait for it, not spawn alongside it (or be told the dying pid is "already
// running").
func TestStartDuringStopWaitsForOldTree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	pm := NewProcessManager()
	pm.stopGrace = 400 * time.Millisecond
	route := &Route{Name: "flip", Type: RouteManaged, Port: 1, Dir: home,
		Cmd: stubbornCmd}
	oldPID, err := pm.Start(route)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- pm.Stop("flip") }()
	time.Sleep(100 * time.Millisecond) // Stop is inside its grace window now
	newPID, err := pm.Start(route)
	// Checked before waiting on Stop: the old tree traps TERM and is only
	// ~100ms into a 400ms grace window, so a Start that didn't wait would
	// return here with the old tree still alive.
	oldAliveAtStart := processAlive(oldPID)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	t.Cleanup(func() { _ = pm.Stop("flip") })
	if err := <-stopped; err != nil {
		t.Fatalf("stop: %v", err)
	}
	if newPID == oldPID {
		t.Fatalf("Start during Stop returned the dying pid %d as already running", oldPID)
	}
	if oldAliveAtStart {
		t.Fatalf("Start returned while the old tree (pid %d) was still alive — it didn't wait for Stop", oldPID)
	}
	if !pm.IsRunning("flip") {
		t.Fatal("the new child is not tracked after the old Stop finished")
	}
}

// Two Stops for one route (the idle timer racing a user stop) must share one
// teardown: the second used to overwrite the stopping entry, the first's
// cleanup then deleted it, and a Start arriving during the second's escalation
// spawned alongside the dying tree. Both Stops return only once it is gone.
func TestConcurrentStopsShareOneTeardown(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	pm := NewProcessManager()
	pm.stopGrace = 400 * time.Millisecond
	route := &Route{Name: "twice", Type: RouteManaged, Port: 1, Dir: home,
		Cmd: stubbornCmd}
	pid, err := pm.Start(route)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	errs := make(chan error, 2)
	go func() { errs <- pm.Stop("twice") }()
	time.Sleep(50 * time.Millisecond)
	go func() { errs <- pm.Stop("twice") }()
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("stop %d: %v", i, err)
		}
		if processAlive(pid) {
			t.Fatalf("a Stop returned (%d of 2) while pid %d was still alive", i+1, pid)
		}
	}
	pm.mu.Lock()
	_, left := pm.stopping["twice"]
	pm.mu.Unlock()
	if left {
		t.Fatal("stopping entry leaked after both Stops returned")
	}
}
