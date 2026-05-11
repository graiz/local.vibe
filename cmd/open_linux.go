//go:build linux

package cmd

import "os/exec"

// openURL opens url in the default browser via xdg-open.
func openURL(url string) error {
	return exec.Command("xdg-open", url).Run()
}
