package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Configure DNS and autostart (one-time, requires elevation)",
	Long: `Sets up DNS so *.vibe resolves to 127.0.0.1, installs port forwarding
rules (80 → 7999, 443 → 7443), and registers an autostart hook so the daemon
runs at login.

This command is idempotent — safe to run multiple times. Platform-specific
implementations live in setup_<goos>.go.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return setupPlatform()
	},
}

type setupStep struct {
	name string
	fn   func() error
}

func runSteps(steps []setupStep) error {
	for _, step := range steps {
		fmt.Printf("  %-54s", step.name+"...")
		if err := step.fn(); err != nil {
			fmt.Println("FAILED")
			return fmt.Errorf("%s: %w", step.name, err)
		}
		fmt.Println("ok")
	}
	return nil
}

// promptYN reads a y/n answer from /dev/tty when available (so it works under
// sudo, which may redirect stdin away from the terminal). On platforms
// without /dev/tty, falls back to stdin and defaults to yes when not
// interactive (CI, pipes).
func promptYN(question string) bool {
	tty, err := openControllingTTY()
	if err != nil {
		fmt.Printf("\n%s — defaulting to yes\n", question)
		return true
	}
	defer tty.Close()

	fmt.Printf("\n%s [Y/n] ", question)
	scanner := bufio.NewScanner(tty)
	if !scanner.Scan() {
		return true
	}
	answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
	return answer == "" || answer == "y" || answer == "yes"
}

// openControllingTTY is per-OS: /dev/tty on unix, stdin fallback on Windows.
// Returns an io.Closer so the caller can defer Close().
func openControllingTTY() (*os.File, error) {
	return openTTYPlatform()
}
