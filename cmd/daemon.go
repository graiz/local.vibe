package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/graiz/local.vibe/internal/config"
	"github.com/spf13/cobra"
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Manage the vibe daemon",
}

// openDashboard opens local.vibe or falls back to localhost direct.
func openDashboard() {
	cfg, _ := config.Load()
	url := "http://local.vibe"
	if cfg != nil && cfg.Daemon.TLS.Enabled {
		url = "https://local.vibe"
	}
	if _, err := os.Stat("/etc/resolver/vibe"); err != nil {
		url = "http://localhost:7999"
	}
	fmt.Printf("opening %s\n", url)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return
	}
	_ = cmd.Start()
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

		// Prefer LaunchAgent (installed by `vibe setup`, no sudo needed)
		agentPlist := launchAgentPlist()
		if _, err := os.Stat(agentPlist); err == nil {
			out, err := exec.Command("launchctl", "load", "-w", agentPlist).CombinedOutput()
			if err != nil {
				return fmt.Errorf("launchctl load: %w\n%s", err, strings.TrimSpace(string(out)))
			}
			for i := 0; i < 15; i++ {
				time.Sleep(200 * time.Millisecond)
				if isDaemonRunning() {
					pid, _ := readDaemonPID()
					fmt.Printf("daemon started (pid %d)\n", pid)
					openDashboard()
					return nil
				}
			}
			return fmt.Errorf("daemon did not start — check %s", filepath.Join(config.Dir(), "daemon.log"))
		}

		// Fallback: fork-based start
		fmt.Println("tip: run 'sudo vibe setup' to install autostart at login")
		return forkDaemon()
	},
}

var daemonStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		agentPlist := launchAgentPlist()
		if _, err := os.Stat(agentPlist); err == nil {
			out, err := exec.Command("launchctl", "unload", agentPlist).CombinedOutput()
			if err != nil {
				return fmt.Errorf("launchctl unload: %w\n%s", err, strings.TrimSpace(string(out)))
			}
			fmt.Println("daemon stopped")
			return nil
		}

		pid, err := readDaemonPID()
		if err != nil || pid == 0 {
			fmt.Println("daemon is not running")
			return nil
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			return err
		}
		if err := proc.Signal(syscall.SIGTERM); err != nil {
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
		agentPlist := launchAgentPlist()
		if _, err := os.Stat(agentPlist); err == nil {
			_ = exec.Command("launchctl", "unload", agentPlist).Run()
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
	return fmt.Errorf("daemon did not start — check %s", logPath)
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
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func init() {
	daemonCmd.AddCommand(daemonStartCmd)
	daemonCmd.AddCommand(daemonStopCmd)
	daemonCmd.AddCommand(daemonRestartCmd)
	daemonCmd.AddCommand(daemonStatusCmd)
}
