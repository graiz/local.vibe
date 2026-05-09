//go:build linux

package cmd

import (
	"fmt"
	"os"
)

// setupPlatform on Linux currently prints manual setup instructions. Phase 2
// of the cross-platform port (a separate branch) will wire up systemd-resolved
// for DNS, nftables for port forwarding, a `vibe.service` user unit, and CA
// trust via update-ca-certificates + NSS certutil.
func setupPlatform() error {
	fmt.Println("Linux setup:")
	fmt.Println()
	fmt.Println("1. Install and configure dnsmasq:")
	fmt.Println("     sudo apt install dnsmasq")
	fmt.Println("     echo 'address=/.vibe/127.0.0.1' | sudo tee -a /etc/dnsmasq.conf")
	fmt.Println("     sudo systemctl restart dnsmasq")
	fmt.Println()
	fmt.Println("2. Port 80 forwarding:")
	fmt.Println("     sudo iptables -t nat -A OUTPUT -p tcp -d 127.0.0.1 --dport 80 -j REDIRECT --to-port 7999")
	fmt.Println()
	fmt.Println("3. Autostart (systemd user service):")
	fmt.Println("     systemctl --user enable --now vibe")
	return nil
}

func openTTYPlatform() (*os.File, error) {
	return os.Open("/dev/tty")
}
