package cmd

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/graiz/local.vibe/internal/client"
	"github.com/graiz/local.vibe/internal/config"
	"github.com/spf13/cobra"
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Manage the vibe daemon",
}

// openDashboard opens local.vibe in the browser, falling back to localhost
// when DNS for *.vibe isn't wired up yet (e.g. before `vibe setup` has run).
// The browser-launch command is per-OS — see open_<goos>.go.
func openDashboard() {
	cfg, _ := config.Load()
	url := "http://local.vibe"
	if cfg != nil && cfg.Daemon.TLS.Enabled {
		url = "https://local.vibe"
	}
	if _, err := net.LookupHost("local.vibe"); err != nil {
		port := config.DefaultPort
		if cfg != nil && cfg.Daemon.Port > 0 {
			port = cfg.Daemon.Port
		}
		url = fmt.Sprintf("http://localhost:%d", port)
	}
	fmt.Printf("opening %s\n", url)
	_ = openURL(url)
}

var daemonStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		if isDaemonRunning() {
			pid, _ := readDaemonPID()
			fmt.Printf("daemon already running (pid %d)\n", pid)
			openDashboard()
			return nil
		}

		// Try the platform-native autostart hook first (LaunchAgent on
		// macOS, scheduled task on Windows in Phase 2). If it's not
		// installed, fall through to a fork-based start.
		handled, err := tryPlatformDaemonStart()
		if err != nil {
			return err
		}
		if handled {
			// schtasks /run on Windows can take several seconds to launch
			// the elevated process — wait for HTTP-ready, not just pidfile.
			if waitForDaemonReady(8 * time.Second) {
				pid, _ := readDaemonPID()
				fmt.Printf("daemon started (pid %d)\n", pid)
				if warn := scanDaemonLogForWarnings(); warn != "" {
					fmt.Fprintln(os.Stderr, warn)
				}
				openDashboard()
				return nil
			}
			return daemonStartFailed(filepath.Join(config.Dir(), "daemon.log"))
		}

		// Fallback: fork-based start
		fmt.Println("tip: run `sudo vibe setup` (or the platform equivalent) to install autostart at login")
		return forkDaemon()
	},
}

var daemonStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		handled, err := tryPlatformDaemonStop()
		if err != nil {
			return err
		}
		if handled {
			fmt.Println("daemon stopped")
			return nil
		}

		pid, err := readDaemonPID()
		if err != nil || pid == 0 {
			fmt.Println("daemon is not running")
			return nil
		}
		if err := signalDaemonStop(pid); err != nil {
			return fmt.Errorf("could not stop daemon: %w", err)
		}
		fmt.Printf("daemon stopped (pid %d)\n", pid)
		return nil
	},
}

var daemonRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		handled, err := tryPlatformDaemonStop()
		if err != nil {
			return err
		}
		if handled {
			time.Sleep(300 * time.Millisecond)
			return daemonStartCmd.RunE(cmd, args)
		}
		_ = daemonStopCmd.RunE(cmd, args)
		time.Sleep(500 * time.Millisecond)
		return daemonStartCmd.RunE(cmd, args)
	},
}

var daemonStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show daemon process status",
	RunE: func(cmd *cobra.Command, args []string) error {
		if isDaemonRunning() {
			pid, _ := readDaemonPID()
			fmt.Printf("running (pid %d)\n", pid)
		} else {
			fmt.Println("not running")
		}
		return nil
	},
}

// forkDaemon spawns `vibe serve` detached from this process (per-OS
// detachAttrs: a new session on unix, a detached no-window process on
// Windows) and waits for it to answer HTTP. Used when no platform autostart
// hook is installed — before `vibe setup`, or on platforms without one.
//
// Readiness is an actual /_api/health response, not the pidfile: the daemon
// writes its pidfile before binding, and may still fail to bind (:53 on
// Windows, say) after writing it.
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
	proc.SysProcAttr = detachAttrs()
	if err := proc.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("failed to start daemon: %w", err)
	}
	logFile.Close()
	if waitForDaemonReady(5 * time.Second) {
		fmt.Printf("daemon started (pid %d)\n", proc.Process.Pid)
		fmt.Printf("log: %s\n", logPath)
		openDashboard()
		return nil
	}
	return daemonStartFailed(logPath)
}

// tailFile returns the last n lines of the file at path, or "" if unreadable.
func tailFile(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// scanDaemonLogForWarnings returns a single-line summary of any "warning:"
// lines in the recent tail of the daemon log, or "" if none. Used to surface
// degraded-mode warnings (e.g. HTTPS bind failure) on a successful start.
func scanDaemonLogForWarnings() string {
	tail := tailFile(filepath.Join(config.Dir(), "daemon.log"), 30)
	if tail == "" {
		return ""
	}
	var warns []string
	for _, line := range strings.Split(tail, "\n") {
		if strings.Contains(line, "warning:") {
			warns = append(warns, strings.TrimSpace(line))
		}
	}
	if len(warns) == 0 {
		return ""
	}
	return strings.Join(warns, "\n")
}

func readDaemonPID() (int, error) {
	data, err := os.ReadFile(filepath.Join(config.Dir(), "daemon.pid"))
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

func isDaemonRunning() bool {
	pid, err := readDaemonPID()
	if err != nil || pid == 0 {
		return false
	}
	return cliProcessAlive(pid)
}

// daemonHTTPResponding reports whether the daemon answers a health check.
// Stronger than isDaemonRunning (which only checks pidfile + process alive)
// because it confirms the daemon is actually serving requests.
//
// It uses the same client as the rest of the CLI — unix socket preferred, TCP
// (127.0.0.1:7999) fallback — rather than a raw TCP dial to 7999. A daemon
// that's up and serving over the socket then isn't reported "down" just because
// a direct TCP connection to 7999 is unreachable (e.g. a pf rule filtering it),
// which is exactly what made `vibe dev` print a spurious "daemon not running".
func daemonHTTPResponding() bool {
	_, err := client.New().Health()
	return err == nil
}

// waitForDaemonReady polls daemonHTTPResponding for up to maxWait. Returns
// true if the daemon answered within the deadline. Used by `vibe dev` and
// the daemon-start command to know when it's safe to claim the daemon
// is up — schtasks /run can take a few seconds to launch elevated, and
// pidfile presence alone isn't enough.
func waitForDaemonReady(maxWait time.Duration) bool {
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		if isDaemonRunning() && daemonHTTPResponding() {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func init() {
	daemonCmd.AddCommand(daemonStartCmd)
	daemonCmd.AddCommand(daemonStopCmd)
	daemonCmd.AddCommand(daemonRestartCmd)
	daemonCmd.AddCommand(daemonStatusCmd)
}
