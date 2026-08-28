//go:build linux

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

// tryPlatformDaemonStart starts the daemon via the user systemd unit when
// installed. Returns handled=false (so the caller forks directly) before
// `vibe setup` has run.
func tryPlatformDaemonStart() (bool, error) {
	if !linuxUserUnitInstalled() {
		return false, nil
	}
	out, err := exec.Command("systemctl", "--user", "start", "vibe.service").CombinedOutput()
	if err != nil {
		return true, fmt.Errorf("systemctl --user start: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return true, nil
}

// tryPlatformDaemonStop stops the daemon via the user systemd unit when
// installed.
func tryPlatformDaemonStop() (bool, error) {
	if !linuxUserUnitInstalled() {
		return false, nil
	}
	out, err := exec.Command("systemctl", "--user", "stop", "vibe.service").CombinedOutput()
	if err != nil {
		return true, fmt.Errorf("systemctl --user stop: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return true, nil
}

// linuxUserUnitInstalled reports whether the user-level vibe.service exists.
// Checked by file path so every `vibe daemon start` doesn't spawn systemctl.
func linuxUserUnitInstalled() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(home, ".config", "systemd", "user", "vibe.service"))
	return err == nil
}

// forkDaemon detaches via Setsid (works on all unix-like systems).
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

func signalDaemonStop(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(syscall.SIGTERM)
}
