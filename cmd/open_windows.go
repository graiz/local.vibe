//go:build windows

package cmd

import (
	"os/exec"

	"github.com/graiz/local.vibe/internal/winutil"
)

// openURL opens url in the default browser via rundll32 + url.dll's
// FileProtocolHandler entry point. This is the standard shellexec hop
// without requiring a console window. Resolved through Sys32: this also runs
// at the end of an elevated `vibe setup`, where a PATH lookup is a
// privilege-escalation surface.
func openURL(url string) error {
	return exec.Command(winutil.Sys32("rundll32"), "url.dll,FileProtocolHandler", url).Start()
}
