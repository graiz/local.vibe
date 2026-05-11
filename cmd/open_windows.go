//go:build windows

package cmd

import "os/exec"

// openURL opens url in the default browser via rundll32 + url.dll's
// FileProtocolHandler entry point. This is the standard shellexec hop
// without requiring a console window.
func openURL(url string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}
