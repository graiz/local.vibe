//go:build !darwin

package cmd

import "fmt"

// redirectMechanismName names the privileged-port layer per platform. Windows
// uses netsh portproxy; other platforms vary.
func redirectMechanismName() string { return "port-forward" }

// platformRepairRedirect has no automatic implementation off darwin yet — the
// redirect there (Windows netsh portproxy, etc.) is re-established by `vibe setup`.
func platformRepairRedirect() error {
	return fmt.Errorf("automatic redirect repair isn't supported on this platform yet — re-run `vibe setup`")
}
