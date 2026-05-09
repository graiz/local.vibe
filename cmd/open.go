package cmd

import (
	"fmt"
	"os"

	"github.com/graiz/local.vibe/internal/client"
	"github.com/spf13/cobra"
)

var openCmd = &cobra.Command{
	Use:   "open <name>",
	Short: "Open a registered service in the browser",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := client.New()
		routes, err := c.List()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		var url string
		for _, r := range routes {
			if r.Name == args[0] {
				url = r.URL
				break
			}
		}
		if url == "" {
			fmt.Fprintf(os.Stderr, "no route named %q\n", args[0])
			os.Exit(1)
		}

		fmt.Println(url)
		return openURL(url)
	},
}
