package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
)

var devCmd = &cobra.Command{
	Use:   "dev",
	Short: "Rebuild and restart the daemon (for vibe development)",
	Long:  `Rebuilds the vibe binary from source and restarts the daemon. Run this after making code changes.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		srcDir, _ := cmd.Flags().GetString("src")
		if srcDir == "" {
			// Try to find go.mod in current or parent directories
			dir, _ := os.Getwd()
			for {
				if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
					srcDir = dir
					break
				}
				parent := filepath.Dir(dir)
				if parent == dir {
					break
				}
				dir = parent
			}
		}
		if srcDir == "" {
			return fmt.Errorf("could not find go.mod — run from the vibe source directory or use --src")
		}

		binary, err := exec.LookPath("vibe")
		if err != nil {
			binary = "/opt/homebrew/bin/vibe"
		}

		fmt.Printf("building from %s...\n", srcDir)
		build := exec.Command("go", "build", "-o", binary, ".")
		build.Dir = srcDir
		build.Stdout = os.Stdout
		build.Stderr = os.Stderr
		if err := build.Run(); err != nil {
			return fmt.Errorf("build failed: %w", err)
		}
		fmt.Println("build ok")

		// Kill the running daemon — LaunchAgent will restart with the new binary.
		pid, err := readDaemonPID()
		if err == nil && pid > 0 {
			if p, err := os.FindProcess(pid); err == nil {
				_ = p.Signal(os.Kill)
			}
		}

		// Wait for new daemon to come up.
		time.Sleep(1 * time.Second)
		if isDaemonRunning() {
			pid, _ := readDaemonPID()
			fmt.Printf("daemon restarted (pid %d)\n", pid)
		} else {
			fmt.Println("daemon not running — start with: vibe daemon start")
		}
		return nil
	},
}

func init() {
	devCmd.Flags().String("src", "", "Path to vibe source directory")
	rootCmd.AddCommand(devCmd)
}
