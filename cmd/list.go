package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/localvibe/vibe/internal/client"
	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:     "list",
	Short:   "List registered routes",
	Aliases: []string{"ls"},
	RunE: func(cmd *cobra.Command, args []string) error {
		c := client.New()
		routes, err := c.List()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		if len(routes) == 0 {
			fmt.Println("no routes registered")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tURL\tPORT\tTYPE\tPID\tSINCE")
		for _, r := range routes {
			pid := "—"
			if r.PID != nil {
				pid = fmt.Sprintf("%d", *r.PID)
			}
			since := time.Since(r.RegisteredAt).Round(time.Second)
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s ago\n",
				r.Name, r.URL, r.Port, r.Type, pid, since)
		}
		return w.Flush()
	},
}
