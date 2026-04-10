package cmd

import (
	"fmt"
	"os"

	"github.com/localvibe/vibe/internal/client"
	"github.com/spf13/cobra"
)

var registerCmd = &cobra.Command{
	Use:   "register <name> <port>",
	Short: "Register a local service",
	Example: `  vibe register myapp 3000
  vibe register myapp 3000 --pid $$
  vibe register myapp 3000 --ttl 3600`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		var port int
		if _, err := fmt.Sscan(args[1], &port); err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("invalid port: %s", args[1])
		}

		req := client.RegisterRequest{Name: name, Port: port}

		if cmd.Flags().Changed("pid") {
			pid, _ := cmd.Flags().GetInt("pid")
			req.PID = &pid
		}
		if cmd.Flags().Changed("ttl") {
			ttl, _ := cmd.Flags().GetInt("ttl")
			req.TTL = &ttl
		}

		c := client.New()
		resp, err := c.Register(req)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Printf("registered: %s → %s\n", name, resp.URL)
		return nil
	},
}

func init() {
	registerCmd.Flags().Int("pid", 0, "Watch PID; auto-deregister when process exits")
	registerCmd.Flags().Int("ttl", 0, "Auto-expire route after N seconds")
}
