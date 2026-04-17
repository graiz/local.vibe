package cmd

import (
	"fmt"
	"os"

	"github.com/graiz/local.vibe/internal/config"
	"github.com/graiz/local.vibe/internal/daemon"
	"github.com/spf13/cobra"
)

// serveCmd is the hidden command that actually runs the daemon loop.
// Users interact with "daemon start/stop" instead.
var serveCmd = &cobra.Command{
	Use:    "serve",
	Short:  "Run the daemon in the foreground",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: config error (%v), using defaults\n", err)
			cfg = config.DefaultConfig()
		}
		srv := daemon.NewServer(cfg)
		return srv.Start()
	},
}
