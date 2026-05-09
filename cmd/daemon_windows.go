//go:build windows

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
	"golang.org/x/sys/windows"
)

const scheduledTaskName = "vibe"

// tryPlatformDaemonStart prefers the Scheduled Task installed by setup. It
// runs the daemon with elevated rights (the task is registered with
// /rl HIGHEST), which is needed to bind UDP :53 reliably and to keep
// HTTPS hot-reloading clean. If the task doesn't exist (setup was skipped),
// returns handled=false so the caller falls through to a plain forkDaemon.
func tryPlatformDaemonStart() (bool, error) {
	if !scheduledTaskExists(scheduledTaskName) {
		return false, nil
	}
	out, err := exec.Command("schtasks", "/run", "/tn", scheduledTaskName).CombinedOutput()
	if err != nil {
		return true, fmt.Errorf("schtasks /run %s: %w — %s", scheduledTaskName, err, strings.TrimSpace(string(out)))
	}
	return true, nil
}

// tryPlatformDaemonStop ends the Scheduled Task's running instance via
// schtasks /end. If the task was never installed, fall through.
//
// /end terminates only the currently-running task instance — it doesn't
// delete the task itself, so autostart at next logon still works. Use
// `vibe uninstall` to remove the task entirely.
func tryPlatformDaemonStop() (bool, error) {
	if !scheduledTaskExists(scheduledTaskName) {
		return false, nil
	}
	out, err := exec.Command("schtasks", "/end", "/tn", scheduledTaskName).CombinedOutput()
	if err != nil {
		// "task is not running" returns a non-zero exit but we still want to
		// report success to the caller — the daemon ended up in the right
		// state either way. schtasks's exact wording varies by locale, so
		// match conservatively on the SCHED_E_NOT_RUNNING-ish exit code.
		combined := strings.ToLower(string(out))
		if strings.Contains(combined, "not running") || strings.Contains(combined, "not currently running") {
			return true, nil
		}
		return true, fmt.Errorf("schtasks /end %s: %w — %s", scheduledTaskName, err, strings.TrimSpace(string(out)))
	}
	return true, nil
}

// scheduledTaskExists returns true if a task with the given name is
// registered with Task Scheduler. We use `schtasks /query` and key off the
// exit code; non-zero means "not found" (or some deeper error, in which
// case we fall through to forkDaemon and surface the real failure there).
func scheduledTaskExists(name string) bool {
	cmd := exec.Command("schtasks", "/query", "/tn", name)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// forkDaemon spawns the daemon detached so it survives the CLI exit.
// Used when no Scheduled Task is installed yet (e.g. before `vibe setup`)
// or when the user explicitly runs `vibe serve` themselves.
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
	const (
		detachedProcess        = 0x00000008
		createNewProcessGroup  = 0x00000200
		createNoWindow         = 0x08000000
	)
	proc.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: detachedProcess | createNewProcessGroup | createNoWindow,
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

// cliProcessAlive on Windows opens a query handle and reads the exit code.
// STILL_ACTIVE means the process hasn't exited yet — same trick the daemon
// internals use, but redeclared here so the cmd package doesn't reach into
// internal/daemon for a private helper.
func cliProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var exitCode uint32
	if err := windows.GetExitCodeProcess(h, &exitCode); err != nil {
		return false
	}
	const stillActive = 259 // STILL_ACTIVE
	return exitCode == stillActive
}

// signalDaemonStop on Windows uses os.Process.Kill — there's no graceful
// equivalent of SIGTERM that Go can deliver to an arbitrary console
// process from outside its console group. The daemon's pid file and unix
// socket are removed best-effort on next startup.
func signalDaemonStop(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}
