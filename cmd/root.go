package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// rootCmd is the top-level CLI command. All subcommands are registered in init().
var rootCmd = &cobra.Command{
	Use:   "vibe",
	Short: "Give your local dev servers friendly names",
	Long: `local.vibe routes *.vibe domains to your local dev servers.

Instead of remembering localhost:3000, localhost:5678, localhost:8123...
just use myapp.vibe, n8n.vibe, desk.vibe.`,
}

// SetVersion sets the CLI version string (populated via ldflags at build time).
func SetVersion(v string) {
	rootCmd.Version = v
}

// Execute runs the root command. Called from main().
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(registerCmd)
	rootCmd.AddCommand(deregisterCmd)
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(openCmd)
	rootCmd.AddCommand(daemonCmd)
	rootCmd.AddCommand(setupCmd)
	rootCmd.AddCommand(serveCmd)
}
