package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Install the vibe binary to your PATH",
	Long:  `Copies the current binary to a standard location so it's available as 'vibe' in any terminal.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dest := installDestination()

		self, err := os.Executable()
		if err != nil {
			return fmt.Errorf("could not find current binary: %w", err)
		}
		// Resolve symlinks so we copy the real binary
		self, _ = filepath.EvalSymlinks(self)

		if self == dest {
			fmt.Printf("already installed at %s\n", dest)
			return nil
		}

		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return fmt.Errorf("create install dir %s: %w", filepath.Dir(dest), err)
		}

		if err := copyFile(self, dest); err != nil {
			return fmt.Errorf("install to %s: %w\n(try running with elevated privileges)", dest, err)
		}

		fmt.Printf("installed → %s\n", dest)
		return nil
	},
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func init() {
	rootCmd.AddCommand(installCmd)
}
