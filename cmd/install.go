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

		if err := installTo(self, dest); err != nil {
			return err
		}
		fmt.Printf("installed → %s\n", dest)
		return nil
	},
}

// installTo copies the binary to dest and signs the copy the way `vibe dev`
// signs its builds (macOS only; a no-op elsewhere), so an installed vibe keeps
// its privacy grants across updates too.
func installTo(self, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return fmt.Errorf("create install dir %s: %w", filepath.Dir(dest), err)
	}
	if err := copyFile(self, dest); err != nil {
		return fmt.Errorf("install to %s: %w\n(try running with elevated privileges)", dest, err)
	}
	signBuild(dest)
	return nil
}

// codesignCmd signs a built binary in place — setup.sh runs it on the binary
// it just built, before copying it into place.
var codesignCmd = &cobra.Command{
	Use:    "codesign <path>",
	Short:  "Sign a built vibe binary with this machine's signing identity (macOS)",
	Hidden: true,
	Args:   cobra.ExactArgs(1),
	Run:    func(cmd *cobra.Command, args []string) { signBuild(args[0]) },
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
	rootCmd.AddCommand(codesignCmd)
}
