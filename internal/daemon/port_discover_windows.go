//go:build windows

package daemon

// portFromProcessGroup on Windows is a Phase 1 stub. There's no concept
// of a process group on Windows; Phase 2 will enumerate the route's Job
// Object members via QueryInformationJobObject and match against
// `netstat -ano` (or iphlpapi.GetExtendedTcpTable) to find listening
// ports owned by those PIDs.
//
// Until then, the daemon's port-discovery falls back to log-scan only,
// which already handles most dev-server output (Vite, Next.js, etc.).
func portFromProcessGroup(route *Route) (int, bool) {
	_ = route
	return 0, false
}
