package cmd

import (
	"github.com/spf13/cobra"
)

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Reverse `vibe setup`: remove DNS, port forwarding, autostart, trusted CA",
	Long: `Removes everything `+ "`vibe setup`"+` installed on this machine:

  macOS:    pf LaunchDaemon, dnsmasq config, /etc/resolver/vibe, LaunchAgent,
            and the trusted CA in your Keychain
  Windows:  netsh portproxy rules, adapter DNS reset to DHCP, the vibe
            Scheduled Task, and the trusted CA in the system root store
  Linux:    not yet implemented

The vibe binary itself is left in place — delete it manually if you want
a fully clean machine.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return uninstallPlatform()
	},
}

func init() {
	rootCmd.AddCommand(uninstallCmd)
}
