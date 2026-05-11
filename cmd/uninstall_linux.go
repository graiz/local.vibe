//go:build linux

package cmd

import "fmt"

func uninstallPlatform() error {
	fmt.Println("Linux uninstall is not yet implemented.")
	fmt.Println("Manual steps:")
	fmt.Println("  - Stop and remove your vibe systemd unit (if you set one up)")
	fmt.Println("  - Remove /etc/dnsmasq.conf entries for *.vibe")
	fmt.Println("  - Remove iptables/nftables rules forwarding 80→7999, 443→7443")
	fmt.Println("  - sudo update-ca-certificates --remove (and NSS via certutil) to drop the local.vibe CA")
	return nil
}
