package cmd

import (
	"fmt"
	"os"

	"github.com/localvibe/vibe/internal/client"
	"github.com/spf13/cobra"
)

var deregisterCmd = &cobra.Command{
	Use:     "deregister <name>",
	Short:   "Remove a registered route",
	Aliases: []string{"rm", "remove"},
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := client.New()
		if err := c.Deregister(args[0]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Printf("deregistered: %s\n", args[0])
		return nil
	},
}
