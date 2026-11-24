//go:build !windows

package daemon

import (
	"testing"
	"time"
)

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
		Cmd: "trap '' TERM; sleep 60"}
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
