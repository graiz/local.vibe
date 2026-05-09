//go:build windows

package daemon

// processAlive on Windows is a Phase 1 stub. The unix Signal(0) trick has no
// Windows equivalent inside stdlib, and we don't want to pull in
// golang.org/x/sys/windows just for liveness checks during Phase 1.
//
// Returning true for any positive PID means stale managed routes won't be
// auto-cleaned on Windows yet — but the daemon won't crash, and macOS
// behavior is unaffected. Phase 2 will replace this with OpenProcess +
// GetExitCodeProcess via golang.org/x/sys/windows.
func processAlive(pid int) bool {
	return pid > 0
}
