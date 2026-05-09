//go:build windows

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/graiz/local.vibe/internal/config"
)

// tryPlatformDaemonStart on Windows is a Phase 1 stub. Phase 2 will detect
// a registered Scheduled Task ("vibe") and run it via `schtasks /run /tn vibe`.
func tryPlatformDaemonStart() (bool, error) { return false, nil }

// tryPlatformDaemonStop on Windows is a Phase 1 stub. Phase 2 will end the
// Scheduled Task via `schtasks /end /tn vibe` when present.
func tryPlatformDaemonStop() (bool, error) { return false, nil }

// forkDaemon spawns the daemon as a detached process so it survives the CLI
// exit. Phase 1 uses HideWindow + DETACHED_PROCESS via CreationFlags; Phase
// 2 should also AssignProcessToJobObject for cleaner shutdown semantics.
func forkDaemon() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	_ = os.MkdirAll(config.Dir(), 0755)
	logPath := filepath.Join(config.Dir(), "daemon.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	proc := exec.Command(self, "serve")
	proc.Stdout = logFile
	proc.Stderr = logFile
	// DETACHED_PROCESS (0x00000008) | CREATE_NEW_PROCESS_GROUP (0x00000200)
	// keeps the daemon alive after the CLI exits and gives it its own
	// console group (so a Ctrl+C in the parent doesn't propagate).
	proc.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x00000008 | 0x00000200,
	}
	if err := proc.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("failed to start daemon: %w", err)
	}
	logFile.Close()
	for i := 0; i < 10; i++ {
		time.Sleep(200 * time.Millisecond)
		if isDaemonRunning() {
			fmt.Printf("daemon started (pid %d)\n", proc.Process.Pid)
			fmt.Printf("log: %s\n", logPath)
			openDashboard()
			return nil
		}
	}
	if tail := tailFile(logPath, 20); tail != "" {
		return fmt.Errorf("daemon did not start — last lines of %s:\n%s", logPath, tail)
	}
	return fmt.Errorf("daemon did not start — check %s", logPath)
}

// cliProcessAlive on Windows is a Phase 1 stub: any positive PID is treated
// as alive. Phase 2 will use OpenProcess + GetExitCodeProcess via
// golang.org/x/sys/windows so stale PID files don't masquerade as a running
// daemon. For Phase 1, the worst case is `daemon start` printing "already
// running" when the daemon actually crashed — recoverable by deleting the
// pid file.
func cliProcessAlive(pid int) bool {
	return pid > 0
}

// signalDaemonStop on Windows uses os.Process.Kill (TerminateProcess) since
// SIGTERM doesn't exist. The daemon doesn't get a chance to clean up its
// pid file or unix socket — both are best-effort cleanup-on-start anyway.
func signalDaemonStop(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}
