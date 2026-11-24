package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/graiz/local.vibe/internal/client"
	"github.com/graiz/local.vibe/internal/peer"
	"github.com/spf13/cobra"
)

var (
	listLong bool
	listAll  bool
)

var listCmd = &cobra.Command{
	Use:     "list",
	Short:   "List registered routes",
	Aliases: []string{"ls"},
	Long: `List registered routes.

Flags follow ls(1): -l adds each route's directory and start command, and
-a is accepted so "vibe ls -la" works (every route is already listed).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		warnIfRedirectDown()
		c := client.New()
		routes, err := c.List()
		if err != nil {
			return err
		}
		if len(routes) == 0 {
			fmt.Println("no routes registered")
			return nil
		}
		// Paired peers' routes (experimental). Best-effort like Worktrees —
		// decoration must never break the listing.
		var peers *client.PeersResponse
		if p, err := c.Peers(); err == nil && p.Enabled {
			peers = p
		}
		home, _ := os.UserHomeDir()
		return writeRouteTable(os.Stdout, routes, c.Worktrees(), peers, listLong, home, time.Now())
	},
}

func init() {
	listCmd.Flags().BoolVarP(&listLong, "long", "l", false, "long format: include each route's directory and command")
	listCmd.Flags().BoolVarP(&listAll, "all", "a", false, "list all routes (the default; accepted for the ls -la habit)")
}

func writeRouteTable(out io.Writer, routes []client.RouteInfo, worktrees []client.WorktreeInfo, peers *client.PeersResponse, long bool, home string, now time.Time) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	header := "NAME\tURL\tPORT\tTYPE\tSTATUS\tPID\tSINCE"
	if long {
		header += "\tDIR\tCMD"
	}
	fmt.Fprintln(w, header)
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
		since := now.Sub(r.RegisteredAt).Round(time.Second)
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\t%s ago",
			r.Name, r.URL, r.Port, r.Type, status, pid, since)
		if long {
			fmt.Fprintf(w, "\t%s\t%s", orDash(displayPath(r.Dir, home)), orDash(r.Cmd))
		}
		fmt.Fprintln(w)
	}
	// Git worktrees that exist on disk but have never been started. Without
	// this they were invisible from the CLI, and reachable in the browser
	// only by stopping the parent app to get its picker.
	for _, wt := range worktrees {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s",
			wt.Name, wt.URL, "—", "worktree", "not started", "—", "branch "+wt.Branch)
		if long {
			fmt.Fprintf(w, "\t%s\t%s", orDash(displayPath(wt.Path, home)), "—")
		}
		fmt.Fprintln(w)
	}
	// Shadowing is surfaced, never silent: a local route or an earlier peer
	// claiming the same name wins, so this row's URL would reach the winner
	// instead. A peer's directory and command never leave its machine, so the
	// long columns are always dashes here.
	if peers != nil {
		localNames := make(map[string]bool, len(routes))
		for _, r := range routes {
			localNames[r.Name] = true
		}
		claimedBy := make(map[string]string) // route name → winning peer
		for _, p := range peers.Peers {
			for _, pr := range p.Routes {
				// The status column is always the peer's own view of the
				// route; shadowing goes in the note, so a shadowed row still
				// says whether it runs over there.
				status := peer.Status(pr.Running, pr.Ready, p.Reachable)
				note := "on " + p.Name
				if localNames[pr.Name] {
					note += ", shadowed by this machine"
				} else if winner, taken := claimedBy[pr.Name]; taken {
					note += ", shadowed by " + winner
				} else {
					claimedBy[pr.Name] = p.Name
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s",
					pr.Name, "https://"+pr.Name+".vibe", "—", "peer", status, "—", note)
				if long {
					fmt.Fprint(w, "\t—\t—")
				}
				fmt.Fprintln(w)
			}
		}
	}
	return w.Flush()
}

// displayPath abbreviates home to "~", as shells do. Only a whole leading
// path component matches, so /Users/greg is not abbreviated inside
// /Users/gregory.
func displayPath(p, home string) string {
	if p == "" || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(p, home+string(filepath.Separator)); ok {
		return "~" + string(filepath.Separator) + rest
	}
	return p
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
