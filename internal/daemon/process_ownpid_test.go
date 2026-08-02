//go:build !windows

package daemon

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestOwnsPIDMatchesDescendants is the regression test for vibe SIGTERM'ing
// another project's dev server. Managed children are spawned through a login
// shell, so the process holding the route's port is a descendant, not the
// process-group leader vibe recorded. killPort resolves the port holder via
// lsof — which reports that descendant — so a leader-only OwnsPID returns
// false and the "never signal a managed route" guard silently fails open.
func TestOwnsPIDMatchesDescendants(t *testing.T) {
	// A leader (sh) that spawns a child (sleep) in the same process group,
	// mirroring "$SHELL -lc 'npm run dev'" spawning node.
	cmd := exec.Command("sh", "-c", "sleep 30 & echo $!; wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	leader := cmd.Process.Pid
	defer func() {
		_ = syscall.Kill(-leader, syscall.SIGKILL)
		_ = cmd.Wait()
	}()

	buf := make([]byte, 32)
	n, err := stdout.Read(buf)
	if err != nil {
		t.Fatalf("read child pid: %v", err)
	}
	descendant, err := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	if err != nil {
		t.Fatalf("parse child pid %q: %v", string(buf[:n]), err)
	}
	if descendant == leader {
		t.Skip("shell exec-optimized away the child; no descendant to test")
	}
	// Give the child a moment to be reparented into the group.
	time.Sleep(50 * time.Millisecond)

	pm := NewProcessManager()
	pm.Adopt("app", leader)

	if !pm.OwnsPID(leader) {
		t.Errorf("OwnsPID(leader %d) = false", leader)
	}
	if !pm.OwnsPID(descendant) {
		t.Errorf("OwnsPID(descendant %d) = false — killPort would SIGTERM a managed "+
			"route's real server, since lsof reports the descendant, not the leader",
			descendant)
	}
	// A process in no managed group must still be killable.
	if pm.OwnsPID(1) {
		t.Error("OwnsPID(1) = true; unrelated processes must not be claimed")
	}
}
