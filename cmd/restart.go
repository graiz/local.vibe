package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/graiz/local.vibe/internal/client"
	"github.com/spf13/cobra"
)

var restartCmd = &cobra.Command{
	Use:   "restart <name>",
	Short: "Stop and start a managed app",
	Long: `Stop a managed app and start it again. Equivalent to:

  vibe stop <name> && vibe start <name>

Useful when reloading code that survived watch mode (e.g. environment
variable changes) or when recovering from a stuck child process.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		c := client.New()
		// Stop is idempotent — the daemon falls back to killPort if the
		// child process isn't tracked, so a route that's already stopped
		// is fine here.
		if err := c.Stop(name); err != nil {
			fmt.Fprintln(os.Stderr, "error stopping:", err)
			os.Exit(1)
		}
		// Brief settle so the port releases before the daemon's preflight
		// re-checks it on Start. Without this, a fast restart can hit a
		// spurious "port in use" recovery prompt.
		time.Sleep(300 * time.Millisecond)
		if err := c.Start(name); err != nil {
			fmt.Fprintln(os.Stderr, "error starting:", err)
			os.Exit(1)
		}
		fmt.Printf("restarted: %s\n", name)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(restartCmd)
}
