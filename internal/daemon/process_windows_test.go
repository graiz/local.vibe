//go:build windows

package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ensureWindowsPath augments PATH with %SystemRoot%\System32 so cmd.exe
// can find ping/timeout/etc. when these tests run from a non-Windows
// shell (e.g. a git-bash session passing a POSIX-style PATH down). In a
// regular `cmd /go test` run this is a no-op since System32 is already
// on PATH.
func ensureWindowsPath(t *testing.T) {
	t.Helper()
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	sys32 := filepath.Join(root, "System32")
	t.Setenv("PATH", sys32+";"+os.Getenv("PATH"))
}

// TestProcessWindowsStartAndStop spawns `cmd.exe /C ping -n 60 127.0.0.1`
// (a long-lived no-op available on every Windows install), confirms it
// shows up as alive, and verifies that Stop terminates it via the Job
// Object. Ping is portable across every Windows SKU we care about.
func TestProcessWindowsStartAndStop(t *testing.T) {
	ensureWindowsPath(t)
	pm := NewProcessManager()

	route := &Route{
		Name: "winsleeper",
		// `ping -n 60 127.0.0.1` keeps cmd.exe alive ~60s with minimal CPU.
		Cmd: "ping -n 60 127.0.0.1 > nul",
		Dir: t.TempDir(),
	}

	pid, err := pm.Start(route)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if pid == 0 {
		t.Fatal("Start returned pid 0")
	}
	if !pm.IsRunning("winsleeper") {
		t.Error("IsRunning = false right after Start")
	}

	if err := pm.Stop("winsleeper"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// After TerminateJobObject the process is gone immediately, but the
	// kernel may take a few ticks to update GetExitCodeProcess. Poll briefly.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !pm.IsRunning("winsleeper") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("IsRunning still true 2s after Stop")
}

// TestProcessWindowsEarlyCrashDetection mirrors the unix-tagged version:
// a command that exits immediately should produce a StartError, not a
// running pid.
func TestProcessWindowsEarlyCrashDetection(t *testing.T) {
	ensureWindowsPath(t)
	pm := NewProcessManager()

	route := &Route{
		Name: "wincrasher",
		Cmd:  "exit 1",
		Dir:  t.TempDir(),
	}

	_, err := pm.Start(route)
	if err == nil {
		t.Fatal("expected error for immediately-exiting command")
	}
	if !strings.Contains(err.Error(), "process exited immediately") {
		t.Errorf("error = %q; want 'process exited immediately'", err)
	}
}
