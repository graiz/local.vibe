//go:build !windows

package cmd

// hideConsoleOnDaemonStart is a no-op on platforms with proper headless-
// daemon support — macOS LaunchAgent and Linux systemd run the binary
// without ever attaching a TTY in the first place.
func hideConsoleOnDaemonStart() {}
