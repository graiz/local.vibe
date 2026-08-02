package daemon

import (
	"os"
	"testing"
)

// TestKillPortRefusesDaemonAndManaged guards the daemon-suicide bug: a managed
// route's primary port can collide with a port the daemon already binds for
// another route (an oauth_callback_port / reserve_port). preflightPort →
// killPort must never signal the daemon itself, nor a process owned by another
// managed route — only genuine external holders.
func TestKillPortRefusesDaemonAndManaged(t *testing.T) {
	s := testServer()

	// A managed (adopted) route process the daemon owns. It must be a LIVE
	// pid: Adopt starts a watcher that removes the adoption as soon as the
	// process is gone, and on platforms whose watchPIDExit polls (Windows)
	// that check runs immediately — a synthetic pid was therefore un-adopted
	// out from under the assertion, intermittently, depending on whether the
	// goroutine got scheduled before killPort ran. The parent process is alive
	// for the duration of the test and is not our own pid.
	managedPID := os.Getppid()
	s.procs.Adopt("other", managedPID)

	const externalPID = 999000
	holders := []int{os.Getpid(), managedPID, externalPID}

	origHolders, origTerm := findPortHoldersFn, terminateProcessFn
	defer func() { findPortHoldersFn, terminateProcessFn = origHolders, origTerm }()

	var killed []int
	findPortHoldersFn = func(int) []int { return holders }
	terminateProcessFn = func(pid int) error { killed = append(killed, pid); return nil }

	s.killPort(7265)

	if len(killed) != 1 || killed[0] != externalPID {
		t.Errorf("killPort signaled %v; want only the external pid [%d] (never the daemon %d or managed %d)",
			killed, externalPID, os.Getpid(), managedPID)
	}
}
