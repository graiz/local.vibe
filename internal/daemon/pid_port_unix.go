//go:build !windows

package daemon

import "syscall"

// pidGroupHoldsPort reports whether port is held by a member of pid's process
// group. It is the ownership proof used before signalling a process the daemon
// only knows from persisted state: a PID alone is not evidence of identity
// across daemon downtime, because the OS recycles PIDs, but "this pid's group
// is listening on the port we registered" is.
//
// Fails closed — an unreadable process group, an unknown port, or an empty
// lsof result all return false, so the caller does not kill.
func pidGroupHoldsPort(pid, port int) bool {
	if pid <= 0 || port <= 0 {
		return false
	}
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		return false
	}
	group, err := pidsInGroup(pgid)
	if err != nil || len(group) == 0 {
		return false
	}
	inGroup := make(map[int]bool, len(group))
	for _, p := range group {
		inGroup[p] = true
	}
	for _, lp := range pidsListeningOnPort(port) {
		if inGroup[lp] {
			return true
		}
	}
	return false
}
