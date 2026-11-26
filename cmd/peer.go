package cmd

// CLI surface for the experimental peer subsystem: `vibe peers` shows the
// paired machines and their routes; `vibe peer invite|add|remove` manages
// pairing. All of it talks to the local daemon's loopback API — the CLI
// never dials another machine directly.

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/graiz/local.vibe/internal/client"
	"github.com/graiz/local.vibe/internal/config"
	"github.com/spf13/cobra"
)

var peersCmd = &cobra.Command{
	Use:   "peers",
	Short: "List paired peer machines and their routes (experimental)",
	RunE: func(cmd *cobra.Command, args []string) error {
		c := client.New()
		resp, err := c.Peers()
		if err != nil {
			return err
		}
		if !resp.Enabled {
			fmt.Println("peers are disabled — set daemon.peers.enabled=true in ~/.vibe/config.json and restart the daemon")
			return nil
		}
		if len(resp.Peers) == 0 {
			fmt.Println("no peers paired — run `vibe peer invite` on the other machine, then `vibe peer add <host> --code <code>` here")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tHOST\tSTATUS\tROUTES")
		for _, p := range resp.Peers {
			status := "ok"
			if !p.Reachable {
				status = "unreachable"
				if p.LastError != "" {
					status = "unreachable (" + p.LastError + ")"
				}
			}
			routes := ""
			for i, r := range p.Routes {
				if i > 0 {
					routes += " "
				}
				routes += r.Name
			}
			if routes == "" {
				routes = "—"
			}
			fmt.Fprintf(w, "%s\t%s:%d\t%s\t%s\n", p.Name, p.Host, p.Port, status, routes)
		}
		if err := w.Flush(); err != nil {
			return err
		}
		// Ports held on this machine for peers' OAuth callbacks. Listed
		// because a local dev server that wants one of them gets "address in
		// use" from vibe's listener, with nothing else pointing here.
		printedHeader := false
		for _, p := range resp.Peers {
			for _, b := range p.OAuthBridges {
				if !printedHeader {
					fmt.Println("\nOAuth callback bridges on this machine:")
					printedHeader = true
				}
				state := "held by vibe"
				if !b.Bound {
					state = "NOT bound — another process holds it; sign-ins to this route will fail here"
				}
				fmt.Printf("  localhost:%d → %s on %s (%s)\n", b.Port, b.Route, p.Name, state)
			}
		}
		return nil
	},
}

var peerCmd = &cobra.Command{
	Use:   "peer",
	Short: "Pair with other vibe machines (experimental)",
}

var peerInviteCmd = &cobra.Command{
	Use:   "invite",
	Short: "Open a one-time pairing window and print the invite code",
	RunE: func(cmd *cobra.Command, args []string) error {
		c := client.New()
		resp, err := c.PeerInvite()
		if err != nil {
			return err
		}
		hostname, _ := os.Hostname()
		fmt.Printf("invite code: %s (valid until %s)\n\n", resp.Code, resp.ExpiresAt.Format("15:04:05"))
		fmt.Printf("on the other machine, run:\n\n  vibe peer add %s --code %s\n", hostname, resp.Code)
		if resp.Port != config.DefaultPeerPort {
			fmt.Printf("\n(this machine's peer port is %d — add --port %d)\n", resp.Port, resp.Port)
		}
		return nil
	},
}

var (
	peerAddCode string
	peerAddPort int
)

var peerAddCmd = &cobra.Command{
	Use:   "add <host>",
	Short: "Pair with a machine that has an open invite",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if peerAddCode == "" {
			return fmt.Errorf("--code is required (shown by `vibe peer invite` on the other machine)")
		}
		c := client.New()
		p, err := c.PeerAdd(args[0], peerAddPort, peerAddCode)
		if err != nil {
			return err
		}
		fmt.Printf("paired with %s (%s:%d)\nfingerprint %s\n", p.Name, p.Host, p.Port, p.Fingerprint)
		fmt.Println("their routes will appear in `vibe list` and resolve as https://<name>.vibe")
		return nil
	},
}

var peerRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Unpair a peer",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := client.New()
		if err := c.PeerRemove(args[0]); err != nil {
			return err
		}
		fmt.Printf("removed peer %s\n", args[0])
		return nil
	},
}

func init() {
	peerAddCmd.Flags().StringVar(&peerAddCode, "code", "", "invite code from the other machine")
	peerAddCmd.Flags().IntVar(&peerAddPort, "port", config.DefaultPeerPort, "peer listener port on the other machine")
	peerCmd.AddCommand(peerInviteCmd)
	peerCmd.AddCommand(peerAddCmd)
	peerCmd.AddCommand(peerRemoveCmd)
	rootCmd.AddCommand(peersCmd)
	rootCmd.AddCommand(peerCmd)
}
