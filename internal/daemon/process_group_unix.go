//go:build !windows

package daemon

import "syscall"

// processGroupOf returns pid's process group id. Managed children are started
// with Setpgid, so a child's whole tree shares the leader's pid as its pgid —
// which is how a descendant is traced back to the route that owns it.
func processGroupOf(pid int) (int, bool) {
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		return 0, false
	}
	return pgid, true
}
