//go:build !darwin && !windows

package cmd

// No platform autostart hook outside macOS (LaunchAgent) and Windows
// (Scheduled Task) yet: `vibe daemon start` forks, `vibe daemon stop`
// signals. A systemd user unit is the planned Linux hook — see FUTURE_PLAN.md.
func tryPlatformDaemonStart() (bool, error) { return false, nil }
func tryPlatformDaemonStop() (bool, error)  { return false, nil }
