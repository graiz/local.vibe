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
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		if len(routes) == 0 {
			fmt.Println("no routes registered")
			return nil
		}
		home, _ := os.UserHomeDir()
		return writeRouteTable(os.Stdout, routes, c.Worktrees(), listLong, home, time.Now())
	},
}

func init() {
	listCmd.Flags().BoolVarP(&listLong, "long", "l", false, "long format: include each route's directory and command")
	listCmd.Flags().BoolVarP(&listAll, "all", "a", false, "list all routes (the default; accepted for the ls -la habit)")
}

func writeRouteTable(out io.Writer, routes []client.RouteInfo, worktrees []client.WorktreeInfo, long bool, home string, now time.Time) error {
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
