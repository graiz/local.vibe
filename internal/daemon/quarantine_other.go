//go:build !darwin

package daemon

// scanQuarantinedExecutables is macOS-only: com.apple.quarantine is a macOS
// concept, and no other platform refuses to exec on an extended attribute.
func scanQuarantinedExecutables(dir, cmd string) *Recovery { return nil }
