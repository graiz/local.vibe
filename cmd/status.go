package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/graiz/local.vibe/internal/client"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show daemon health",
	RunE: func(cmd *cobra.Command, args []string) error {
		c := client.New()
		h, err := c.Health()
		if err != nil {
			fmt.Fprintf(os.Stderr, "daemon not running: %v\n", err)
			os.Exit(1)
		}
		uptime := time.Duration(h.Uptime) * time.Second
		fmt.Printf("daemon:  %s\n", h.Status)
		fmt.Printf("uptime:  %s\n", uptime.Round(time.Second))
		fmt.Printf("routes:  %d\n", h.Routes)
		return nil
	},
}
