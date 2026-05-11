//go:build darwin

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/graiz/local.vibe/internal/config"
)

// tryPlatformDaemonStart loads the LaunchAgent if it's installed. Returns
// (handled=true) when the LaunchAgent took over the lifecycle so the caller
// shouldn't fork a separate daemon process.
func tryPlatformDaemonStart() (bool, error) {
	agentPlist := launchAgentPlist()
	if _, err := os.Stat(agentPlist); err != nil {
		return false, nil
	}
	out, err := exec.Command("launchctl", "load", "-w", agentPlist).CombinedOutput()
	if err != nil {
		return true, fmt.Errorf("launchctl load: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return true, nil
}

// tryPlatformDaemonStop unloads the LaunchAgent if installed.
func tryPlatformDaemonStop() (bool, error) {
	agentPlist := launchAgentPlist()
	if _, err := os.Stat(agentPlist); err != nil {
		return false, nil
	}
	out, err := exec.Command("launchctl", "unload", agentPlist).CombinedOutput()
	if err != nil {
		return true, fmt.Errorf("launchctl unload: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return true, nil
}

// forkDaemon detaches the daemon from the current shell session via Setsid
// so it survives the CLI exit. Used when no LaunchAgent is installed (e.g.
// before `vibe setup` has run).
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
	proc.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
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

// cliProcessAlive checks whether a daemon PID is still running. Uses signal
// 0 — a no-op delivery that errors if the process has exited.
func cliProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// signalDaemonStop sends SIGTERM to a non-LaunchAgent-managed daemon.
func signalDaemonStop(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(syscall.SIGTERM)
}
