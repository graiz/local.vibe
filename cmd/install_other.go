//go:build !darwin && !linux && !windows

package cmd

func installDestination() string {
	return "/usr/local/bin/vibe"
}
