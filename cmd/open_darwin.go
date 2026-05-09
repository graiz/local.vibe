//go:build darwin

package cmd

import "os/exec"

// openURL opens url in the default browser via the macOS `open` command.
func openURL(url string) error {
	return exec.Command("open", url).Run()
}
