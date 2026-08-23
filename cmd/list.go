package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/graiz/local.vibe/internal/client"
	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:     "list",
	Short:   "List registered routes",
	Aliases: []string{"ls"},
	RunE: func(cmd *cobra.Command, args []string) error {
		warnIfRedirectDown()
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
		fmt.Fprintln(w, "NAME\tURL\tPORT\tTYPE\tSTATUS\tPID\tSINCE")
		for _, r := range routes {
			pid := "—"
			if r.PID != nil {
				pid = fmt.Sprintf("%d", *r.PID)
			}
			status := "ready"
			if r.Running && !r.Ready {
				status = "starting"
			} else if !r.Running && r.Type == "managed" {
				status = "stopped"
			}
			since := time.Since(r.RegisteredAt).Round(time.Second)
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\t%s ago\n",
				r.Name, r.URL, r.Port, r.Type, status, pid, since)
		}
		// Git worktrees that exist on disk but have never been started. Without
		// this they were invisible from the CLI, and reachable in the browser
		// only by stopping the parent app to get its picker.
		for _, wt := range c.Worktrees() {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				wt.Name, wt.URL, "—", "worktree", "not started", "—", "branch "+wt.Branch)
		}
		// Paired peers' routes (experimental). Best-effort like Worktrees —
		// decoration must never break the listing. Shadowing is surfaced,
		// never silent: a local route or an earlier peer claiming the same
		// name wins, so this row's URL would reach the winner instead.
		if peers, err := c.Peers(); err == nil && peers.Enabled {
			localNames := make(map[string]bool, len(routes))
			for _, r := range routes {
				localNames[r.Name] = true
			}
			seenPeerNames := make(map[string]bool)
			for _, p := range peers.Peers {
				for _, pr := range p.Routes {
					status := "stopped"
					if pr.Ready {
						status = "ready"
					} else if pr.Running {
						status = "starting"
					}
					if localNames[pr.Name] || seenPeerNames[pr.Name] {
						status = "shadowed"
					}
					seenPeerNames[pr.Name] = true
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
						pr.Name, "https://"+pr.Name+".vibe", "—", "peer", status, "—", "on "+p.Name)
				}
			}
		}
		return w.Flush()
	},
}
