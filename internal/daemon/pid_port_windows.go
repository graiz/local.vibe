package daemon

// pidGroupHoldsPort has no Windows equivalent: managed children are contained
// by Job Objects, which do not survive the daemon, so there is never an
// adopted survivor to re-verify. Windows relies on auto-spawn instead.
func pidGroupHoldsPort(pid, port int) bool { return false }
