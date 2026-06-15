//go:build !windows

package daemon

import (
	"bufio"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// adoptOrphan re-adopts a managed route's surviving child process after a
// daemon restart. On unix, managed children run in their own process group
// (Setpgid) and outlive the daemon that spawned them.
//
// Adoption is deliberately conservative: it confirms that the route's
// *registered* port is still being served by a process that belongs to the
// route's process group. That single check rules out three failure modes at
// once — a dead child (PID not alive), a stranger that grabbed the old port
// (not a group member), and an unrelated listener the login shell happened to
// spawn into the group (we anchor on the registered port, not "whatever the
// group is listening on"). Only then do we trust the orphan and return its
// process-group leader PID.
//
// The "is it listening" signal is lsof, not a TCP dial: dialing would open a
// connection to the app on every check (perturbing single-connection servers)
// and tells us nothing about ownership. lsof reports the listening PID without
// touching the socket.
//
// Returns ok=false when there is nothing safe to adopt; the caller then falls
// through to the spawn / start-page path.
func (s *Server) adoptOrphan(route *Route) (pid int, port int, ok bool) {
	leader, hasPID := route.PIDValue()
	if !hasPID || !processAlive(leader) {
		return 0, 0, false
	}
	if route.Port <= 0 {
		return 0, 0, false
	}

	pgid, err := syscall.Getpgid(leader)
	if err != nil {
		return 0, 0, false
	}
	group, err := pidsInGroup(pgid)
	if err != nil || len(group) == 0 {
		return 0, 0, false
	}
	inGroup := make(map[int]bool, len(group))
	for _, p := range group {
		inGroup[p] = true
	}
	for _, lp := range pidsListeningOnPort(route.Port) {
		if inGroup[lp] {
			return leader, route.Port, true
		}
	}
	return 0, 0, false
}

// managedPortHealthy reports whether a managed route that believes it is
// running is still genuinely served by its own process group. It reuses the
// exact ownership anchor as adoptOrphan: the route's registered port must be
// held by a member of the route's process group.
//
// This is the monitor's defense against silent rot. processAlive(pid) alone is
// fooled by PID reuse — when a child dies the OS can recycle its PID to an
// unrelated live process — and a bare readiness dial is fooled by a squatter
// that grabs the freed port and answers TCP without speaking HTTP. Anchoring
// on "is the registered port owned by my group" catches both at once: a dead
// child, a recycled PID, and a port stranger all fail it.
//
// It uses lsof (not a dial) for the same reason adoptOrphan does — dialing
// perturbs single-connection servers and tells us nothing about ownership.
func (s *Server) managedPortHealthy(route *Route) bool {
	_, _, ok := s.adoptOrphan(route)
	return ok
}

// pidsListeningOnPort returns the PIDs with a LISTEN socket on the given TCP
// port, via `lsof -t -nP -iTCP:<port> -sTCP:LISTEN`. Empty on none (lsof exits
// non-zero when there are no matches — that's "nobody listening", not an error).
func pidsListeningOnPort(port int) []int {
	out, err := exec.Command("lsof", "-t", "-nP", fmt.Sprintf("-iTCP:%d", port), "-sTCP:LISTEN").Output()
	if err != nil {
		return nil
	}
	var pids []int
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		if p, err := strconv.Atoi(strings.TrimSpace(scanner.Text())); err == nil {
			pids = append(pids, p)
		}
	}
	return pids
}
