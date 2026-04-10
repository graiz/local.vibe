package cmd

import (
	"fmt"
	"os"

	"github.com/localvibe/vibe/internal/client"
	"github.com/spf13/cobra"
)

var stopCmd = &cobra.Command{
	Use:   "stop <name>",
	Short: "Stop a managed app",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := client.New()
		if err := c.Stop(args[0]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Printf("stopped: %s\n", args[0])
		return nil
	},
}

func init() {
	rootCmd.AddCommand(stopCmd)
}
