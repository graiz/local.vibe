package daemon

import (
	"strings"
	"testing"
)

func TestProcessStartEarlyCrashDetection(t *testing.T) {
	t.Parallel()
	pm := NewProcessManager()

	route := &Route{
		Name: "crasher",
		Cmd:  "nonexistent_command_xyz",
		Dir:  t.TempDir(),
	}

	_, err := pm.Start(route)
	if err == nil {
		t.Fatal("expected error for nonexistent command")
	}
	if !strings.Contains(err.Error(), "process exited immediately") {
		t.Errorf("error = %q; want 'process exited immediately'", err)
	}
}

func TestProcessStartNoCommand(t *testing.T) {
	t.Parallel()
	pm := NewProcessManager()

	route := &Route{Name: "empty", Dir: t.TempDir()}
	_, err := pm.Start(route)
	if err == nil {
		t.Fatal("expected error for empty command")
	}
	if !strings.Contains(err.Error(), "no command configured") {
		t.Errorf("error = %q; want 'no command configured'", err)
	}
}

func TestProcessStartAndStop(t *testing.T) {
	t.Parallel()
	pm := NewProcessManager()

	route := &Route{
		Name: "sleeper",
		Cmd:  "sleep 60",
		Dir:  t.TempDir(),
	}

	pid, err := pm.Start(route)
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	if pid == 0 {
		t.Fatal("Start() returned pid 0")
	}
	if !pm.IsRunning("sleeper") {
		t.Error("IsRunning() = false after Start()")
	}

	if err := pm.Stop("sleeper"); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}
	if pm.IsRunning("sleeper") {
		t.Error("IsRunning() = true after Stop()")
	}
}

func TestProcessStartAlreadyRunning(t *testing.T) {
	t.Parallel()
	pm := NewProcessManager()

	route := &Route{
		Name: "dup",
		Cmd:  "sleep 60",
		Dir:  t.TempDir(),
	}

	pid1, err := pm.Start(route)
	if err != nil {
		t.Fatalf("first Start() error: %v", err)
	}

	// Starting again should return same PID, not error
	pid2, err := pm.Start(route)
	if err != nil {
		t.Fatalf("second Start() error: %v", err)
	}
	if pid1 != pid2 {
		t.Errorf("second Start() pid = %d; want %d (same process)", pid2, pid1)
	}

	pm.Stop("dup")
}
