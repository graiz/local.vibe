//go:build windows

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/graiz/local.vibe/internal/winutil"
	"golang.org/x/sys/windows"
)

const scheduledTaskName = "vibe"

// tryPlatformDaemonStart prefers the Scheduled Task installed by setup,
// which runs the daemon at logon at the user's normal integrity level
// (see installScheduledTask for the rationale on dropping /rl HIGHEST).
// If the task doesn't exist (setup was skipped), returns handled=false
// so the caller falls through to a plain forkDaemon.
//
// When the task is missing we warn the user to run `vibe setup` so the
// daemon comes up automatically at next logon — but the fork still works
// fine without admin, since none of the daemon's runtime operations need
// elevation on Windows.
func tryPlatformDaemonStart() (bool, error) {
	if !scheduledTaskExists(scheduledTaskName) {
		fmt.Fprintln(os.Stderr,
			"note: no Scheduled Task registered. The daemon will start in this\n"+
				"      session, but won't auto-start at logon. Run `vibe setup`\n"+
				"      from an elevated terminal to register the autostart task.")
		return false, nil
	}
	out, err := exec.Command(winutil.Sys32("schtasks"), "/run", "/tn", scheduledTaskName).CombinedOutput()
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
	out, err := exec.Command(winutil.Sys32("schtasks"), "/end", "/tn", scheduledTaskName).CombinedOutput()
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
	cmd := exec.Command(winutil.Sys32("schtasks"), "/query", "/tn", name)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// detachAttrs starts the forked daemon as a detached, windowless process so
// it survives the CLI's console closing and never pops a console of its own.
func detachAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
	}
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
