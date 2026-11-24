package cmd

import (
	"fmt"

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
			return err
		}
		var url string
		for _, r := range routes {
			if r.Name == args[0] {
				url = r.URL
				break
			}
		}
		if url == "" {
			return fmt.Errorf("no route named %q", args[0])
		}

		fmt.Println(url)
		return openURL(url)
	},
}
