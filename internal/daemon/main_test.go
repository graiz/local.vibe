package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestMain fences killPort's signal in this package's tests: it may only
// reach processes this test binary started. Tests run against the real
// machine's ports, and a test once auto-assigned a port a developer's own dev
// server held (findFreePort's probe missed IPv6/wildcard listeners) and the
// preflight SIGTERMed it. Tests that stub terminateProcessFn replace this
// guard for their duration and restore it after.
func TestMain(m *testing.M) {
	real := terminateProcessFn
	terminateProcessFn = func(pid int) error {
		if !isOwnDescendant(pid) {
			fmt.Fprintf(os.Stderr, "test guard: refusing to signal pid %d — not started by this test binary\n", pid)
			return fmt.Errorf("test guard: pid %d is not a descendant of the test process", pid)
		}
		return real(pid)
	}
	os.Exit(m.Run())
}

// isOwnDescendant walks pid's parent chain looking for this process. On
// Windows (no cheap ppid lookup here) it refuses everything, which only makes
// tests stricter.
func isOwnDescendant(pid int) bool {
	if runtime.GOOS == "windows" {
		return false
	}
	self := os.Getpid()
	for i := 0; i < 32 && pid > 1; i++ {
		out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			return false
		}
		ppid, err := strconv.Atoi(strings.TrimSpace(string(out)))
		if err != nil {
			return false
		}
		if ppid == self {
			return true
		}
		pid = ppid
	}
	return false
}
