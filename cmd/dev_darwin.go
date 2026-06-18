//go:build darwin

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
)

// restartDaemonForDev restarts the daemon LaunchAgent with `launchctl kickstart
// -k`, which kills the running instance and relaunches it in a single launchd
// operation.
//
// This replaced the old approach of killing the daemon PID and waiting for the
// LaunchAgent's KeepAlive to respawn it: that path raced launchd's ~10s minimum
// respawn throttle, so running `vibe dev` in quick succession made the readiness
// wait time out and falsely report "daemon not running" even though the daemon
// came back moments later. kickstart is an explicit, deterministic restart.
func restartDaemonForDev() error {
	if u, err := user.Current(); err == nil {
		target := fmt.Sprintf("gui/%s/com.vibe.daemon", u.Uid)
		if err := exec.Command("launchctl", "kickstart", "-k", target).Run(); err == nil {
			return nil
		}
		// Fall through to a best-effort kill if kickstart didn't take (e.g. the
		// LaunchAgent isn't installed) — KeepAlive will respawn if present.
	}
	pid, err := readDaemonPID()
	if err != nil || pid <= 0 {
		return nil
	}
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Signal(os.Kill)
	}
	return nil
}
