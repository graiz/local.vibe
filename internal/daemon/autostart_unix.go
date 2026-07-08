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

// portForeignToRoute reports whether a managed route's *registered* port is
// held exclusively by processes OUTSIDE the route's process group — i.e. a
// stranger squatting a recycled ephemeral port. This closes the gap the
// reverse-proxy ErrorHandler cannot: a squatter that speaks HTTP returns a
// valid response, so the proxy round-trip succeeds and the ErrorHandler never
// fires; without this check vibe would pass the stranger's 401/200 straight to
// the browser.
//
// It is deliberately fail-open. It returns true only when it can positively
// identify a foreign owner: someone is listening on the port and none of the
// listeners belong to the route's process group. Nobody listening, no live
// child to compare against, or a group member among the listeners all return
// false — so a healthy route (or an undeterminable state) is never
// misclassified and needlessly forced into recovery.
func (s *Server) portForeignToRoute(route *Route) bool {
	if route.Type != RouteManaged || route.Port <= 0 {
		return false
	}
	listeners := pidsListeningOnPort(route.Port)
	if len(listeners) == 0 {
		return false // nobody home — readiness/repair handles this, not us
	}
	leader, ok := route.PIDValue()
	if !ok || !processAlive(leader) {
		return false // no live child to anchor ownership on — fail open
	}
	pgid, err := syscall.Getpgid(leader)
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
	for _, lp := range listeners {
		if inGroup[lp] {
			return false // one of ours is listening — healthy
		}
	}
	return true // someone listens on our port, none are ours — a stranger
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
