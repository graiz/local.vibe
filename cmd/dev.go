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
			binary = defaultVibeInstallPath()
		}

		fmt.Printf("building from %s...\n", srcDir)
		if err := replaceVibeBinary(srcDir, binary); err != nil {
			return err
		}
		fmt.Println("build ok")

		// Restart the daemon. On macOS the LaunchAgent KeepAlive will revive
		// it automatically once we kill the old PID; on Windows we have to
		// explicitly schtasks /run (or fork) since the Scheduled Task is
		// OnLogon-only. restartDaemonForDev encapsulates the platform pick.
		if err := restartDaemonForDev(); err != nil {
			fmt.Fprintf(os.Stderr, "daemon restart: %v\n", err)
		}

		// Wait for the daemon to actually answer HTTP before declaring
		// success. schtasks /run on Windows can take a few seconds to
		// launch the elevated process, and even on macOS LaunchAgent's
		// KeepAlive respawn isn't instant — a flat 1s sleep was racy.
		if waitForDaemonReady(8 * time.Second) {
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
