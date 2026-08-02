package daemon

// processGroupOf has no Windows equivalent: managed children are contained by
// Job Objects rather than process groups, and killPort's guard there works off
// the tracked pids directly.
func processGroupOf(pid int) (int, bool) { return 0, false }
