//go:build windows

package cmd

import (
	"errors"
	"fmt"
	"os"
)

// setupPlatform on Windows is a Phase 1 stub. Phase 2 (this same branch)
// will:
//   - Verify Administrator via x/sys/windows.IsUserAnAdmin equivalent
//   - Generate TLS certs (cert.EnsureCA / EnsureLeaf are already portable)
//   - Trust the CA via certutil -addstore -f Root <ca.pem>
//   - Repoint the active adapter's DNS to 127.0.0.1 via netsh
//   - Install netsh portproxy rules (80 → 7999, 443 → 7443)
//   - Enable TLS in the daemon config (cross-platform JSON edit)
//   - Register a Scheduled Task on logon (schtasks /create) for autostart
//   - Verify DNS resolution
//
// Until then, this prints what would happen.
func setupPlatform() error {
	fmt.Println("Windows setup is not yet implemented in this build.")
	fmt.Println()
	fmt.Println("Tracking issue: see FUTURE_PLAN.md (\"Windows & Linux Support\")")
	fmt.Println()
	fmt.Println("Manual interim steps if you want to experiment now:")
	fmt.Println("  1. Add `127.0.0.1 local.vibe` to C:\\Windows\\System32\\drivers\\etc\\hosts")
	fmt.Println("  2. Install netsh port forwarding rules:")
	fmt.Println("       netsh interface portproxy add v4tov4 listenport=80 listenaddress=127.0.0.1 connectport=7999 connectaddress=127.0.0.1")
	fmt.Println("       netsh interface portproxy add v4tov4 listenport=443 listenaddress=127.0.0.1 connectport=7443 connectaddress=127.0.0.1")
	fmt.Println("  3. Run `vibe daemon start` (no autostart yet)")
	return errors.New("automatic setup is not yet supported on Windows")
}

// openTTYPlatform on Windows: there is no /dev/tty equivalent that's safe to
// open under arbitrary launching contexts. Fall back to stdin — promptYN
// will degrade to "default yes" when stdin isn't a terminal.
func openTTYPlatform() (*os.File, error) {
	return os.Stdin, nil
}
