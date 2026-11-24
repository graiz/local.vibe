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
	group := groupPIDs(pid)
	if len(group) == 0 {
		return false
	}
	for _, lp := range pidsListeningOnPort(port) {
		if group[lp] {
			return true
		}
	}
	return false
}

// groupPIDs returns the members of leader's process group as a set, or an
// empty set when the group can't be read. Shared by every ownership check
// (adoption, stranger detection, the load-time prune) so they can't disagree
// about what "ours" means.
func groupPIDs(leader int) map[int]bool {
	pgid, err := syscall.Getpgid(leader)
	if err != nil {
		return nil
	}
	group, err := pidsInGroup(pgid)
	if err != nil {
		return nil
	}
	set := make(map[int]bool, len(group))
	for _, p := range group {
		set[p] = true
	}
	return set
}
